package deployments

import (
	"os"
	"path/filepath"
	"testing"
)

func writeJSON(t *testing.T, dir, name, body string) string {
	t.Helper()
	full := filepath.Join(dir, name)
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
	return full
}

func TestLoadMerged_EmptyDirReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	merged, err := LoadMerged(dir)
	if err != nil {
		t.Fatalf("LoadMerged: %v", err)
	}
	if len(merged) != 0 {
		t.Fatalf("expected empty merged map, got %d entries", len(merged))
	}
}

func TestLoadMerged_MissingDirReturnsEmpty(t *testing.T) {
	merged, err := LoadMerged(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("LoadMerged: %v", err)
	}
	if len(merged) != 0 {
		t.Fatalf("expected empty merged map, got %d entries", len(merged))
	}
}

func TestLoadMerged_BlankDirReturnsEmpty(t *testing.T) {
	merged, err := LoadMerged("")
	if err != nil {
		t.Fatalf("LoadMerged: %v", err)
	}
	if len(merged) != 0 {
		t.Fatalf("expected empty merged map, got %d entries", len(merged))
	}
}

func TestLoadMerged_AcceptsStringAndObjectContracts(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, dir, "a.json", `{
		"network": "sepolia",
		"chainId": 11155111,
		"timestamp": "2026-01-01T00:00:00Z",
		"contracts": {
			"router": "0xRouter",
			"MockWETH": {"address": "0xWeth", "decimals": 18},
			"MockUSDC": {"address": "0xUsdc", "decimals": 6}
		}
	}`)

	merged, err := LoadMerged(dir)
	if err != nil {
		t.Fatalf("LoadMerged: %v", err)
	}
	cfg, ok := merged[11155111]
	if !ok {
		t.Fatalf("expected entry for chainId 11155111, got %v", merged)
	}
	if got := cfg.Contracts["router"].Address; got != "0xRouter" {
		t.Errorf("router address = %q, want %q", got, "0xRouter")
	}
	if got := cfg.Contracts["MockWETH"].Address; got != "0xWeth" {
		t.Errorf("MockWETH address = %q, want %q", got, "0xWeth")
	}
	if got, ok := cfg.Contracts["MockWETH"].Extra["decimals"]; !ok || got.(float64) != 18 {
		t.Errorf("MockWETH decimals lost: %v", cfg.Contracts["MockWETH"].Extra)
	}
}

func TestLoadMerged_LaterTimestampWinsOnConflict(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, dir, "early.json", `{
		"chainId": 31337,
		"timestamp": "2026-01-01T00:00:00Z",
		"contracts": {"weth": "0xOldWeth", "factory": "0xFactory"}
	}`)
	writeJSON(t, dir, "later.json", `{
		"chainId": 31337,
		"timestamp": "2026-02-01T00:00:00Z",
		"contracts": {"weth": "0xNewWeth"}
	}`)

	merged, err := LoadMerged(dir)
	if err != nil {
		t.Fatalf("LoadMerged: %v", err)
	}
	cfg := merged[31337]
	if got := cfg.Contracts["weth"].Address; got != "0xNewWeth" {
		t.Errorf("weth should be overwritten by later file; got %q", got)
	}
	if got := cfg.Contracts["factory"].Address; got != "0xFactory" {
		t.Errorf("factory should be inherited from earlier file; got %q", got)
	}
	if len(cfg.Sources) != 2 || cfg.Sources[0] != "early.json" || cfg.Sources[1] != "later.json" {
		t.Errorf("sources order wrong: %v", cfg.Sources)
	}
}

func TestLoadMerged_SkipsInvalidFiles(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, dir, "bad-json.json", `{not valid`)
	writeJSON(t, dir, "no-chain.json", `{"network": "x"}`)
	writeJSON(t, dir, "zero-chain.json", `{"chainId": 0}`)
	writeJSON(t, dir, "good.json", `{"chainId": 42, "contracts": {"r": "0xR"}}`)
	if err := os.WriteFile(filepath.Join(dir, "not-json.txt"), []byte("ignored"), 0o644); err != nil {
		t.Fatal(err)
	}

	merged, err := LoadMerged(dir)
	if err != nil {
		t.Fatalf("LoadMerged: %v", err)
	}
	if len(merged) != 1 {
		t.Fatalf("expected 1 chain entry, got %d: %v", len(merged), merged)
	}
	if got := merged[42].Contracts["r"].Address; got != "0xR" {
		t.Errorf("good file dropped: %v", merged[42])
	}
}

func TestLookupAddress_FirstMatchWins(t *testing.T) {
	cfg := ChainConfig{Contracts: map[string]ContractInfo{
		"MockWETH": {Address: "0xMockWeth"},
		"weth":     {Address: "0xWeth"},
	}}
	if got := LookupAddress(cfg, "MockWETH", "weth"); got != "0xMockWeth" {
		t.Errorf("first alias should win; got %q", got)
	}
	if got := LookupAddress(cfg, "weth", "MockWETH"); got != "0xWeth" {
		t.Errorf("first alias should win; got %q", got)
	}
}

func TestLookupAddress_FallsThroughEmpty(t *testing.T) {
	cfg := ChainConfig{Contracts: map[string]ContractInfo{
		"absent":  {Address: ""},
		"present": {Address: "0xP"},
	}}
	if got := LookupAddress(cfg, "absent", "missing", "present"); got != "0xP" {
		t.Errorf("expected fallthrough to present; got %q", got)
	}
}

func TestLookupAddress_NilContractsSafe(t *testing.T) {
	if got := LookupAddress(ChainConfig{}, "anything"); got != "" {
		t.Errorf("nil contracts map should return empty; got %q", got)
	}
}

func TestDiscoverDir_EnvWins(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CONTRACTS_DEPLOYMENTS_DIR", dir)
	if got := DiscoverDir(); got != dir {
		t.Errorf("env should win; got %q want %q", got, dir)
	}
}

func TestDiscoverDir_FindsRepoLayoutFromNestedCwd(t *testing.T) {
	t.Setenv("CONTRACTS_DEPLOYMENTS_DIR", "")
	root := t.TempDir()
	target := filepath.Join(root, "packages", "shared", "deployments")
	nested := filepath.Join(root, "services", "api-go", "cmd", "api")
	for _, d := range []string{target, nested} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(nested)
	got := DiscoverDir()
	// Resolve symlinks: macOS TempDir lives under /var -> /private/var.
	wantResolved, _ := filepath.EvalSymlinks(target)
	gotResolved, _ := filepath.EvalSymlinks(got)
	if gotResolved != wantResolved {
		t.Errorf("expected ancestor discovery; got %q want %q", got, target)
	}
}

func TestDiscoverDir_EmptyWhenNoRepoLayout(t *testing.T) {
	t.Setenv("CONTRACTS_DEPLOYMENTS_DIR", "")
	t.Chdir(t.TempDir())
	if got := DiscoverDir(); got != "" {
		t.Errorf("expected empty outside a repo checkout; got %q", got)
	}
}
