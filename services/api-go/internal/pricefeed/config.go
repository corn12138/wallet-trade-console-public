package pricefeed

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// Config drives the ingest worker. Every knob is env-driven so the same binary
// runs in dev and prod; defaults are chosen so that simply setting
// PRICEFEED_ENABLED=true produces a useful chart.
type Config struct {
	Enabled bool

	// Symbols to ingest, canonical form ("ETH-USD").
	Symbols []string

	// Market (off-chain venue) ingestion.
	MarketEnabled  bool
	MarketBaseURL  string // "" → public Gate.io endpoint
	MarketInterval time.Duration
	// Resolutions to keep materialized. Wider buckets are cheap and make the
	// chart's timeframe switcher work without re-deriving from 1m.
	Resolutions []Resolution
	// Backfill depth per resolution on each poll. The venue returns the most
	// recent N buckets, so this doubles as "how much history we hold".
	MarketLimit int

	// Oracle (on-chain Chainlink) ingestion.
	OracleEnabled  bool
	OracleInterval time.Duration
	OracleRPCURL   string
	OracleChainID  int
	Feeds          []FeedRef
}

// DefaultSymbols are the markets the product lists.
func DefaultSymbols() []string { return []string{"ETH-USD", "BTC-USD"} }

// ConfigFromEnv builds the worker config.
//
// PRICEFEED_ENABLED           (default false) — master switch.
// PRICEFEED_SYMBOLS           (default "ETH-USD,BTC-USD")
// PRICEFEED_MARKET_ENABLED    (default true when the feed is on)
// PRICEFEED_MARKET_BASE_URL   (default public Gate.io v4)
// PRICEFEED_MARKET_INTERVAL_MS(default 60000)
// PRICEFEED_RESOLUTIONS       (default "1m,5m,15m,1h,4h,1d")
// PRICEFEED_MARKET_LIMIT      (default 500)
// PRICEFEED_ORACLE_ENABLED    (default true when the feed is on)
// PRICEFEED_ORACLE_INTERVAL_MS(default 120000)
// PRICEFEED_ORACLE_RPC_URL    (falls back to SEPOLIA_HTTPS_RPC / RPC_URL)
// PRICEFEED_ORACLE_CHAIN_ID   (default 11155111)
func ConfigFromEnv() Config {
	enabled := envBool("PRICEFEED_ENABLED", false)
	cfg := Config{
		Enabled:        enabled,
		Symbols:        envList("PRICEFEED_SYMBOLS", DefaultSymbols()),
		MarketEnabled:  envBool("PRICEFEED_MARKET_ENABLED", true),
		MarketBaseURL:  strings.TrimSpace(os.Getenv("PRICEFEED_MARKET_BASE_URL")),
		MarketInterval: envDuration("PRICEFEED_MARKET_INTERVAL_MS", time.Minute),
		MarketLimit:    envInt("PRICEFEED_MARKET_LIMIT", 500),
		OracleEnabled:  envBool("PRICEFEED_ORACLE_ENABLED", true),
		OracleInterval: envDuration("PRICEFEED_ORACLE_INTERVAL_MS", 2*time.Minute),
		OracleChainID:  envInt("PRICEFEED_ORACLE_CHAIN_ID", 11155111),
	}

	cfg.Resolutions = parseResolutionList(os.Getenv("PRICEFEED_RESOLUTIONS"))

	cfg.OracleRPCURL = firstNonEmpty(
		os.Getenv("PRICEFEED_ORACLE_RPC_URL"),
		os.Getenv("SEPOLIA_HTTPS_RPC"),
		os.Getenv("RPC_URL"),
	)

	// Feeds are the published Sepolia aggregators filtered to the configured
	// symbols, so PRICEFEED_SYMBOLS is the single place markets are listed.
	wanted := map[string]bool{}
	for _, s := range cfg.Symbols {
		wanted[NormalizeSymbol(s)] = true
	}
	for _, f := range SepoliaFeeds() {
		if f.ChainID == cfg.OracleChainID && wanted[NormalizeSymbol(f.Symbol)] {
			cfg.Feeds = append(cfg.Feeds, f)
		}
	}

	return cfg
}

// parseResolutionList validates each entry and drops unknown ones rather than
// coercing them, so a typo shrinks coverage visibly instead of silently
// materializing the wrong bucket width.
func parseResolutionList(raw string) []Resolution {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return SupportedResolutions()
	}
	out := []Resolution{}
	for part := range strings.SplitSeq(raw, ",") {
		if r, err := ParseResolution(part); err == nil {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return SupportedResolutions()
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

func envBool(key string, fallback bool) bool {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch raw {
	case "":
		return fallback
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

func envInt(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

func envDuration(key string, fallback time.Duration) time.Duration {
	ms := envInt(key, int(fallback/time.Millisecond))
	return time.Duration(ms) * time.Millisecond
}

func envList(key string, fallback []string) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	out := []string{}
	for part := range strings.SplitSeq(raw, ",") {
		if s := NormalizeSymbol(part); s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return fallback
	}
	return out
}
