package web3auth

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMemoryNonceStore_ConsumesExactlyOnce(t *testing.T) {
	store := NewMemoryNonceStore()
	ctx := context.Background()
	challenge := Challenge{IssuedAt: Now(), ExpiresAt: Now().Add(time.Minute), Domain: "d", URI: "https://d"}

	if err := store.Issue(ctx, "n1", challenge); err != nil {
		t.Fatalf("issue: %v", err)
	}
	got, err := store.Consume(ctx, "n1")
	if err != nil {
		t.Fatalf("first consume: %v", err)
	}
	if got.Domain != "d" {
		t.Errorf("challenge did not round-trip: %+v", got)
	}
	if _, err := store.Consume(ctx, "n1"); err != ErrNonceUnknown {
		t.Errorf("second consume = %v, want ErrNonceUnknown", err)
	}
}

// The memory store is the single-process guard, so its atomicity still has to
// hold under concurrent consumers inside that process.
func TestMemoryNonceStore_ConcurrentConsumeHasOneWinner(t *testing.T) {
	store := NewMemoryNonceStore()
	ctx := context.Background()
	if err := store.Issue(ctx, "race", Challenge{ExpiresAt: Now().Add(time.Minute)}); err != nil {
		t.Fatalf("issue: %v", err)
	}

	const racers = 32
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := store.Consume(ctx, "race"); err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()

	if wins != 1 {
		t.Errorf("winners = %d, want exactly 1", wins)
	}
	if store.Len() != 0 {
		t.Errorf("store retained %d challenges after consumption", store.Len())
	}
}

func TestMemoryNonceStore_ExpiredChallengeIsNotUsable(t *testing.T) {
	store := NewMemoryNonceStore()
	ctx := context.Background()
	if err := store.Issue(ctx, "old", Challenge{ExpiresAt: Now().Add(-time.Second)}); err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, err := store.Consume(ctx, "old"); err != ErrNonceExpired {
		t.Errorf("consume = %v, want ErrNonceExpired", err)
	}
	// Even an expired challenge is removed, so it cannot be retried.
	if _, err := store.Consume(ctx, "old"); err != ErrNonceUnknown {
		t.Errorf("re-consume = %v, want ErrNonceUnknown", err)
	}
}

func TestResolveNonceStore_SharedRequiredRefusesMemory(t *testing.T) {
	_, err := ResolveNonceStore(context.Background(), NonceStoreConfig{
		Kind:          "memory",
		RequireShared: true,
	})
	if err == nil {
		t.Fatal("a deployed process + memory must not resolve")
	}
	if !strings.Contains(err.Error(), "SIWE_NONCE_STORE=memory is refused") {
		t.Errorf("error should name the refused setting, got %q", err)
	}
}

func TestResolveNonceStore_SharedRequiredRefusesMissingRedisTarget(t *testing.T) {
	// Kind empty resolves to redis when a shared store is required; with no
	// URL that must fail rather than silently fall back to the in-process map.
	_, err := ResolveNonceStore(context.Background(), NonceStoreConfig{RequireShared: true})
	if err == nil {
		t.Fatal("a deployed process with no redis target must not resolve")
	}
	if !strings.Contains(err.Error(), "SIWE_NONCE_REDIS_URL") {
		t.Errorf("error should name the variable to set, got %q", err)
	}
}

func TestResolveNonceStore_SharedRequiredRefusesUnreachableRedis(t *testing.T) {
	// Port 1 is reserved and never listening; construction must fail closed
	// instead of degrading.
	store, err := ResolveNonceStore(context.Background(), NonceStoreConfig{
		Kind:          "redis",
		RedisURL:      "redis://127.0.0.1:1/0",
		RequireShared: true,
	})
	if err == nil {
		_ = store.Close()
		t.Fatal("unreachable redis must not resolve")
	}
	if strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Errorf("redis target must be redacted from the error, got %q", err)
	}
}

func TestResolveNonceStore_LocalTestDefaultsToMemory(t *testing.T) {
	store, err := ResolveNonceStore(context.Background(), NonceStoreConfig{})
	if err != nil {
		t.Fatalf("development resolve: %v", err)
	}
	defer store.Close()
	if store.Kind() != "memory" {
		t.Errorf("kind = %q, want memory", store.Kind())
	}
}

func TestResolveNonceStore_RejectsUnknownKind(t *testing.T) {
	if _, err := ResolveNonceStore(context.Background(), NonceStoreConfig{Kind: "postgres"}); err == nil {
		t.Fatal("unknown store kind must not resolve")
	}
}

func TestNonceStoreConfigFromEnv_PrefersTheDedicatedTarget(t *testing.T) {
	t.Setenv("REDIS_URL", "redis://shared:6379/0")
	t.Setenv("SIWE_NONCE_REDIS_URL", "redis://auth:6379/2")
	t.Setenv("SIWE_NONCE_STORE", "redis")

	cfg := NonceStoreConfigFromEnv(true)
	if cfg.RedisURL != "redis://auth:6379/2" {
		t.Errorf("RedisURL = %q, want the dedicated auth target", cfg.RedisURL)
	}
	if !cfg.RequireShared {
		t.Error("the caller's requirement must be kept")
	}
}

func TestNonceStoreConfigFromEnv_FallsBackToTheSharedRedis(t *testing.T) {
	t.Setenv("REDIS_URL", "redis://shared:6379/0")
	t.Setenv("SIWE_NONCE_REDIS_URL", "")
	if got := NonceStoreConfigFromEnv(false).RedisURL; got != "redis://shared:6379/0" {
		t.Errorf("RedisURL = %q, want the shared target", got)
	}
}

// The whole point of the slice: a deployed process (anything but a loopback
// development/test one) with no shared store does not start.
func TestNewServiceFromEnv_DeployedWithoutSharedStoreRefusesToStart(t *testing.T) {
	t.Setenv("REDIS_URL", "")
	t.Setenv("SIWE_NONCE_REDIS_URL", "")
	for _, kind := range []string{"", "memory"} {
		t.Setenv("SIWE_NONCE_STORE", kind)
		svc, err := NewServiceFromEnv(context.Background(), Runtime{})
		if err == nil {
			_ = svc.Close()
			t.Fatalf("SIWE_NONCE_STORE=%q: a deployed process without a shared nonce store must not construct a service", kind)
		}
	}
}

func TestNewServiceFromEnv_LocalTestStartsOnMemory(t *testing.T) {
	t.Setenv("REDIS_URL", "")
	t.Setenv("SIWE_NONCE_REDIS_URL", "")
	t.Setenv("SIWE_NONCE_STORE", "")

	svc, err := NewServiceFromEnv(context.Background(), Runtime{LocalTest: true})
	if err != nil {
		t.Fatalf("a loopback development/test process must start without Redis: %v", err)
	}
	defer svc.Close()
	if svc.NonceStoreKind() != "memory" {
		t.Errorf("store kind = %q, want memory", svc.NonceStoreKind())
	}
}
