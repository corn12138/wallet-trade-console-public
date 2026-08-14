package marketstream

import (
	"context"
	"errors"
	"testing"
	"time"
)

// listFunc builds a ListMarketsFunc that records the chain filter it was called
// with and returns a fixed market set.
func listFunc(record *[]int, markets ...TrackedMarket) ListMarketsFunc {
	return func(_ context.Context, chainID *int) ([]TrackedMarket, error) {
		if record != nil {
			if chainID == nil {
				*record = append(*record, 0)
			} else {
				*record = append(*record, *chainID)
			}
		}
		return markets, nil
	}
}

func symbolsOf(ms []TrackedMarket) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Symbol
	}
	return out
}

func TestBuildTrackedFunc_EmptySymbolsAutoTracksAll(t *testing.T) {
	var chains []int
	cfg := Config{ChainID: 11155111} // empty Symbols => auto-track
	tracked := cfg.BuildTrackedFunc(listFunc(&chains,
		TrackedMarket{Symbol: "ETH-USD", ChainID: 11155111},
		TrackedMarket{Symbol: "BTC-USD", ChainID: 11155111},
	))

	got, err := tracked(context.Background())
	if err != nil {
		t.Fatalf("tracked: %v", err)
	}
	if syms := symbolsOf(got); len(syms) != 2 || syms[0] != "ETH-USD" || syms[1] != "BTC-USD" {
		t.Errorf("empty symbols should auto-track all markets, got %v", syms)
	}
	// chain filter must be the configured chain, not nil.
	if len(chains) != 1 || chains[0] != 11155111 {
		t.Errorf("expected chain filter 11155111 passed to lister, got %v", chains)
	}
}

func TestBuildTrackedFunc_NonEmptyFilters(t *testing.T) {
	cfg := Config{ChainID: 11155111, Symbols: []string{"ETH-USD"}}
	tracked := cfg.BuildTrackedFunc(listFunc(nil,
		TrackedMarket{Symbol: "ETH-USD", ChainID: 11155111},
		TrackedMarket{Symbol: "BTC-USD", ChainID: 11155111},
	))

	got, err := tracked(context.Background())
	if err != nil {
		t.Fatalf("tracked: %v", err)
	}
	if syms := symbolsOf(got); len(syms) != 1 || syms[0] != "ETH-USD" {
		t.Errorf("non-empty symbols should filter, got %v", syms)
	}
}

func TestBuildTrackedFunc_FilterIsCaseInsensitive(t *testing.T) {
	cfg := Config{Symbols: []string{"eth-usd"}} // lower-case on both sides
	tracked := cfg.BuildTrackedFunc(listFunc(nil,
		TrackedMarket{Symbol: "ETH-USD"},
		TrackedMarket{Symbol: "btc-usd"},
	))
	got, _ := tracked(context.Background())
	if syms := symbolsOf(got); len(syms) != 1 || syms[0] != "ETH-USD" {
		t.Errorf("filter should be case-insensitive, got %v", syms)
	}
}

func TestBuildTrackedFunc_AllChainsWhenChainZero(t *testing.T) {
	var chains []int
	cfg := Config{} // ChainID 0 => nil filter (all chains)
	tracked := cfg.BuildTrackedFunc(listFunc(&chains, TrackedMarket{Symbol: "ETH-USD"}))
	if _, err := tracked(context.Background()); err != nil {
		t.Fatalf("tracked: %v", err)
	}
	if len(chains) != 1 || chains[0] != 0 {
		t.Errorf("chain 0 should pass nil filter (recorded as 0), got %v", chains)
	}
}

func TestBuildTrackedFunc_ListErrorPropagates(t *testing.T) {
	boom := errors.New("db down")
	cfg := Config{}
	tracked := cfg.BuildTrackedFunc(func(context.Context, *int) ([]TrackedMarket, error) {
		return nil, boom
	})
	if _, err := tracked(context.Background()); !errors.Is(err, boom) {
		t.Errorf("list error should propagate, got %v", err)
	}
}

func TestConfigFromEnv_Defaults(t *testing.T) {
	for _, k := range []string{
		"MARKET_STREAM_ENABLED", "MARKET_STREAM_RUN_IN_API", "MARKET_STREAM_REDIS_URL",
		"REDIS_URL", "MARKET_STREAM_CHANNEL_PREFIX", "MARKET_STREAM_CHAIN_ID",
		"MARKET_STREAM_SYMBOLS", "MARKET_STREAM_REFRESH_INTERVAL_MS",
	} {
		t.Setenv(k, "")
	}
	cfg := ConfigFromEnv()
	if !cfg.Enabled || !cfg.RunInAPI {
		t.Errorf("enabled/runInApi should default true, got %+v", cfg)
	}
	if cfg.RedisURL != "" {
		t.Errorf("redis url should default empty, got %q", cfg.RedisURL)
	}
	if cfg.RefreshInterval != DefaultRefreshInterval {
		t.Errorf("refresh interval should default %v, got %v", DefaultRefreshInterval, cfg.RefreshInterval)
	}
	if len(cfg.Symbols) != 0 {
		t.Errorf("symbols should default empty (auto-track), got %v", cfg.Symbols)
	}
}

func TestConfigFromEnv_Parsing(t *testing.T) {
	t.Setenv("MARKET_STREAM_ENABLED", "true")
	t.Setenv("MARKET_STREAM_RUN_IN_API", "false")
	t.Setenv("MARKET_STREAM_REDIS_URL", "redis://127.0.0.1:6739")
	t.Setenv("MARKET_STREAM_CHANNEL_PREFIX", "ms")
	t.Setenv("MARKET_STREAM_CHAIN_ID", "11155111")
	t.Setenv("MARKET_STREAM_SYMBOLS", " eth-usd , btc-usd ,, ")
	t.Setenv("MARKET_STREAM_REFRESH_INTERVAL_MS", "2500")

	cfg := ConfigFromEnv()
	if cfg.Enabled != true || cfg.RunInAPI != false {
		t.Errorf("enabled/runInApi parse: %+v", cfg)
	}
	if cfg.RedisURL != "redis://127.0.0.1:6739" || cfg.ChannelPrefix != "ms" {
		t.Errorf("redis/prefix parse: %+v", cfg)
	}
	if cfg.ChainID != 11155111 {
		t.Errorf("chainId parse: %d", cfg.ChainID)
	}
	if len(cfg.Symbols) != 2 || cfg.Symbols[0] != "ETH-USD" || cfg.Symbols[1] != "BTC-USD" {
		t.Errorf("symbols parse (trim/upper/drop-empty): %v", cfg.Symbols)
	}
	if cfg.RefreshInterval != 2500*time.Millisecond {
		t.Errorf("refresh parse: %v", cfg.RefreshInterval)
	}
}

func TestConfigFromEnv_RedisURLFallsBackToGeneric(t *testing.T) {
	t.Setenv("MARKET_STREAM_REDIS_URL", "")
	t.Setenv("REDIS_URL", "redis://127.0.0.1:6379")
	if got := ConfigFromEnv().RedisURL; got != "redis://127.0.0.1:6379" {
		t.Errorf("should fall back to REDIS_URL, got %q", got)
	}
}

func TestBuildDriver(t *testing.T) {
	// Memory when no redis URL.
	mem, shared, err := Config{}.BuildDriver()
	if err != nil || shared {
		t.Errorf("no redis url => memory/non-shared, got shared=%v err=%v", shared, err)
	}
	if _, ok := mem.(*MemoryDriver); !ok {
		t.Errorf("expected *MemoryDriver, got %T", mem)
	}

	// Redis when a valid URL is set (NewRedisDriver only parses; no connect).
	rd, shared, err := Config{RedisURL: "redis://127.0.0.1:6379"}.BuildDriver()
	if err != nil || !shared {
		t.Errorf("valid redis url => redis/shared, got shared=%v err=%v", shared, err)
	}
	if _, ok := rd.(*RedisDriver); !ok {
		t.Errorf("expected *RedisDriver, got %T", rd)
	}

	// Invalid URL falls back to memory and surfaces the error.
	fb, shared, err := Config{RedisURL: "not-a-redis-url"}.BuildDriver()
	if err == nil || shared {
		t.Errorf("invalid redis url => memory fallback + error, got shared=%v err=%v", shared, err)
	}
	if _, ok := fb.(*MemoryDriver); !ok {
		t.Errorf("expected *MemoryDriver fallback, got %T", fb)
	}
}
