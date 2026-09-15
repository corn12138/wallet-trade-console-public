package web3auth

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// requireAcceptanceRedis returns an explicit, run-specific Redis target.
//
// The variable is deliberately not defaulted to localhost. When it is unset the
// test is skipped and the capability stays BLOCKED; when it is set the test
// FAILS on an unreachable target rather than skipping, so a broken acceptance
// environment can never be mistaken for a pass.
func requireAcceptanceRedis(t *testing.T) (string, string) {
	t.Helper()
	url := strings.TrimSpace(os.Getenv("WEB3AUTH_TEST_REDIS_URL"))
	if url == "" {
		t.Skip("WEB3AUTH_TEST_REDIS_URL unset — cross-replica nonce behaviour is UNPROVEN, not passing")
	}
	prefix := strings.TrimSpace(os.Getenv("WEB3AUTH_TEST_REDIS_PREFIX"))
	if prefix == "" {
		t.Fatal("WEB3AUTH_TEST_REDIS_URL is set but WEB3AUTH_TEST_REDIS_PREFIX is not; " +
			"a run-specific prefix is required so the test cannot collide with app data")
	}
	return url, prefix
}

func newAcceptanceRedisStore(t *testing.T, url, prefix string) *RedisNonceStore {
	t.Helper()
	store, err := NewRedisNonceStore(context.Background(), url, prefix)
	if err != nil {
		t.Fatalf("acceptance Redis is configured but unusable: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// TestRedisNonceStore_SecondReplicaCannotReuseAConsumedNonce is the property
// the in-memory store cannot have: replica A issues, replica B consumes, and
// neither can consume again.
func TestRedisNonceStore_SecondReplicaCannotReuseAConsumedNonce(t *testing.T) {
	url, prefix := requireAcceptanceRedis(t)
	replicaA := newAcceptanceRedisStore(t, url, prefix)
	replicaB := newAcceptanceRedisStore(t, url, prefix)
	ctx := context.Background()

	nonce := "cross-replica-" + t.Name()
	issued := Challenge{
		IssuedAt: Now(), ExpiresAt: Now().Add(time.Minute),
		Domain: "localhost:3002", URI: "http://localhost:3002",
		Statement: "s", AllowedChainIDs: []int{11155111},
	}
	if err := replicaA.Issue(ctx, nonce, issued); err != nil {
		t.Fatalf("replica A issue: %v", err)
	}

	got, err := replicaB.Consume(ctx, nonce)
	if err != nil {
		t.Fatalf("replica B could not consume a challenge replica A issued: %v", err)
	}
	if got.Domain != issued.Domain || got.URI != issued.URI || got.Statement != issued.Statement {
		t.Errorf("the issuing replica's configuration did not travel with the nonce: %+v", got)
	}
	if len(got.AllowedChainIDs) != 1 || got.AllowedChainIDs[0] != 11155111 {
		t.Errorf("allowed chains did not survive the round trip: %+v", got.AllowedChainIDs)
	}

	if _, err := replicaA.Consume(ctx, nonce); err != ErrNonceUnknown {
		t.Errorf("replica A consume after replica B = %v, want ErrNonceUnknown (replay)", err)
	}
	if _, err := replicaB.Consume(ctx, nonce); err != ErrNonceUnknown {
		t.Errorf("replica B re-consume = %v, want ErrNonceUnknown", err)
	}
}

// TestRedisNonceStore_ConcurrentReplicasHaveExactlyOneWinner races independent
// clients — the shape of two API pods receiving the same replayed request at
// the same moment.
func TestRedisNonceStore_ConcurrentReplicasHaveExactlyOneWinner(t *testing.T) {
	url, prefix := requireAcceptanceRedis(t)
	ctx := context.Background()

	const replicas = 16
	stores := make([]*RedisNonceStore, replicas)
	for i := range stores {
		stores[i] = newAcceptanceRedisStore(t, url, prefix)
	}

	nonce := "race-" + t.Name()
	if err := stores[0].Issue(ctx, nonce, Challenge{ExpiresAt: Now().Add(time.Minute)}); err != nil {
		t.Fatalf("issue: %v", err)
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	wins, unknown := 0, 0
	var otherErrs []error
	start := make(chan struct{})
	for _, store := range stores {
		wg.Add(1)
		go func(s *RedisNonceStore) {
			defer wg.Done()
			<-start
			_, err := s.Consume(ctx, nonce)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins++
			case err == ErrNonceUnknown:
				unknown++
			default:
				otherErrs = append(otherErrs, err)
			}
		}(store)
	}
	close(start)
	wg.Wait()

	if len(otherErrs) > 0 {
		t.Fatalf("unexpected consume errors: %v", otherErrs)
	}
	if wins != 1 {
		t.Errorf("winners = %d, want exactly 1", wins)
	}
	if unknown != replicas-1 {
		t.Errorf("losers = %d, want %d", unknown, replicas-1)
	}
}

// A nonce must not outlive its TTL in Redis either — expiry is enforced by the
// key TTL, not only by the timestamp inside the payload.
func TestRedisNonceStore_TTLExpiresTheChallenge(t *testing.T) {
	url, prefix := requireAcceptanceRedis(t)
	store := newAcceptanceRedisStore(t, url, prefix)
	ctx := context.Background()

	nonce := "ttl-" + t.Name()
	if err := store.Issue(ctx, nonce, Challenge{ExpiresAt: Now().Add(900 * time.Millisecond)}); err != nil {
		t.Fatalf("issue: %v", err)
	}
	ttl, err := store.client.TTL(ctx, store.key(nonce)).Result()
	if err != nil {
		t.Fatalf("ttl: %v", err)
	}
	if ttl <= 0 || ttl > time.Second {
		t.Errorf("redis ttl = %v, want (0, 1s]", ttl)
	}

	time.Sleep(1200 * time.Millisecond)
	if _, err := store.Consume(ctx, nonce); err != ErrNonceUnknown {
		t.Errorf("consume after TTL = %v, want ErrNonceUnknown", err)
	}
}

// Two full Service instances sharing one Redis: the end-to-end replay the
// in-memory store allowed.
func TestService_ReplayAcrossReplicasIsRejected(t *testing.T) {
	url, prefix := requireAcceptanceRedis(t)
	replicaA := NewServiceWithConfig("shared-secret", Config{
		AllowedChainIDs: []int{11155111},
		NonceStore:      newAcceptanceRedisStore(t, url, prefix),
	})
	replicaB := NewServiceWithConfig("shared-secret", Config{
		AllowedChainIDs: []int{11155111},
		NonceStore:      newAcceptanceRedisStore(t, url, prefix),
	})

	priv, addr := acceptanceKey(t)
	challenge, err := replicaA.GenerateNonceFor(context.Background(), addr)
	if err != nil {
		t.Fatalf("replica A nonce: %v", err)
	}
	message := buildMessage(addr, 11155111, challenge)
	sig := signEthMessage(t, priv, message)
	in := VerifyInput{Message: message, Signature: sig}

	if _, err := replicaB.VerifySiwe(context.Background(), in); err != nil {
		t.Fatalf("replica B must accept a challenge replica A issued: %v", err)
	}
	if _, err := replicaA.VerifySiwe(context.Background(), in); err != ErrNonceUnknown {
		t.Fatalf("replaying the same signed message on replica A = %v, want ErrNonceUnknown", err)
	}
}
