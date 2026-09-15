package web3auth

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// DefaultNonceRedisPrefix namespaces SIWE challenges away from the market
// snapshot keys that share the instance.
const DefaultNonceRedisPrefix = "web3auth:siwe:nonce"

// RedisNonceStore is the multi-replica store. Consumption is a single GETDEL,
// which is atomic server-side: there is no window in which two replicas both
// read a challenge before either deletes it. A GET-then-DEL pair, or a
// WATCH/MULTI retry loop, would each leave that window open.
//
// GETDEL requires Redis >= 6.2; NewRedisNonceStore proves the command exists at
// construction rather than discovering it on the first login.
type RedisNonceStore struct {
	client *redis.Client
	prefix string
}

// NewRedisNonceStore dials redisURL and verifies both reachability and GETDEL
// support. It returns an error instead of a degraded store: an authentication
// store that silently loses its replay guard looks identical to a working one
// from the browser's side.
func NewRedisNonceStore(ctx context.Context, redisURL, prefix string) (*RedisNonceStore, error) {
	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("web3auth: parse nonce redis url: %w", err)
	}
	if strings.TrimSpace(prefix) == "" {
		prefix = DefaultNonceRedisPrefix
	}
	store := &RedisNonceStore{client: redis.NewClient(opt), prefix: strings.TrimSpace(prefix)}
	if err := store.client.Ping(ctx).Err(); err != nil {
		_ = store.client.Close()
		return nil, fmt.Errorf("web3auth: nonce redis unreachable: %w", redactRedisErr(err))
	}
	if err := store.probeGetDel(ctx); err != nil {
		_ = store.client.Close()
		return nil, err
	}
	return store, nil
}

// probeGetDel fails construction on a Redis too old for atomic consumption.
func (s *RedisNonceStore) probeGetDel(ctx context.Context) error {
	key := s.key("__probe__")
	if err := s.client.Set(ctx, key, "1", 5*time.Second).Err(); err != nil {
		return fmt.Errorf("web3auth: nonce redis probe write: %w", redactRedisErr(err))
	}
	if err := s.client.GetDel(ctx, key).Err(); err != nil && !errors.Is(err, redis.Nil) {
		return fmt.Errorf("web3auth: nonce redis lacks atomic GETDEL: %w", redactRedisErr(err))
	}
	return nil
}

func (s *RedisNonceStore) Kind() string { return "redis" }

func (s *RedisNonceStore) Close() error { return s.client.Close() }

func (s *RedisNonceStore) key(nonce string) string { return s.prefix + ":" + nonce }

// Issue writes the challenge with NX so a nonce collision cannot overwrite a
// live challenge, and with the challenge's own remaining lifetime as the key
// TTL so Redis expiry and the stored expiry cannot drift apart.
func (s *RedisNonceStore) Issue(ctx context.Context, nonce string, challenge Challenge) error {
	payload, err := encodeChallenge(challenge)
	if err != nil {
		return fmt.Errorf("web3auth: encode challenge: %w", err)
	}
	ttl := challenge.ExpiresAt.Sub(Now())
	if ttl <= 0 {
		return fmt.Errorf("web3auth: refusing to issue an already-expired challenge")
	}
	stored, err := s.client.SetNX(ctx, s.key(nonce), payload, ttl).Result()
	if err != nil {
		return fmt.Errorf("web3auth: nonce redis write: %w", redactRedisErr(err))
	}
	if !stored {
		return fmt.Errorf("web3auth: nonce already issued")
	}
	return nil
}

// Consume is the cross-replica replay guard. Exactly one GETDEL returns the
// payload; every other caller gets redis.Nil and therefore ErrNonceUnknown.
func (s *RedisNonceStore) Consume(ctx context.Context, nonce string) (Challenge, error) {
	raw, err := s.client.GetDel(ctx, s.key(nonce)).Bytes()
	if errors.Is(err, redis.Nil) {
		return Challenge{}, ErrNonceUnknown
	}
	if err != nil {
		// A transport failure is not "nonce unknown" and must not be reported
		// as one: the caller has to fail the login, not retry into a store
		// that may still hold the challenge.
		return Challenge{}, fmt.Errorf("web3auth: nonce redis read: %w", redactRedisErr(err))
	}
	challenge, err := decodeChallenge(raw)
	if err != nil {
		return Challenge{}, fmt.Errorf("web3auth: decode challenge: %w", err)
	}
	if Now().After(challenge.ExpiresAt) {
		return Challenge{}, ErrNonceExpired
	}
	return challenge, nil
}

// redactRedisErr keeps the target out of anything a handler might log or
// return. go-redis surfaces the dial target verbatim ("dial tcp 127.0.0.1:6379:
// connect: connection refused"), which is internal topology.
var redisTargetRE = regexp.MustCompile(
	`redis(?:s)?://\S+` +
		`|\[[0-9a-fA-F:]+\]:\d{1,5}` +
		`|\b(?:\d{1,3}\.){3}\d{1,3}:\d{1,5}\b` +
		`|\blocalhost:\d{1,5}\b` +
		`|\b[A-Za-z0-9\-]+(?:\.[A-Za-z0-9\-]+)+:\d{1,5}\b`)

func redactRedisErr(err error) error {
	if err == nil {
		return nil
	}
	return errors.New(redisTargetRE.ReplaceAllString(err.Error(), "[redacted]"))
}
