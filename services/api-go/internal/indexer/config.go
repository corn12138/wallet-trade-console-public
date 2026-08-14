// Package indexer is the Go port of legacy NestJS indexer/.
// Phase 6a.1 scaffolds the worker's configuration loading + the
// minimal Worker shell. Subsequent slices (6a.2 → 6a.7) add the
// checkpoint store, log parser, backfill loop, head subscription,
// event handlers, and reconnect/backoff machinery.
package indexer

import (
	"errors"
	"os"
	"strconv"
	"strings"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/deployments"
)

// DefaultChainID matches DEFAULT_INDEXER_CONFIG.chainId in the NestJS
// indexer (Sepolia).
const DefaultChainID = 11155111

// AnvilChainID matches ANVIL_INDEXER_CONFIG.chainId (local anvil).
const AnvilChainID = 31337

// DefaultPollIntervalMs matches DEFAULT_INDEXER_CONFIG.pollInterval.
const DefaultPollIntervalMs = 12_000

// DefaultBatchSize matches DEFAULT_INDEXER_CONFIG.batchSize.
const DefaultBatchSize uint64 = 1000

// DefaultSepoliaRPCURL matches DEFAULT_INDEXER_CONFIG.rpcUrl's public
// fallback in the NestJS indexer.
const DefaultSepoliaRPCURL = "https://rpc.sepolia.org"

// ZeroAddress matches resolveIndexerAddress()'s empty-registry fallback
// in legacy NestJS indexer/indexer.config.ts.
const ZeroAddress = "0x0000000000000000000000000000000000000000"

// Defaults for reconnect / backfill retry budgets — match the NestJS
// constants (INDEXER_MAX_RECONNECT_ATTEMPTS, INDEXER_BACKFILL_MAX_RETRIES).
const (
	DefaultMaxReconnectAttempts = 12
	DefaultBackfillMaxRetries   = 5
	// DefaultConfirmationDepth matches the NestJS fallback:
	// `Number.parseInt(process.env.INDEXER_CONFIRMATION_DEPTH ?? '6', 10)`.
	DefaultConfirmationDepth = 6
)

// Contracts holds the subset of deployment addresses the indexer
// tracks. Factory + Router for the FE workflows; PerpMarket + StakingPool
// for the perp/staking projections; Tokens + Pairs so ERC-20
// Approval/Transfer and AMM Swap/Mint/Burn/Sync events land in
// web3_events (the security-approvals and activity read models).
// The rest of the chain config tree is loaded lazily via the
// deployments registry.
type Contracts struct {
	Factory     string
	Router      string
	PerpMarket  string // "" when the chain has no perp deployment
	StakingPool string // "" when the chain has no staking deployment
	// DexFactory is the AMM pair factory (registry key "factory") used to
	// discover trading-pair addresses at startup; "" disables discovery.
	DexFactory string
	// Tokens are the registry's known ERC-20s (mock assets + staking token).
	Tokens []string
	// Pairs are registry-listed AMM pair contracts.
	Pairs []string
}

// Config is the indexer worker's runtime configuration. Populated by
// Load() from env vars + the deployments registry.
type Config struct {
	ChainID              int
	RPCURL               string
	WSSURL               string
	PollIntervalMs       int
	BatchSize            uint64
	ConfirmationDepth    int
	MaxReconnectAttempts int
	BackfillMaxRetries   int
	// DeployBlock is the lowest block the backfill loop will read from
	// when no checkpoint exists yet. Mirrors IndexerConfig.deployBlock
	// in the NestJS service (default 0n; override via INDEXER_DEPLOY_BLOCK).
	DeployBlock uint64
	Contracts   Contracts
	DatabaseURL string
	// DeploymentsDir is the resolved deployments-registry directory, exposed
	// so cmd/indexer can build the perp symbol resolver from the same source.
	DeploymentsDir string
}

// Load reads the indexer config from env vars. Mirrors
// DEFAULT_INDEXER_CONFIG / ANVIL_INDEXER_CONFIG: SEPOLIA_RPC_URL +
// SEPOLIA_HTTPS_RPC + SEPOLIA_WSS_RPC name parity, the public Sepolia
// fallback RPC URL, and deployment-registry address fallback.
func Load() (Config, error) {
	cfg := Config{
		ChainID:              DefaultChainID,
		PollIntervalMs:       DefaultPollIntervalMs,
		BatchSize:            DefaultBatchSize,
		ConfirmationDepth:    DefaultConfirmationDepth,
		MaxReconnectAttempts: DefaultMaxReconnectAttempts,
		BackfillMaxRetries:   DefaultBackfillMaxRetries,
	}

	if strings.ToLower(os.Getenv("INDEXER_LOCAL")) == "1" {
		cfg.ChainID = AnvilChainID
		cfg.RPCURL = "http://127.0.0.1:8545"
		cfg.PollIntervalMs = 1000
		cfg.BatchSize = 100
	}

	if v := firstNonEmpty(os.Getenv("SEPOLIA_RPC_URL"), os.Getenv("SEPOLIA_HTTPS_RPC")); v != "" {
		cfg.RPCURL = v
	}
	if cfg.RPCURL == "" {
		cfg.RPCURL = DefaultSepoliaRPCURL
	}
	if v := os.Getenv("SEPOLIA_WSS_RPC"); v != "" {
		cfg.WSSURL = v
	}
	if cfg.RPCURL == "" {
		return Config{}, ErrMissingRPCURL
	}

	if v := os.Getenv("INDEXER_POLL_INTERVAL_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.PollIntervalMs = n
		}
	}
	if v := os.Getenv("INDEXER_BATCH_SIZE"); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil && n > 0 {
			cfg.BatchSize = n
		}
	}
	if v := os.Getenv("INDEXER_CONFIRMATION_DEPTH"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			cfg.ConfirmationDepth = n
		}
	}
	if v := os.Getenv("INDEXER_MAX_RECONNECT_ATTEMPTS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.MaxReconnectAttempts = n
		}
	}
	if v := os.Getenv("INDEXER_BACKFILL_MAX_RETRIES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.BackfillMaxRetries = n
		}
	}
	if v := os.Getenv("INDEXER_DEPLOY_BLOCK"); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil {
			cfg.DeployBlock = n
		}
	}

	cfg.Contracts = resolveContracts(cfg.ChainID)
	cfg.DatabaseURL = os.Getenv("DATABASE_URL")
	cfg.DeploymentsDir = defaultDeploymentsDir()

	return cfg, nil
}

// ErrMissingRPCURL is kept as the sentinel for invalid RPC config.
// The default Sepolia URL means production Load() normally does not hit it.
var ErrMissingRPCURL = errors.New("indexer: SEPOLIA_RPC_URL / SEPOLIA_HTTPS_RPC must be set")

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func resolveContracts(chainID int) Contracts {
	registry, _ := deployments.LoadMerged(defaultDeploymentsDir())
	chain := registry[chainID]
	return Contracts{
		Factory: firstNonEmpty(
			os.Getenv("FACTORY_ADDRESS"),
			deployments.LookupAddress(chain, "TokenFactory", "factory"),
			ZeroAddress,
		),
		Router: firstNonEmpty(
			os.Getenv("ROUTER_ADDRESS"),
			deployments.LookupAddress(chain, "Router", "router"),
			ZeroAddress,
		),
		// No ZeroAddress fallback here: an unresolved perp/staking contract
		// yields "" so the worker's nonEmpty() drops it from the watch set
		// instead of scanning address zero every tick.
		PerpMarket: firstNonEmpty(
			os.Getenv("PERP_MARKET_ADDRESS"),
			deployments.LookupAddress(chain, "PerpMarket"),
		),
		StakingPool: firstNonEmpty(
			os.Getenv("STAKING_POOL_ADDRESS"),
			deployments.LookupAddress(chain, "StakingPool"),
		),
		// The AMM pair factory. Note the alias precedence is the REVERSE of
		// Factory above: "factory" (DEX) first, so on chains that deploy both
		// the launchpad TokenFactory and the pair factory each field gets its
		// own contract.
		DexFactory: firstNonEmpty(
			os.Getenv("DEX_FACTORY_ADDRESS"),
			deployments.LookupAddress(chain, "factory"),
		),
		Tokens: dedupAddresses(
			deployments.LookupAddress(chain, "MockUSDC", "tokenA"),
			deployments.LookupAddress(chain, "MockWETH", "weth", "tokenB"),
			deployments.LookupAddress(chain, "MockWBTC"),
			deployments.LookupAddress(chain, "StakingToken"),
		),
		Pairs: dedupAddresses(
			deployments.LookupAddress(chain, "pair"),
		),
	}
}

// dedupAddresses drops empties, the zero address, and case-insensitive
// duplicates while preserving order. Registry aliases (weth == tokenB on
// Sepolia) would otherwise double-scan the same contract every tick.
func dedupAddresses(addrs ...string) []string {
	seen := make(map[string]struct{}, len(addrs))
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		if a == "" || strings.EqualFold(a, ZeroAddress) {
			continue
		}
		key := strings.ToLower(a)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, a)
	}
	return out
}

// defaultDeploymentsDir delegates to the shared resolver so api /
// indexer / market-stream all discover the registry the same way.
func defaultDeploymentsDir() string {
	return deployments.DiscoverDir()
}
