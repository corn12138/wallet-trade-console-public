package contractconfig

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/deployments"
)

type fakeLoader struct {
	registry map[int]deployments.ChainConfig
	err      error
}

func (f fakeLoader) Load() (map[int]deployments.ChainConfig, error) {
	return f.registry, f.err
}

func TestLoadABIs_EmptyDirReturnsEmpty(t *testing.T) {
	out, err := LoadABIs("")
	if err != nil {
		t.Fatalf("LoadABIs(\"\") err = %v", err)
	}
	if len(out) != 0 {
		t.Errorf("out = %+v, want empty", out)
	}
}

func TestLoadABIs_NonExistentReturnsEmpty(t *testing.T) {
	out, err := LoadABIs(filepath.Join(t.TempDir(), "missing"))
	if err != nil {
		t.Fatalf("LoadABIs(missing) err = %v", err)
	}
	if len(out) != 0 {
		t.Errorf("out = %+v, want empty", out)
	}
}

func TestLoadABIs_ParsesArtifact(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "Foo.sol")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	artifact := `{"contractName":"Foo","sourceName":"src/Foo.sol","abi":[{"type":"function","name":"x"}]}`
	if err := os.WriteFile(filepath.Join(subdir, "Foo.json"), []byte(artifact), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := LoadABIs(dir)
	if err != nil {
		t.Fatalf("LoadABIs err = %v", err)
	}
	if _, ok := out["Foo"]; !ok {
		t.Errorf("Foo missing from %+v", out)
	}
}

func TestLoadABIs_SkipsBuildInfo(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "build-info")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	huge := `{"contractName":"BuildInfo","abi":[]}`
	if err := os.WriteFile(filepath.Join(subdir, "output.json"), []byte(huge), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := LoadABIs(dir)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if _, ok := out["BuildInfo"]; ok {
		t.Errorf("build-info output.json should have been skipped")
	}
}

func TestService_GetConfig_WithChains(t *testing.T) {
	loader := fakeLoader{registry: map[int]deployments.ChainConfig{
		11155111: {Network: "sepolia", Contracts: map[string]deployments.ContractInfo{
			"X": {Address: "0xAAA"},
		}},
	}}
	svc := NewService(loader, "")
	cfg, err := svc.GetConfig()
	if err != nil {
		t.Fatalf("GetConfig err = %v", err)
	}
	if cfg.DefaultChainID != DefaultChainID {
		t.Errorf("defaultChainId = %d, want %d", cfg.DefaultChainID, DefaultChainID)
	}
	if _, ok := cfg.Chains["11155111"]; !ok {
		t.Errorf("chain 11155111 missing from %+v", cfg.Chains)
	}
}

// TestGetConfig_FEContract_AddressesIndependentOfABISource locks the ABI-source
// decision recorded in docs/migration/backend-go/cutover/contracts.md: the
// frontend consumes ONLY config.chains (deployed addresses) from
// GET /contracts/config — it never reads config.abis (the useContractConfig
// getAbi helper has zero call sites, and /contracts/abi/:name has zero FE
// callers; ABIs are bundled into the FE from @wallet-trade/shared + viem). So the
// Go(foundry-artifact) vs NestJS(bundled-TS) ABI-source divergence is functionally
// irrelevant. This pins that the FE-consumed addresses come from the deployments
// loader regardless of the ABI source (here: empty abisDir → empty abis map, as in
// prod when contracts/artifacts is not shipped), so cutting = /api/contracts/config
// to Go is FE-safe with no Go behavior change.
func TestGetConfig_FEContract_AddressesIndependentOfABISource(t *testing.T) {
	loader := fakeLoader{registry: map[int]deployments.ChainConfig{
		11155111: {Network: "sepolia", Contracts: map[string]deployments.ContractInfo{
			"TokenFactory": {Address: "0xFACADE"},
		}},
	}}
	svc := NewService(loader, "") // empty abisDir → abis map is empty
	cfg, err := svc.GetConfig()
	if err != nil {
		t.Fatalf("GetConfig err = %v", err)
	}
	// The FE-consumed contract: chains[chainId].contracts[name].address.
	chain, ok := cfg.Chains["11155111"]
	if !ok {
		t.Fatalf("chain 11155111 missing from %+v", cfg.Chains)
	}
	if got := chain.Contracts["TokenFactory"].Address; got != "0xFACADE" {
		t.Errorf("TokenFactory address = %q, want 0xFACADE (the FE-consumed value)", got)
	}
	// abis is the non-FE-consumed field. It must stay a non-nil (possibly empty)
	// map so the response serializes as `"abis":{}` and the FE's optional
	// `config?.abis?.[name]` access stays shape-stable.
	if cfg.ABIs == nil {
		t.Errorf("abis must be a non-nil map (FE shape stability), got nil")
	}
	if len(cfg.ABIs) != 0 {
		t.Errorf("abis = %v, want empty when abisDir is empty", cfg.ABIs)
	}
}

func TestService_GetConfigByChain_FallbackEmpty(t *testing.T) {
	svc := NewService(fakeLoader{registry: map[int]deployments.ChainConfig{}}, "")
	resp, err := svc.GetConfigByChain(31337)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if resp.ChainID != 31337 {
		t.Errorf("ChainID = %d, want 31337", resp.ChainID)
	}
	if len(resp.Contracts) != 0 {
		t.Errorf("contracts = %v, want empty", resp.Contracts)
	}
}

func TestService_GetABI_NotFound(t *testing.T) {
	svc := NewService(fakeLoader{}, "")
	if _, err := svc.GetABI("Missing"); !errors.Is(err, ErrABINotFound) {
		t.Errorf("err = %v, want ErrABINotFound", err)
	}
}

func TestHandler_ConfigOK(t *testing.T) {
	mux := Router(NewService(fakeLoader{registry: map[int]deployments.ChainConfig{}}, ""))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/config", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body ConfigResponse
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if body.DefaultChainID != DefaultChainID {
		t.Errorf("defaultChainId = %d", body.DefaultChainID)
	}
}

func TestHandler_ConfigByChain_400OnBad(t *testing.T) {
	mux := Router(NewService(fakeLoader{}, ""))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/config/abc", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestHandler_ABI_404OnMissing(t *testing.T) {
	mux := Router(NewService(fakeLoader{}, ""))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/abi/MissingThing", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestHandler_ABI_200WhenLoaded(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "Bar.sol")
	_ = os.MkdirAll(subdir, 0o755)
	_ = os.WriteFile(filepath.Join(subdir, "Bar.json"), []byte(`{"contractName":"Bar","abi":[{"type":"function"}]}`), 0o644)

	mux := Router(NewService(fakeLoader{}, dir))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/abi/Bar", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
}
