package indexer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoad_DefaultsToSepoliaRPCURL(t *testing.T) {
	resetIndexerEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.RPCURL != DefaultSepoliaRPCURL {
		t.Errorf("RPCURL = %q, want default Sepolia RPC", cfg.RPCURL)
	}
	if cfg.Contracts.Factory != ZeroAddress {
		t.Errorf("Factory = %q, want zero address fallback", cfg.Contracts.Factory)
	}
	if cfg.Contracts.Router != ZeroAddress {
		t.Errorf("Router = %q, want zero address fallback", cfg.Contracts.Router)
	}
}

func TestLoad_PicksSepoliaRPCURL(t *testing.T) {
	resetIndexerEnv(t)
	t.Setenv("SEPOLIA_RPC_URL", "https://sepolia.example/rpc")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.RPCURL != "https://sepolia.example/rpc" {
		t.Errorf("RPCURL = %q, want %q", cfg.RPCURL, "https://sepolia.example/rpc")
	}
	if cfg.ChainID != DefaultChainID {
		t.Errorf("ChainID = %d, want %d", cfg.ChainID, DefaultChainID)
	}
	if cfg.PollIntervalMs != DefaultPollIntervalMs {
		t.Errorf("PollIntervalMs = %d, want %d", cfg.PollIntervalMs, DefaultPollIntervalMs)
	}
	if cfg.BatchSize != DefaultBatchSize {
		t.Errorf("BatchSize = %d, want %d", cfg.BatchSize, DefaultBatchSize)
	}
}

func TestLoad_FallsBackToHttpsRPC(t *testing.T) {
	resetIndexerEnv(t)
	t.Setenv("SEPOLIA_HTTPS_RPC", "https://fallback.example/rpc")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.RPCURL != "https://fallback.example/rpc" {
		t.Errorf("RPCURL = %q, want fallback", cfg.RPCURL)
	}
}

func TestLoad_LocalMode(t *testing.T) {
	resetIndexerEnv(t)
	t.Setenv("INDEXER_LOCAL", "1")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ChainID != AnvilChainID {
		t.Errorf("ChainID = %d, want %d", cfg.ChainID, AnvilChainID)
	}
	if cfg.RPCURL != "http://127.0.0.1:8545" {
		t.Errorf("RPCURL = %q, want anvil default", cfg.RPCURL)
	}
	if cfg.PollIntervalMs != 1000 {
		t.Errorf("PollIntervalMs = %d, want 1000", cfg.PollIntervalMs)
	}
	if cfg.BatchSize != 100 {
		t.Errorf("BatchSize = %d, want 100", cfg.BatchSize)
	}
}

func TestLoad_LocalModeOverriddenByExplicitRPC(t *testing.T) {
	resetIndexerEnv(t)
	t.Setenv("INDEXER_LOCAL", "1")
	t.Setenv("SEPOLIA_RPC_URL", "https://prod.example/rpc")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.RPCURL != "https://prod.example/rpc" {
		t.Errorf("explicit RPC URL should override local default, got %q", cfg.RPCURL)
	}
	if cfg.ChainID != AnvilChainID {
		t.Errorf("ChainID = %d, INDEXER_LOCAL should still set anvil chain", cfg.ChainID)
	}
}

func TestLoad_OverrideKnobs(t *testing.T) {
	resetIndexerEnv(t)
	t.Setenv("SEPOLIA_RPC_URL", "https://sepolia.example/rpc")
	t.Setenv("INDEXER_POLL_INTERVAL_MS", "500")
	t.Setenv("INDEXER_BATCH_SIZE", "42")
	t.Setenv("INDEXER_CONFIRMATION_DEPTH", "7")
	t.Setenv("INDEXER_MAX_RECONNECT_ATTEMPTS", "3")
	t.Setenv("INDEXER_BACKFILL_MAX_RETRIES", "9")
	t.Setenv("INDEXER_DEPLOY_BLOCK", "12345678")
	t.Setenv("FACTORY_ADDRESS", "0xfactory")
	t.Setenv("ROUTER_ADDRESS", "0xrouter")
	t.Setenv("DATABASE_URL", "postgres://x")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.PollIntervalMs != 500 {
		t.Errorf("PollIntervalMs = %d, want 500", cfg.PollIntervalMs)
	}
	if cfg.BatchSize != 42 {
		t.Errorf("BatchSize = %d, want 42", cfg.BatchSize)
	}
	if cfg.ConfirmationDepth != 7 {
		t.Errorf("ConfirmationDepth = %d, want 7", cfg.ConfirmationDepth)
	}
	if cfg.MaxReconnectAttempts != 3 {
		t.Errorf("MaxReconnectAttempts = %d, want 3", cfg.MaxReconnectAttempts)
	}
	if cfg.BackfillMaxRetries != 9 {
		t.Errorf("BackfillMaxRetries = %d, want 9", cfg.BackfillMaxRetries)
	}
	if cfg.DeployBlock != 12345678 {
		t.Errorf("DeployBlock = %d, want 12345678", cfg.DeployBlock)
	}
	if cfg.Contracts.Factory != "0xfactory" {
		t.Errorf("Factory = %q, want 0xfactory", cfg.Contracts.Factory)
	}
	if cfg.Contracts.Router != "0xrouter" {
		t.Errorf("Router = %q, want 0xrouter", cfg.Contracts.Router)
	}
	if cfg.DatabaseURL != "postgres://x" {
		t.Errorf("DatabaseURL = %q, want postgres://x", cfg.DatabaseURL)
	}
}

func TestLoad_ContractsFallbackToDeploymentRegistry(t *testing.T) {
	resetIndexerEnv(t)
	dir := t.TempDir()
	t.Setenv("CONTRACTS_DEPLOYMENTS_DIR", dir)
	writeDeployment(t, dir, "sepolia.json", `{
		"chainId": 11155111,
		"contracts": {
			"TokenFactory": {"address": "0xTokenFactory"},
			"Router": "0xRouter"
		}
	}`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Contracts.Factory != "0xTokenFactory" {
		t.Errorf("Factory = %q, want registry TokenFactory", cfg.Contracts.Factory)
	}
	if cfg.Contracts.Router != "0xRouter" {
		t.Errorf("Router = %q, want registry Router", cfg.Contracts.Router)
	}
}

func TestLoad_IgnoresInvalidNumericOverrides(t *testing.T) {
	resetIndexerEnv(t)
	t.Setenv("SEPOLIA_RPC_URL", "https://sepolia.example/rpc")
	t.Setenv("INDEXER_POLL_INTERVAL_MS", "not-a-number")
	t.Setenv("INDEXER_BATCH_SIZE", "0")
	t.Setenv("INDEXER_CONFIRMATION_DEPTH", "-1")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PollIntervalMs != DefaultPollIntervalMs {
		t.Errorf("invalid poll interval should fall back to default")
	}
	if cfg.BatchSize != DefaultBatchSize {
		t.Errorf("zero batch should fall back to default")
	}
	if cfg.ConfirmationDepth != DefaultConfirmationDepth {
		t.Errorf("negative confirmation depth should fall back to default")
	}
}

func TestFirstNonEmpty(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{}, ""},
		{[]string{""}, ""},
		{[]string{"", "a"}, "a"},
		{[]string{"a", "b"}, "a"},
		{[]string{"", "", "c"}, "c"},
	}
	for i, tc := range cases {
		got := firstNonEmpty(tc.in...)
		if got != tc.want {
			t.Errorf("case %d: firstNonEmpty(%v) = %q, want %q", i, tc.in, got, tc.want)
		}
	}
}

// resetIndexerEnv clears every env var Load() touches so each test
// starts from a known state. t.Setenv restores them on teardown.
func resetIndexerEnv(t *testing.T) {
	t.Helper()
	keys := []string{
		"INDEXER_LOCAL",
		"SEPOLIA_RPC_URL",
		"SEPOLIA_HTTPS_RPC",
		"SEPOLIA_WSS_RPC",
		"INDEXER_POLL_INTERVAL_MS",
		"INDEXER_BATCH_SIZE",
		"INDEXER_CONFIRMATION_DEPTH",
		"INDEXER_MAX_RECONNECT_ATTEMPTS",
		"INDEXER_BACKFILL_MAX_RETRIES",
		"INDEXER_DEPLOY_BLOCK",
		"CONTRACTS_DEPLOYMENTS_DIR",
		"FACTORY_ADDRESS",
		"ROUTER_ADDRESS",
		"DATABASE_URL",
	}
	for _, k := range keys {
		t.Setenv(k, "")
	}
	t.Setenv("CONTRACTS_DEPLOYMENTS_DIR", t.TempDir())
}

func writeDeployment(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("write deployment: %v", err)
	}
}
