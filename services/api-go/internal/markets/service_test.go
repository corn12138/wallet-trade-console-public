package markets

import (
	"errors"
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

func ptrInt(v int) *int       { return &v }
func ptrStr(v string) *string { return &v }

func TestGetSymbols_FullCatalog(t *testing.T) {
	loader := fakeLoader{registry: map[int]deployments.ChainConfig{
		11155111: {Contracts: map[string]deployments.ContractInfo{
			"MockWETH": {Address: "0xWETH-sep"},
			"MockWBTC": {Address: "0xWBTC-sep"},
			"MockUSDC": {Address: "0xUSDC-sep"},
		}},
		31337: {Contracts: map[string]deployments.ContractInfo{
			"weth": {Address: "0xWETH-local"},
			"wbtc": {Address: "0xWBTC-local"},
			"usdc": {Address: "0xUSDC-local"},
		}},
	}}

	got, err := NewService(loader, nil, nil).GetSymbols(nil)
	if err != nil {
		t.Fatalf("GetSymbols: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("expected 4 markets across both chains, got %d: %+v", len(got), got)
	}

	expect := []SymbolView{
		{Symbol: "ETH-USD", ChainID: 11155111, BaseAsset: "ETH", QuoteAsset: "USD", IndexToken: "0xWETH-sep", CollateralToken: "0xUSDC-sep", FundingRate: "0"},
		{Symbol: "BTC-USD", ChainID: 11155111, BaseAsset: "BTC", QuoteAsset: "USD", IndexToken: "0xWBTC-sep", CollateralToken: "0xUSDC-sep", FundingRate: "0"},
		{Symbol: "ETH-USD", ChainID: 31337, BaseAsset: "ETH", QuoteAsset: "USD", IndexToken: "0xWETH-local", CollateralToken: "0xUSDC-local", FundingRate: "0"},
		{Symbol: "BTC-USD", ChainID: 31337, BaseAsset: "BTC", QuoteAsset: "USD", IndexToken: "0xWBTC-local", CollateralToken: "0xUSDC-local", FundingRate: "0"},
	}
	for i, want := range expect {
		if got[i] != want {
			t.Errorf("market[%d] = %+v, want %+v", i, got[i], want)
		}
	}
}

func TestGetSymbols_ChainFilter(t *testing.T) {
	loader := fakeLoader{registry: map[int]deployments.ChainConfig{
		11155111: {Contracts: map[string]deployments.ContractInfo{
			"MockWETH": {Address: "0xWETH"},
			"MockWBTC": {Address: "0xWBTC"},
			"MockUSDC": {Address: "0xUSDC"},
		}},
		31337: {Contracts: map[string]deployments.ContractInfo{
			"weth": {Address: "0xLW"},
			"wbtc": {Address: "0xLB"},
			"usdc": {Address: "0xLU"},
		}},
	}}

	got, err := NewService(loader, nil, nil).GetSymbols(ptrInt(11155111))
	if err != nil {
		t.Fatalf("GetSymbols: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 markets on sepolia only, got %d: %+v", len(got), got)
	}
	for _, m := range got {
		if m.ChainID != 11155111 {
			t.Errorf("chainId filter leaked %d", m.ChainID)
		}
	}
}

func TestGetSymbols_DropsUndeployedMarkets(t *testing.T) {
	// Only ETH on local; BTC has no addresses → must be skipped.
	loader := fakeLoader{registry: map[int]deployments.ChainConfig{
		31337: {Contracts: map[string]deployments.ContractInfo{
			"weth": {Address: "0xW"},
			"usdc": {Address: "0xU"},
		}},
	}}

	got, err := NewService(loader, nil, nil).GetSymbols(ptrInt(31337))
	if err != nil {
		t.Fatalf("GetSymbols: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected just ETH-USD on local; got %d: %+v", len(got), got)
	}
	if got[0].Symbol != "ETH-USD" || got[0].IndexToken != "0xW" || got[0].CollateralToken != "0xU" {
		t.Errorf("unexpected market: %+v", got[0])
	}
}

func TestGetSymbols_EmptyRegistryYieldsEmpty(t *testing.T) {
	got, err := NewService(fakeLoader{registry: map[int]deployments.ChainConfig{}}, nil, nil).GetSymbols(nil)
	if err != nil {
		t.Fatalf("GetSymbols: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty result; got %+v", got)
	}
}

func TestGetSymbols_LoaderErrorPropagates(t *testing.T) {
	sentinel := errors.New("boom")
	_, err := NewService(fakeLoader{err: sentinel}, nil, nil).GetSymbols(nil)
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error, got %v", err)
	}
}

func TestSplitSymbol(t *testing.T) {
	tests := []struct {
		in, base, quote string
	}{
		{"ETH-USD", "ETH", "USD"},
		{"BTC-USD", "BTC", "USD"},
		{"NODASH", "NODASH", ""},
		{"-LEAD", "", "LEAD"},
		{"TRAIL-", "TRAIL", ""},
	}
	for _, tc := range tests {
		b, q := splitSymbol(tc.in)
		if b != tc.base || q != tc.quote {
			t.Errorf("splitSymbol(%q) = (%q, %q), want (%q, %q)", tc.in, b, q, tc.base, tc.quote)
		}
	}
}
