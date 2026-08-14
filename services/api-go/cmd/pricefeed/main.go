// Command pricefeed is the standalone real-price ingest worker.
//
// It exists because every candle surface in this product used to be a
// projection of our own on-chain activity, which on a testnet is near-zero —
// so /trade rendered an empty chart and no screen had a real price. This
// worker fills two tables with genuinely real data:
//
//	market_candles            ← dense OHLCV from a public spot venue (Gate.io)
//	oracle_price_observations ← Chainlink aggregator rounds over JSON-RPC
//
// It runs no HTTP server; cmd/api serves the stored rows at /api/prices.
// Nothing is synthesized: an unreachable upstream logs a warning and retries,
// it never writes a placeholder.
//
// Env (see internal/pricefeed.ConfigFromEnv for the full list):
//   - PRICEFEED_ENABLED=true            — required, master switch
//   - PRICEFEED_SYMBOLS                 — default "ETH-USD,BTC-USD"
//   - PRICEFEED_MARKET_INTERVAL_MS      — default 60000
//   - PRICEFEED_ORACLE_INTERVAL_MS      — default 120000
//   - PRICEFEED_ORACLE_RPC_URL / SEPOLIA_HTTPS_RPC / RPC_URL
//   - DATABASE_URL or DATABASE_* — required (ingestion is persisted)
//
// Flags:
//
//	-once   run a single ingest cycle for each enabled source, then exit.
//	        Used by the backfill step and by CI smoke checks.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/db"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/pricefeed"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/rpc"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/runtimeenv"
)

func main() {
	once := flag.Bool("once", false, "run a single ingest cycle per source, then exit")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg := pricefeed.ConfigFromEnv()
	// -once is an explicit operator action, so it implies the master switch:
	// a backfill should not silently do nothing because an env var is unset.
	if *once {
		cfg.Enabled = true
	}
	if !cfg.Enabled {
		slog.Info("pricefeed: PRICEFEED_ENABLED is not true; nothing to do, exiting cleanly")
		return
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	databaseURL, err := runtimeenv.Resolve()
	if err != nil {
		slog.Error("pricefeed: no database configured", "err", err)
		os.Exit(1)
	}
	pool, err := db.Open(ctx, databaseURL)
	if err != nil {
		slog.Error("pricefeed: database connect failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	store := pricefeed.NewStore(pool)

	var provider pricefeed.MarketProvider
	if cfg.MarketEnabled {
		provider = pricefeed.NewGateioProvider(cfg.MarketBaseURL, nil, 20*time.Second)
	}

	var oracle *pricefeed.ChainlinkReader
	if cfg.OracleEnabled {
		if cfg.OracleRPCURL == "" {
			slog.Warn("pricefeed: oracle enabled but no RPC url; oracle ingestion disabled",
				"hint", "set PRICEFEED_ORACLE_RPC_URL or SEPOLIA_HTTPS_RPC")
		} else {
			oracle = pricefeed.NewChainlinkReader(rpc.NewClient(cfg.OracleRPCURL, 15*time.Second))
		}
	}

	worker := pricefeed.NewWorker(cfg, store, provider, oracle, slog.Default())

	if *once {
		runOnce(ctx, cfg, worker, provider, oracle)
		return
	}

	if err := worker.Run(ctx); err != nil && ctx.Err() == nil {
		slog.Error("pricefeed: worker exited with error", "err", err)
		os.Exit(1)
	}
	slog.Info("pricefeed: stopped cleanly")
}

// runOnce drives exactly one cycle per enabled source. It exits non-zero when
// every enabled source failed, so a backfill step in a deploy fails loudly
// instead of leaving an empty chart behind.
func runOnce(ctx context.Context, cfg pricefeed.Config, worker *pricefeed.Worker, provider pricefeed.MarketProvider, oracle *pricefeed.ChainlinkReader) {
	attempted, failed := 0, 0

	if cfg.MarketEnabled && provider != nil {
		attempted++
		if err := worker.IngestMarketOnce(ctx); err != nil {
			failed++
			slog.Error("pricefeed: one-shot market ingest failed", "err", err)
		} else {
			slog.Info("pricefeed: one-shot market ingest complete")
		}
	}
	if cfg.OracleEnabled && oracle != nil && len(cfg.Feeds) > 0 {
		attempted++
		if err := worker.IngestOracleOnce(ctx); err != nil {
			failed++
			slog.Error("pricefeed: one-shot oracle ingest failed", "err", err)
		} else {
			slog.Info("pricefeed: one-shot oracle ingest complete")
		}
	}

	if attempted == 0 {
		slog.Error("pricefeed: no source configured for -once")
		os.Exit(1)
	}
	if failed == attempted {
		slog.Error("pricefeed: every configured source failed", "attempted", attempted)
		os.Exit(1)
	}
}
