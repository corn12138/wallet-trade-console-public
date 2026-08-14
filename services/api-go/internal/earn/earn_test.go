package earn

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/deployments"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/staking"
)

type fakePools struct {
	rows []staking.PoolView
	err  error
}

func (f *fakePools) ListActivePools(_ context.Context, _ *int) ([]staking.PoolView, error) {
	return f.rows, f.err
}

type fakeLoader struct {
	configs map[int]deployments.ChainConfig
	err     error
}

func (f *fakeLoader) Load() (map[int]deployments.ChainConfig, error) {
	return f.configs, f.err
}

func ptrFloat(v float64) *float64 { return &v }
func ptrStr(v string) *string     { return &v }

func TestService_DegradesOnPoolUnavailable(t *testing.T) {
	svc := NewService(&fakePools{err: staking.ErrPoolUnavailable}, nil)
	got, err := svc.GetProducts(context.Background(), nil)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestService_NilPoolListerReturnsEmpty(t *testing.T) {
	svc := NewService(nil, nil)
	got, err := svc.GetProducts(context.Background(), nil)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestService_PropagatesNonDegradedError(t *testing.T) {
	wantErr := errors.New("boom")
	svc := NewService(&fakePools{err: wantErr}, nil)
	if _, err := svc.GetProducts(context.Background(), nil); !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want wrapped boom", err)
	}
}

func TestService_MapsPoolsToProducts(t *testing.T) {
	rows := []staking.PoolView{
		{
			ID:           "p1",
			Name:         "ATLAS Stake",
			PoolType:     "staking",
			TokenAddress: "0xtoken1",
			RewardToken:  ptrStr("0xreward1"),
			APY:          ptrFloat(42.6),
			TVL:          ptrFloat(8.4e6),
			ChainID:      11155111,
			Status:       "active",
		},
		{
			// nil APY/TVL/RewardToken: expect 0 / 0 / nil in output
			ID:           "p2",
			Name:         "USDC Stake",
			PoolType:     "staking",
			TokenAddress: "0xtoken2",
			ChainID:      11155111,
			Status:       "active",
		},
	}
	loader := &fakeLoader{
		configs: map[int]deployments.ChainConfig{
			11155111: {Contracts: map[string]deployments.ContractInfo{
				"StakingPool": {Address: "0xPool"},
			}},
		},
	}
	svc := NewService(&fakePools{rows: rows}, loader)
	got, err := svc.GetProducts(context.Background(), nil)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].APY != 42.6 || got[0].TVL != 8.4e6 {
		t.Errorf("p1 apy/tvl = %v/%v, want 42.6/8.4e6", got[0].APY, got[0].TVL)
	}
	if got[0].ContractAddress == nil || *got[0].ContractAddress != "0xPool" {
		t.Errorf("p1 contractAddress = %v, want 0xPool", got[0].ContractAddress)
	}
	if got[1].APY != 0 || got[1].TVL != 0 {
		t.Errorf("p2 apy/tvl = %v/%v, want 0/0", got[1].APY, got[1].TVL)
	}
	if got[1].RewardToken != nil {
		t.Errorf("p2 rewardToken = %v, want nil", got[1].RewardToken)
	}
}

func TestService_MissingChainAddressYieldsNullContract(t *testing.T) {
	// Pool is on chain 99 but the loader knows only 1. The output
	// row should have a null contractAddress — matches NestJS
	// getDeploymentAddress returning '' / undefined.
	rows := []staking.PoolView{
		{ID: "p1", Name: "X", PoolType: "staking", ChainID: 99, Status: "active"},
	}
	loader := &fakeLoader{
		configs: map[int]deployments.ChainConfig{
			1: {Contracts: map[string]deployments.ContractInfo{
				"StakingPool": {Address: "0xMainnetPool"},
			}},
		},
	}
	svc := NewService(&fakePools{rows: rows}, loader)
	got, _ := svc.GetProducts(context.Background(), nil)
	if got[0].ContractAddress != nil {
		t.Errorf("contractAddress = %v, want nil", got[0].ContractAddress)
	}
}

func TestService_LoaderErrorDegradesContractAddress(t *testing.T) {
	rows := []staking.PoolView{
		{ID: "p1", Name: "X", PoolType: "staking", ChainID: 1, Status: "active"},
	}
	svc := NewService(&fakePools{rows: rows}, &fakeLoader{err: errors.New("nope")})
	got, err := svc.GetProducts(context.Background(), nil)
	if err != nil {
		t.Fatalf("loader err must not propagate; got %v", err)
	}
	if got[0].ContractAddress != nil {
		t.Errorf("contractAddress = %v, want nil on load failure", got[0].ContractAddress)
	}
}

func TestHandler_ReturnsJSONArray(t *testing.T) {
	svc := NewService(&fakePools{rows: nil}, nil)
	mux := Router(svc, nil)
	req := httptest.NewRequest(http.MethodGet, "/products?chainId=11155111", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body []Product
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body) != 0 {
		t.Errorf("len = %d, want 0", len(body))
	}
}

// ---- build-tx tests ----

type fakeFinder struct {
	pool staking.PoolView
	err  error
}

func (f *fakeFinder) FindPool(_ context.Context, _ string) (staking.PoolView, error) {
	return f.pool, f.err
}

func TestParseAmount(t *testing.T) {
	cases := []struct {
		amount   string
		decimals int
		want     string
		wantErr  bool
	}{
		{"1", 18, "1000000000000000000", false},
		{"1.5", 18, "1500000000000000000", false},
		{"0.001", 18, "1000000000000000", false},
		// Truncates extra fractional digits beyond decimals.
		{"1.000000000000000001234", 18, "1000000000000000001", false},
		{"0", 6, "0", false},
		{"", 18, "", true},
		{"-1", 18, "", true},
		{"abc", 18, "", true},
		{"1.2.3", 18, "", true},
		{"1.abc", 18, "", true},
	}
	for _, tc := range cases {
		got, err := parseAmount(tc.amount, tc.decimals)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseAmount(%q, %d): want error", tc.amount, tc.decimals)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseAmount(%q, %d): %v", tc.amount, tc.decimals, err)
			continue
		}
		if got.String() != tc.want {
			t.Errorf("parseAmount(%q, %d) = %s, want %s", tc.amount, tc.decimals, got.String(), tc.want)
		}
	}
}

func TestBuildDepositTx_HappyPath(t *testing.T) {
	finder := &fakeFinder{pool: staking.PoolView{ID: "sp1", ChainID: 11155111}}
	loader := &fakeLoader{configs: map[int]deployments.ChainConfig{
		11155111: {Contracts: map[string]deployments.ContractInfo{"StakingPool": {Address: "0x000000000000000000000000000000000000DEAD"}}},
	}}
	svc := NewServiceWithBuilder(nil, finder, loader)
	got, err := svc.BuildDepositTx(context.Background(), BuildTxRequest{ProductID: "sp1", Amount: "5", TokenDecimals: 18})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got.ChainID != 11155111 {
		t.Errorf("chainId = %d, want 11155111", got.ChainID)
	}
	if got.To != "0x000000000000000000000000000000000000DEAD" {
		t.Errorf("to = %q", got.To)
	}
	if got.Value != "0" {
		t.Errorf("value = %q, want 0", got.Value)
	}
	// stake(uint256) selector = 0xa694fc3a; 5e18 = 5000000000000000000.
	// big.Int hex: 0x4563918244F40000 (16 hex chars), padded to 64 chars.
	wantData := "0xa694fc3a" + "00000000000000000000000000000000000000000000000" + "04563918244f40000"
	if got.Data != wantData {
		t.Errorf("data:\n got  %s\nwant %s", got.Data, wantData)
	}
}

func TestBuildWithdrawTx_UsesUnstakeSelector(t *testing.T) {
	finder := &fakeFinder{pool: staking.PoolView{ID: "sp1", ChainID: 11155111}}
	loader := &fakeLoader{configs: map[int]deployments.ChainConfig{
		11155111: {Contracts: map[string]deployments.ContractInfo{"StakingPool": {Address: "0x000000000000000000000000000000000000DEAD"}}},
	}}
	svc := NewServiceWithBuilder(nil, finder, loader)
	got, _ := svc.BuildWithdrawTx(context.Background(), BuildTxRequest{ProductID: "sp1", Amount: "1", TokenDecimals: 6})
	if !startsWith(got.Data, "0x2e17de78") { // unstake(uint256) selector
		t.Errorf("data = %s, want 0x2e17de78 prefix", got.Data)
	}
}

func TestBuildDepositTx_PoolNotFoundReturnsErrProductNotFound(t *testing.T) {
	finder := &fakeFinder{err: staking.ErrPoolNotFound}
	svc := NewServiceWithBuilder(nil, finder, &fakeLoader{})
	if _, err := svc.BuildDepositTx(context.Background(), BuildTxRequest{ProductID: "missing"}); !errors.Is(err, ErrProductNotFound) {
		t.Errorf("err = %v, want ErrProductNotFound", err)
	}
}

func TestBuildDepositTx_NilFinderReturnsErrProductNotFound(t *testing.T) {
	svc := NewService(nil, nil) // no finder wired
	if _, err := svc.BuildDepositTx(context.Background(), BuildTxRequest{ProductID: "any"}); !errors.Is(err, ErrProductNotFound) {
		t.Errorf("err = %v, want ErrProductNotFound", err)
	}
}

func TestBuildDepositTx_MissingDeploymentReturnsErrProductNotFound(t *testing.T) {
	finder := &fakeFinder{pool: staking.PoolView{ID: "sp1", ChainID: 99}}
	loader := &fakeLoader{configs: map[int]deployments.ChainConfig{
		1: {Contracts: map[string]deployments.ContractInfo{"StakingPool": {Address: "0xMain"}}},
	}}
	svc := NewServiceWithBuilder(nil, finder, loader)
	if _, err := svc.BuildDepositTx(context.Background(), BuildTxRequest{ProductID: "sp1", Amount: "1", TokenDecimals: 18}); !errors.Is(err, ErrProductNotFound) {
		t.Errorf("err = %v, want ErrProductNotFound", err)
	}
}

func TestBuildDepositTx_InvalidAmountReturns400(t *testing.T) {
	finder := &fakeFinder{pool: staking.PoolView{ID: "sp1", ChainID: 11155111}}
	loader := &fakeLoader{configs: map[int]deployments.ChainConfig{
		11155111: {Contracts: map[string]deployments.ContractInfo{"StakingPool": {Address: "0x000000000000000000000000000000000000DEAD"}}},
	}}
	svc := NewServiceWithBuilder(nil, finder, loader)
	if _, err := svc.BuildDepositTx(context.Background(), BuildTxRequest{ProductID: "sp1", Amount: "-1", TokenDecimals: 18}); !errors.Is(err, ErrInvalidAmount) {
		t.Errorf("err = %v, want ErrInvalidAmount", err)
	}
}

func TestHandler_BuildDepositReturnsJSON(t *testing.T) {
	finder := &fakeFinder{pool: staking.PoolView{ID: "sp1", ChainID: 11155111}}
	loader := &fakeLoader{configs: map[int]deployments.ChainConfig{
		11155111: {Contracts: map[string]deployments.ContractInfo{"StakingPool": {Address: "0x000000000000000000000000000000000000DEAD"}}},
	}}
	svc := NewServiceWithBuilder(nil, finder, loader)
	mux := Router(svc, nil)
	body, _ := json.Marshal(BuildTxRequest{ProductID: "sp1", Amount: "1", TokenDecimals: 18})
	req := httptest.NewRequest(http.MethodPost, "/build-deposit", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	var resp BuildTxResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ChainID != 11155111 {
		t.Errorf("chainId = %d", resp.ChainID)
	}
}

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func TestHandler_BadChainIDIgnored(t *testing.T) {
	svc := NewService(&fakePools{rows: nil}, nil)
	mux := Router(svc, nil)
	// Non-numeric chainId is dropped (matches NestJS parseNumberParam
	// returning undefined for unparseable input). Endpoint still 200s.
	req := httptest.NewRequest(http.MethodGet, "/products?chainId=banana", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

// ---- public/guarded split (2026-06-15) ----
//
// GET /products is Go-canonical PUBLIC; build-deposit/build-withdraw stay
// access-guarded. denyGuard stands in for the production accessGuard: it 401s
// everything it wraps, so these tests prove the guard reaches the POSTs but
// never the public GET.

func denyGuard(_ http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	})
}

func TestRouter_ProductsPublicEvenWhenBuildTxGuarded(t *testing.T) {
	rows := []staking.PoolView{
		{ID: "p1", Name: "ATLAS Stake", PoolType: "staking", TokenAddress: "0xtoken1",
			APY: ptrFloat(12.5), TVL: ptrFloat(1e6), ChainID: 11155111, Status: "active"},
	}
	svc := NewService(&fakePools{rows: rows}, nil)
	mux := Router(svc, denyGuard) // build-tx guard wired; products must stay public
	req := httptest.NewRequest(http.MethodGet, "/products?chainId=11155111", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /products with guard wired: status = %d, want 200 (must stay public); body=%q", rec.Code, rec.Body.String())
	}
	var body []Product
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body) != 1 || body[0].ID != "p1" || body[0].APY != 12.5 {
		t.Errorf("body = %+v, want one product p1 apy=12.5", body)
	}
}

func TestRouter_ProductsPublicEmptyArrayShape(t *testing.T) {
	// nil backing data → public 200 with a JSON array (`[]`), not 401/null —
	// matches existing Go degraded behavior and parseAtlasResponse expectations.
	svc := NewService(nil, nil)
	mux := Router(svc, denyGuard)
	req := httptest.NewRequest(http.MethodGet, "/products", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "[]\n" {
		t.Errorf("body = %q, want %q", got, "[]\n")
	}
}

func TestRouter_BuildTxStaysGuarded(t *testing.T) {
	finder := &fakeFinder{pool: staking.PoolView{ID: "sp1", ChainID: 11155111}}
	loader := &fakeLoader{configs: map[int]deployments.ChainConfig{
		11155111: {Contracts: map[string]deployments.ContractInfo{"StakingPool": {Address: "0x000000000000000000000000000000000000DEAD"}}},
	}}
	svc := NewServiceWithBuilder(nil, finder, loader)
	mux := Router(svc, denyGuard)
	for _, path := range []string{"/build-deposit", "/build-withdraw"} {
		body, _ := json.Marshal(BuildTxRequest{ProductID: "sp1", Amount: "1", TokenDecimals: 18})
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("POST %s with guard wired: status = %d, want 401 (must stay guarded); body=%q", path, rec.Code, rec.Body.String())
		}
	}
}
