// Command indexer is the Go port of the NestJS indexer service.
// Phase 6a.1 wires the smallest viable binary: load config, start the
// worker shell, block on SIGINT/SIGTERM, then drain the worker. The
// real backfill/head-subscription/event-handler machinery arrives in
// 6a.2 → 6a.7.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"strings"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/db"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/deployments"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/eventbus"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/indexer"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/markets"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/rpc"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/runtimeenv"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	cfg, err := indexer.Load()
	if err != nil {
		if errors.Is(err, indexer.ErrMissingRPCURL) {
			slog.Error("indexer config invalid",
				"err", err,
				"hint", "set SEPOLIA_RPC_URL or SEPOLIA_HTTPS_RPC, or INDEXER_LOCAL=1 for anvil")
		} else {
			slog.Error("indexer config invalid", "err", err)
		}
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	w, cleanup := buildWorker(ctx, cfg)
	defer cleanup()
	if err := w.Start(ctx); err != nil {
		slog.Error("indexer worker failed to start", "err", err)
		os.Exit(1)
	}

	<-ctx.Done()
	slog.Info("indexer received shutdown signal, draining…")

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer stopCancel()
	if err := w.Stop(stopCtx); err != nil {
		slog.Error("indexer worker shutdown error", "err", err)
		os.Exit(1)
	}
	slog.Info("indexer worker stopped cleanly")
}

// buildWorker wires the worker. With a database configured it drives the
// backfiller — live indexing with persisted checkpoints (Phase 6a.5) — and a
// DBSink that upserts web3_events and projects tokens/trades/holders (6a.6).
// The sink is wired back to the worker as a CurveWatcher so a TokenCreated adds
// the new bonding curve to the live watch set. Without a database it runs the
// heartbeat so the binary still starts and reports RPC connectivity. The
// returned cleanup closes the pool.
func buildWorker(ctx context.Context, cfg indexer.Config) (*indexer.Worker, func()) {
	noop := func() {}

	databaseURL, err := runtimeenv.Resolve()
	if err != nil {
		slog.Warn("indexer: database env invalid; running heartbeat only", "err", err)
		return indexer.NewWorker(cfg), noop
	}
	if databaseURL == "" {
		slog.Warn("indexer: no database env; running heartbeat only (no indexing/checkpointing)",
			"hint", "set DATABASE_URL or DATABASE_HOST/PORT/NAME/USER/PASSWORD to index")
		return indexer.NewWorker(cfg), noop
	}
	pool, err := db.Open(ctx, databaseURL)
	if err != nil {
		slog.Error("indexer: database connect failed; running heartbeat only", "err", err)
		return indexer.NewWorker(cfg), noop
	}

	reader := rpc.NewClient(cfg.RPCURL, 0)
	checkpoints := indexer.NewCheckpointStore(pool)
	sink := indexer.NewDBSink(pool) // web3_events upsert + token/trade/holder projection
	// Production realtime producer: committed projections notify the API's
	// /token-events tier over Postgres (eventbus) — cross-process safe.
	sink.SetEventPublisher(eventbus.NewPgPublisher(pool))
	// Perp events carry index-token ADDRESSES; perp tables store market
	// SYMBOLS. The resolver comes from the same markets catalog ×
	// deployments registry the API serves.
	sink.SetSymbolResolver(buildSymbolResolver(cfg.DeploymentsDir))
	backfiller := indexer.NewBackfiller(cfg, reader, checkpoints, sink)
	// Watch set: launchpad factory + DEX router (existing) + the perp market
	// and staking pool so their lifecycle events are projected. Bonding
	// curves join at runtime via the sink's CurveWatcher.
	watch := []string{
		cfg.Contracts.Factory,
		cfg.Contracts.Router,
		cfg.Contracts.PerpMarket,
		cfg.Contracts.StakingPool,
	}
	// Add the ERC-20 mock assets + staking token so their Approval/Transfer
	// events land in web3_events (security-approvals + activity read models).
	watch = append(watch, cfg.Contracts.Tokens...)
	// Add AMM pairs: the registry's static pair plus every pair the factory
	// reports for the known-token combinations (the mWBTC/mWETH pair backing
	// the swap page is not in the registry file). Their Swap/Sync events are
	// what the activity feed reads for real DEX trades.
	watch = append(watch, cfg.Contracts.Pairs...)
	discovered := indexer.DiscoverDexPairs(ctx, reader, cfg.Contracts.DexFactory, cfg.Contracts.Tokens)
	if len(discovered) > 0 {
		slog.Info("indexer: discovered AMM pairs from factory", "count", len(discovered))
		watch = append(watch, discovered...)
	}

	worker := indexer.NewWorkerWithRunner(cfg, backfiller, watch)
	// Close the loop: the sink adds each new bonding curve to the worker's watch
	// set on TokenCreated, so the curve's Buy/Sell events are scanned next tick.
	sink.SetCurveWatcher(worker)

	// Seed the watch set with curves from earlier runs so a restart resumes
	// scanning them immediately (not just on the next TokenCreated).
	if n, err := indexer.SeedWatchset(ctx, worker, sink); err != nil {
		slog.Warn("indexer: failed to seed watch set from existing tokens", "err", err)
	} else if n > 0 {
		slog.Info("indexer: seeded watch set from existing bonding curves", "count", n)
	}

	slog.Info("indexer: live mode — backfiller driving the poll loop, persisting to web3_events",
		"factory", cfg.Contracts.Factory, "router", cfg.Contracts.Router,
		"perp_market", cfg.Contracts.PerpMarket, "staking_pool", cfg.Contracts.StakingPool)
	return worker, func() { pool.Close() }
}

// registrySymbolResolver maps (chainID, index-token address) → market symbol,
// built once at boot from markets.Definitions × the deployments registry.
type registrySymbolResolver struct {
	byChain map[int]map[string]string
}

func (r *registrySymbolResolver) SymbolForIndexToken(chainID int, indexToken string) string {
	if r == nil {
		return ""
	}
	return r.byChain[chainID][strings.ToLower(strings.TrimSpace(indexToken))]
}

func buildSymbolResolver(deploymentsDir string) *registrySymbolResolver {
	table := map[int]map[string]string{}
	registry, err := deployments.LoadMerged(deploymentsDir)
	if err != nil {
		slog.Warn("indexer: deployments registry unavailable; perp events will be raw-only", "err", err)
		return &registrySymbolResolver{byChain: table}
	}
	for _, def := range markets.Definitions(nil) {
		cfg := registry[def.ChainID]
		idx := deployments.LookupAddress(cfg, def.IndexContractNames...)
		if idx == "" {
			continue
		}
		if table[def.ChainID] == nil {
			table[def.ChainID] = map[string]string{}
		}
		table[def.ChainID][strings.ToLower(idx)] = def.Symbol
	}
	total := 0
	for _, m := range table {
		total += len(m)
	}
	slog.Info("indexer: perp symbol resolver ready", "mappings", total)
	return &registrySymbolResolver{byChain: table}
}
