// Command bridgerelayer observes BridgeGateway events and delivers pending
// cross-chain transfers.
//
// It is the off-chain half of the bridge: it projects real on-chain events into
// bridge_transfers and, when a signer is configured, sends `fulfill` on the
// destination chain. It NEVER writes an optimistic status — a transfer becomes
// FULFILLED only when the BridgeFulfilled log is observed on a later scan.
//
// Env:
//   - DATABASE_URL / DATABASE_*        required (the projection is persisted)
//   - CONTRACTS_DEPLOYMENTS_DIR        gateway discovery
//   - SEPOLIA_RPC_URL | SEPOLIA_HTTPS_RPC   chain 11155111
//   - BRIDGE_RPC_URL_<chainId>         any other chain (the extension seam)
//   - BRIDGE_RELAYER_PRIVATE_KEY       optional; without it the relayer is
//     OBSERVE-ONLY: it keeps status current but delivers nothing
//   - BRIDGE_RELAYER_INTERVAL_MS       default 15000
//   - BRIDGE_CONFIRMATIONS             default 3
//   - BRIDGE_START_BLOCK_<chainId>     first block to scan on a fresh store
//
// Flags:
//
//	-once  run a single scan+deliver cycle and exit (used by the e2e proof).
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/bridge"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/db"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/deployments"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/ethtx"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/markets"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/rpc"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/runtimeenv"
)

func main() {
	once := flag.Bool("once", false, "run a single scan+deliver cycle, then exit")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	databaseURL, err := runtimeenv.Resolve()
	if err != nil {
		slog.Error("bridgerelayer: no database configured", "err", err)
		os.Exit(1)
	}
	pool, err := db.Open(ctx, databaseURL)
	if err != nil {
		slog.Error("bridgerelayer: database connect failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	chains := loadChains()
	if len(chains) == 0 {
		slog.Error("bridgerelayer: no deployments registry found", "hint", "set CONTRACTS_DEPLOYMENTS_DIR")
		os.Exit(1)
	}

	clients := buildClients()
	callers := map[int]bridge.EthCaller{}
	chainClients := map[int]bridge.ChainClient{}
	for chainID, c := range clients {
		callers[chainID] = c
		chainClients[chainID] = c
	}

	registry := bridge.NewRegistry(chains, callers)
	if len(registry.Chains()) == 0 {
		slog.Error("bridgerelayer: no chain has a bridgeGateway deployment; nothing to relay")
		os.Exit(1)
	}

	var signer *ethtx.Signer
	if key := strings.TrimSpace(os.Getenv("BRIDGE_RELAYER_PRIVATE_KEY")); key != "" {
		signer, err = ethtx.NewSigner(key)
		if err != nil {
			slog.Error("bridgerelayer: invalid relayer key", "err", err)
			os.Exit(1)
		}
		slog.Info("bridgerelayer: delivery enabled", "relayer", signer.Address())
	} else {
		slog.Warn("bridgerelayer: OBSERVE-ONLY (BRIDGE_RELAYER_PRIVATE_KEY unset) — status stays current, nothing is delivered")
	}

	cfg := bridge.DefaultRelayerConfig()
	cfg.ConfirmationDepth = uint64(envInt("BRIDGE_CONFIRMATIONS", int(cfg.ConfirmationDepth)))
	// Raise this on a chain whose base fee moves fast enough to reject a fulfil
	// bid at the last observed price.
	cfg.GasPricePadPercent = uint64(envInt("BRIDGE_GAS_PRICE_PAD_PERCENT", int(cfg.GasPricePadPercent)))
	cfg.StartBlocks = loadStartBlocks()

	relayer := bridge.NewRelayer(registry, bridge.NewStore(pool), signer, chainClients, cfg, slog.Default())

	if *once {
		if err := relayer.RunOnce(ctx); err != nil {
			slog.Error("bridgerelayer: one-shot cycle failed", "err", err)
			os.Exit(1)
		}
		slog.Info("bridgerelayer: one-shot cycle complete")
		return
	}

	interval := time.Duration(envInt("BRIDGE_RELAYER_INTERVAL_MS", 15_000)) * time.Millisecond
	if err := relayer.Run(ctx, interval); err != nil && ctx.Err() == nil {
		slog.Error("bridgerelayer: exited with error", "err", err)
		os.Exit(1)
	}
	slog.Info("bridgerelayer: stopped cleanly")
}

func loadChains() map[int]deployments.ChainConfig {
	loader := markets.NewDirLoader()
	if loader.Dir() == "" {
		return nil
	}
	chains, err := loader.Load()
	if err != nil {
		slog.Error("bridgerelayer: deployments load failed", "err", err)
		return nil
	}
	return chains
}

// buildClients mirrors cmd/api's seam: Sepolia from the shared env var, every
// other chain from BRIDGE_RPC_URL_<chainId>.
func buildClients() map[int]*rpc.Client {
	out := map[int]*rpc.Client{}
	if url := firstNonEmpty(os.Getenv("SEPOLIA_RPC_URL"), os.Getenv("SEPOLIA_HTTPS_RPC")); url != "" {
		out[11155111] = rpc.NewClient(url, 0)
	}
	for _, entry := range os.Environ() {
		key, value, found := strings.Cut(entry, "=")
		if !found || !strings.HasPrefix(key, "BRIDGE_RPC_URL_") {
			continue
		}
		chainID, err := strconv.Atoi(strings.TrimPrefix(key, "BRIDGE_RPC_URL_"))
		if err != nil || chainID <= 0 {
			continue
		}
		if url := strings.TrimSpace(value); url != "" {
			out[chainID] = rpc.NewClient(url, 0)
			slog.Info("bridgerelayer: chain RPC configured", "chainId", chainID, "rpc_url", rpc.RedactURL(url))
		}
	}
	return out
}

func loadStartBlocks() map[int]uint64 {
	out := map[int]uint64{}
	for _, entry := range os.Environ() {
		key, value, found := strings.Cut(entry, "=")
		if !found || !strings.HasPrefix(key, "BRIDGE_START_BLOCK_") {
			continue
		}
		chainID, err := strconv.Atoi(strings.TrimPrefix(key, "BRIDGE_START_BLOCK_"))
		if err != nil || chainID <= 0 {
			continue
		}
		block, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
		if err != nil {
			continue
		}
		out[chainID] = block
	}
	return out
}

func envInt(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return fallback
	}
	return n
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
