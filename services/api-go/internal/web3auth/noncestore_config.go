package web3auth

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// NonceStoreConfig selects and configures the challenge store.
type NonceStoreConfig struct {
	// Kind is "redis" or "memory". Empty resolves to "redis" when
	// RequireShared is set and "memory" otherwise.
	Kind string
	// RedisURL is required when Kind resolves to "redis".
	RedisURL string
	// RedisPrefix namespaces the keys; empty uses DefaultNonceRedisPrefix.
	RedisPrefix string
	// RequireShared is the fail-closed switch, set for every process except a
	// development/test one listening on loopback. When true, a memory store is
	// refused and an unreachable Redis is a startup error.
	RequireShared bool
}

// NonceStoreConfigFromEnv reads the store selection from the process
// environment. SIWE_NONCE_REDIS_URL wins over the shared REDIS_URL so the
// auth store can be pointed at a separate instance without moving market
// fan-out with it. Whether a shared store is required is decided by cmd/api
// from the validated deployment profile, not re-derived here.
func NonceStoreConfigFromEnv(requireShared bool) NonceStoreConfig {
	return NonceStoreConfig{
		Kind:          strings.ToLower(strings.TrimSpace(os.Getenv("SIWE_NONCE_STORE"))),
		RedisURL:      firstNonEmptyEnv("SIWE_NONCE_REDIS_URL", "REDIS_URL"),
		RedisPrefix:   strings.TrimSpace(os.Getenv("SIWE_NONCE_REDIS_PREFIX")),
		RequireShared: requireShared,
	}
}

// ResolveNonceStore builds the store, or fails.
//
// It never degrades. A deployment that cannot reach its shared store gets a
// process that refuses to start, because the alternative — an in-memory
// fallback — produces logins that succeed while replay protection is silently
// process-local. Nothing in the browser, the wallet, or the response body would
// show the difference.
func ResolveNonceStore(ctx context.Context, cfg NonceStoreConfig) (NonceStore, error) {
	kind := cfg.Kind
	if kind == "" {
		if cfg.RequireShared {
			kind = "redis"
		} else {
			kind = "memory"
		}
	}

	switch kind {
	case "memory":
		if cfg.RequireShared {
			return nil, fmt.Errorf(
				"web3auth: SIWE_NONCE_STORE=memory is refused outside a loopback development/test process " +
					"(a per-process nonce map cannot prevent replay across replicas); " +
					"set SIWE_NONCE_REDIS_URL or REDIS_URL")
		}
		return NewMemoryNonceStore(), nil
	case "redis":
		if strings.TrimSpace(cfg.RedisURL) == "" {
			return nil, fmt.Errorf(
				"web3auth: shared SIWE nonce store selected but no target configured; " +
					"set SIWE_NONCE_REDIS_URL or REDIS_URL")
		}
		return NewRedisNonceStore(ctx, cfg.RedisURL, cfg.RedisPrefix)
	default:
		return nil, fmt.Errorf("web3auth: unknown SIWE_NONCE_STORE %q (want \"redis\" or \"memory\")", cfg.Kind)
	}
}

func firstNonEmptyEnv(keys ...string) string {
	for _, key := range keys {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	return ""
}
