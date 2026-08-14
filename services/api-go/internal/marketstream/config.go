package marketstream

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the resolved MARKET_STREAM_* environment, shared by the API process
// (cmd/api) and the standalone worker (cmd/market-stream) so their behavior
// agrees. Mirrors the NestJS `marketStream` config tree
// (legacy NestJS config/configuration.ts) + the worker/gateway split.
type Config struct {
	// Enabled mirrors marketStream.enabled (default true). When false, no
	// worker runs in either process.
	Enabled bool
	// RunInAPI mirrors marketStream.runInApi (default true). When false, the
	// API process is gateway-only (subscribes to Redis, never computes/publishes)
	// and a separate cmd/market-stream process produces snapshots — the
	// production topology from services/api/ecosystem.config.cjs.
	RunInAPI bool
	// RedisURL is MARKET_STREAM_REDIS_URL ?? REDIS_URL. Empty => in-memory
	// driver (single process only).
	RedisURL string
	// ChannelPrefix is MARKET_STREAM_CHANNEL_PREFIX (default "market-stream").
	ChannelPrefix string
	// ChainID is MARKET_STREAM_CHAIN_ID (0 == unset == all chains).
	ChainID int
	// Symbols is the upper-cased MARKET_STREAM_SYMBOLS CSV. Empty means
	// auto-track every deployed market for ChainID (NestJS parity), NOT "no feed".
	Symbols []string
	// RefreshInterval is MARKET_STREAM_REFRESH_INTERVAL_MS (default 5s).
	RefreshInterval time.Duration
}

// ConfigFromEnv reads the MARKET_STREAM_* environment into a Config.
func ConfigFromEnv() Config {
	return Config{
		Enabled:         envBool("MARKET_STREAM_ENABLED", true),
		RunInAPI:        envBool("MARKET_STREAM_RUN_IN_API", true),
		RedisURL:        firstNonEmpty(os.Getenv("MARKET_STREAM_REDIS_URL"), os.Getenv("REDIS_URL")),
		ChannelPrefix:   strings.TrimSpace(os.Getenv("MARKET_STREAM_CHANNEL_PREFIX")),
		ChainID:         envInt("MARKET_STREAM_CHAIN_ID", 0),
		Symbols:         parseSymbols(os.Getenv("MARKET_STREAM_SYMBOLS")),
		RefreshInterval: refreshIntervalFromEnv(),
	}
}

// ChainFilter returns the chain filter pointer for the markets lister: nil when
// ChainID is 0 (all chains), else a pointer to the configured chain.
func (c Config) ChainFilter() *int {
	if c.ChainID == 0 {
		return nil
	}
	chain := c.ChainID
	return &chain
}

// ListMarketsFunc resolves the deployed (symbol, chainId) markets for a chain
// filter — the auto-track source. In production this wraps
// markets.Service.GetSymbols (see internal/marketstreamwire), mirroring NestJS
// MarketStreamSnapshotService.getTrackedMarkets → tradingService.getMarkets.
type ListMarketsFunc func(ctx context.Context, chainID *int) ([]TrackedMarket, error)

// BuildTrackedFunc builds the worker's TrackedFunc with NestJS-compatible
// semantics: an empty MARKET_STREAM_SYMBOLS auto-tracks *all* deployed markets
// for the configured chain; a non-empty list filters those markets to the
// configured symbols. This mirrors MarketStreamWorkerService.resolveTrackedMarkets:
//
//	const tracked = await getTrackedMarkets(chainId);
//	if (configuredSymbols.length === 0) return tracked;       // auto-track all
//	return tracked.filter(m => allowed.has(m.symbol));        // filtered
func (c Config) BuildTrackedFunc(list ListMarketsFunc) TrackedFunc {
	chainFilter := c.ChainFilter()
	allowed := make(map[string]struct{}, len(c.Symbols))
	for _, s := range c.Symbols {
		if up := strings.ToUpper(strings.TrimSpace(s)); up != "" {
			allowed[up] = struct{}{}
		}
	}
	return func(ctx context.Context) ([]TrackedMarket, error) {
		tracked, err := list(ctx, chainFilter)
		if err != nil {
			return nil, err
		}
		if len(allowed) == 0 {
			return tracked, nil // empty symbols => auto-track all (NestJS parity)
		}
		out := make([]TrackedMarket, 0, len(tracked))
		for _, m := range tracked {
			if _, ok := allowed[strings.ToUpper(strings.TrimSpace(m.Symbol))]; ok {
				out = append(out, m)
			}
		}
		return out, nil
	}
}

// BuildDriver builds the cross-process Redis driver when RedisURL is set, else
// the in-process memory driver. The bool reports whether the driver is shared
// across processes (redis) — the standalone worker warns when it is not, since
// fan-out to a separate API process then cannot happen. A redis URL that fails
// to parse falls back to memory and returns the error for the caller to log.
func (c Config) BuildDriver() (Driver, bool, error) {
	if c.RedisURL != "" {
		rd, err := NewRedisDriver(c.RedisURL, c.ChannelPrefix)
		if err != nil {
			return NewMemoryDriver(), false, err
		}
		return rd, true, nil
	}
	return NewMemoryDriver(), false, nil
}

func refreshIntervalFromEnv() time.Duration {
	if v := strings.TrimSpace(os.Getenv("MARKET_STREAM_REFRESH_INTERVAL_MS")); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms > 0 {
			return time.Duration(ms) * time.Millisecond
		}
	}
	return DefaultRefreshInterval
}

// parseSymbols splits a CSV into trimmed, upper-cased, non-empty symbols.
func parseSymbols(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.ToUpper(strings.TrimSpace(p)); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// envBool treats "false"/"0"/"no"/"off" as false, "true"/"1"/"yes"/"on" as
// true (case-insensitive), and anything else (incl. empty/unset) as def. This
// matches how the NestJS ecosystem config drives MARKET_STREAM_ENABLED /
// MARKET_STREAM_RUN_IN_API with explicit "true"/"false" strings.
func envBool(key string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "false", "0", "no", "off":
		return false
	case "true", "1", "yes", "on":
		return true
	default:
		return def
	}
}

func envInt(key string, def int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
