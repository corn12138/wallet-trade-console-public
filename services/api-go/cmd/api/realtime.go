package main

import (
	"context"
	"log/slog"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/markets"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/marketstream"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/marketstreamwire"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/realtime"
)

// buildRealtime wires the strict-zero-Nest realtime tier (ADR 0006) for the API
// process. The Socket.IO gateways always subscribe to the market-stream driver
// (Redis cross-process when MARKET_STREAM_REDIS_URL/REDIS_URL is set, else
// in-process memory). Whether THIS process also computes & publishes snapshots
// depends on the worker/gateway split (services/api/ecosystem.config.cjs):
//
//   - MARKET_STREAM_ENABLED=false              -> no worker anywhere.
//   - MARKET_STREAM_RUN_IN_API=false (prod)    -> gateway-only; a separate
//     cmd/market-stream process publishes snapshots over Redis and this process
//     just relays them to subscribers.
//   - MARKET_STREAM_RUN_IN_API=true  (default) -> this process also runs the
//     worker (single-process / local convenience).
//
// Returns the server (to mount /socket.io/) and a stop func for graceful
// shutdown.
func buildRealtime(ctx context.Context, marketsSvc *markets.Service) (*realtime.Server, func()) {
	cfg := marketstream.ConfigFromEnv()

	driver, shared, err := cfg.BuildDriver()
	if err != nil {
		slog.Warn("realtime: redis market-stream driver init failed; using in-memory", "err", err)
	}
	if shared {
		// Connect eagerly so cross-process subscribe is validated/logged even
		// when this process runs no worker (gateway-only). Memory Connect is a
		// no-op; Redis Connect just pings (idempotent with worker.Start below).
		if cerr := driver.Connect(ctx); cerr != nil {
			slog.Warn("realtime: redis market-stream driver connect failed; fan-out degraded", "err", cerr)
		} else {
			slog.Info("realtime: redis market-stream driver (cross-process fan-out)")
		}
	} else {
		slog.Info("realtime: in-memory market-stream driver (single process)")
	}

	rt := realtime.NewServer(realtime.Options{
		// snapshot-on-subscribe: serve the latest cached snapshot to a fresh
		// subscriber (matches emitCachedSnapshotIfAvailable in NestJS).
		MarketSnapshot: func(symbol string, chainID int) *realtime.Snapshot {
			snap, err := driver.GetSnapshot(context.Background(), symbol, chainID)
			if err != nil || snap == nil {
				return nil
			}
			return toRealtimeSnapshot(snap)
		},
	})

	unsub, err := driver.Subscribe(func(s *marketstream.Snapshot) {
		rt.BroadcastMarket(toRealtimeSnapshot(s))
	})
	if err != nil {
		slog.Warn("realtime: driver subscribe failed", "err", err)
		unsub = func() {}
	}

	var worker *marketstream.Worker
	if marketsSvc != nil && cfg.Enabled && cfg.RunInAPI {
		tracked := cfg.BuildTrackedFunc(marketstreamwire.ListMarkets(marketsSvc))
		compute := marketstreamwire.Compute(marketsSvc)
		worker = marketstream.NewWorker(driver, tracked, compute, cfg.RefreshInterval)
		if err := worker.Start(ctx); err != nil {
			slog.Warn("realtime: in-API market-stream worker start failed", "err", err)
			worker = nil
		} else {
			slog.Info("realtime: in-API market-stream worker started",
				"intervalMs", cfg.RefreshInterval.Milliseconds(),
				"autoTrack", len(cfg.Symbols) == 0, "chainId", cfg.ChainID)
		}
	} else {
		slog.Info("realtime: gateway-only (no in-API worker)",
			"enabled", cfg.Enabled, "runInApi", cfg.RunInAPI,
			"note", "MARKET_STREAM_RUN_IN_API=false expects a separate cmd/market-stream process")
	}

	stop := func() {
		unsub()
		if worker != nil {
			_ = worker.Stop(context.Background())
		}
		_ = driver.Disconnect(context.Background())
	}
	return rt, stop
}

// toRealtimeSnapshot converts a market-stream snapshot into the realtime
// gateway's snapshot shape.
func toRealtimeSnapshot(s *marketstream.Snapshot) *realtime.Snapshot {
	if s == nil {
		return nil
	}
	return &realtime.Snapshot{
		Symbol:    s.Symbol,
		ChainID:   s.ChainID,
		Ticker:    s.Ticker,
		Bids:      s.Orderbook.Bids,
		Asks:      s.Orderbook.Asks,
		Trades:    s.Trades,
		UpdatedAt: s.UpdatedAt,
	}
}
