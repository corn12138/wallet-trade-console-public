// Command market-stream is the standalone Go market-stream worker — the strict
// zero-Nest replacement for the NestJS `wallet-trade-market-stream` PM2 process
// (services/api/ecosystem.config.cjs → market-stream.standalone.ts).
//
// It computes a DB-backed snapshot per tracked market on an interval and
// publishes it through the market-stream driver (Redis for cross-process
// fan-out), which the API process's Socket.IO gateways relay to subscribers.
// It runs no HTTP server. In production it runs with MARKET_STREAM_RUN_IN_API=false
// so the API process is gateway-only and this process is the sole snapshot
// producer.
//
// Env (shared with cmd/api via internal/marketstream.Config):
//   - MARKET_STREAM_ENABLED (default true) — false exits cleanly without work.
//   - MARKET_STREAM_REDIS_URL ?? REDIS_URL — cross-process fan-out (warns if unset).
//   - MARKET_STREAM_CHANNEL_PREFIX (default "market-stream").
//   - MARKET_STREAM_CHAIN_ID — chain to track (0 = all).
//   - MARKET_STREAM_SYMBOLS — CSV; EMPTY auto-tracks all deployed markets (NestJS parity).
//   - MARKET_STREAM_REFRESH_INTERVAL_MS (default 5000).
//   - DATABASE_* / DATABASE_URL — required (snapshots are DB-backed).
//   - CONTRACTS_DEPLOYMENTS_DIR — deployed-market registry (else nothing to track).
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/db"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/markets"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/marketstream"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/marketstreamwire"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/runtimeenv"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/token"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/trading"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg := marketstream.ConfigFromEnv()
	if !cfg.Enabled {
		slog.Info("market-stream: MARKET_STREAM_ENABLED=false; nothing to do, exiting cleanly")
		return
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Snapshots are DB-backed (trading projections), so a database is required —
	// unlike the in-API gateway, a standalone worker with no DB has nothing to
	// produce. Fail clearly instead of silently emitting empty snapshots.
	databaseURL, err := runtimeenv.Resolve()
	if err != nil {
		slog.Error("market-stream: database env invalid", "err", err)
		os.Exit(1)
	}
	if databaseURL == "" {
		slog.Error("market-stream: no database configured",
			"hint", "set DATABASE_URL or DATABASE_HOST/PORT/NAME/USER/PASSWORD; the worker computes DB-backed snapshots")
		os.Exit(1)
	}
	pool, err := db.Open(ctx, databaseURL)
	if err != nil {
		slog.Error("market-stream: database connect failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	// Markets read service — deployed-market registry (auto-track source) plus
	// the trading repo (DB-backed snapshot compute).
	loader := markets.NewDirLoader()
	if loader.Dir() == "" {
		slog.Warn("market-stream: CONTRACTS_DEPLOYMENTS_DIR not set; no markets to track")
	}
	marketsSvc := markets.NewService(loader, trading.NewRepository(pool), token.NewRepository(pool))

	driver, shared, derr := cfg.BuildDriver()
	if derr != nil {
		slog.Warn("market-stream: redis driver init failed; using in-memory", "err", derr)
	}
	if !shared {
		// A standalone worker on the memory driver cannot fan out to a separate
		// API process — mirrors the NestJS standalone's sharedAcrossProcesses warn.
		slog.Warn("market-stream: in-memory driver — no cross-process fan-out",
			"hint", "set MARKET_STREAM_REDIS_URL or REDIS_URL so the API gateway process receives snapshots")
	}

	tracked := cfg.BuildTrackedFunc(marketstreamwire.ListMarkets(marketsSvc))
	compute := marketstreamwire.Compute(marketsSvc)
	worker := marketstream.NewWorker(driver, tracked, compute, cfg.RefreshInterval)

	if err := worker.Start(ctx); err != nil {
		slog.Error("market-stream: worker failed to start", "err", err)
		os.Exit(1)
	}
	slog.Info("market-stream worker started",
		"intervalMs", cfg.RefreshInterval.Milliseconds(),
		"autoTrack", len(cfg.Symbols) == 0,
		"symbols", cfg.Symbols,
		"chainId", cfg.ChainID,
		"crossProcess", shared)

	<-ctx.Done()
	slog.Info("market-stream received shutdown signal, draining…")

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer stopCancel()
	if err := worker.Stop(stopCtx); err != nil {
		slog.Error("market-stream: worker shutdown error", "err", err)
		os.Exit(1)
	}
	slog.Info("market-stream worker stopped cleanly")
}
