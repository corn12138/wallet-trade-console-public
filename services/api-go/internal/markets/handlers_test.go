package markets

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/deployments"
)

func setupSymbolsRouter() http.Handler {
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
	return Router(NewService(loader, nil, nil))
}

func TestSymbolsHandler_200(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/symbols", nil)
	rr := httptest.NewRecorder()
	setupSymbolsRouter().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content-type = %q", ct)
	}

	var out []SymbolView
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode body: %v\n%s", err, rr.Body.String())
	}
	if len(out) != 4 {
		t.Errorf("expected 4 symbols, got %d", len(out))
	}
}

func TestSymbolsHandler_ChainIDFilter(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/symbols?chainId=31337", nil)
	rr := httptest.NewRecorder()
	setupSymbolsRouter().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var out []SymbolView
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 local markets, got %d: %+v", len(out), out)
	}
	for _, m := range out {
		if m.ChainID != 31337 {
			t.Errorf("chain leak: %+v", m)
		}
	}
}

func TestSymbolsHandler_NonNumericChainIDIgnored(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/symbols?chainId=undefined", nil)
	rr := httptest.NewRecorder()
	setupSymbolsRouter().ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var out []SymbolView
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 4 {
		t.Errorf("non-numeric chainId should be ignored (treated as no filter); got %d markets", len(out))
	}
}

func TestParseChainIDQuery(t *testing.T) {
	cases := []struct {
		in   string
		want *int
	}{
		{"", nil},
		{"undefined", nil},
		{"abc", nil},
		{"11155111", ptrInt(11155111)},
		{"31337", ptrInt(31337)},
		{"0", nil}, // matches JS falsy coercion of chainId=0 → "no filter"
	}
	for _, tc := range cases {
		got := parseChainIDQuery(tc.in)
		switch {
		case got == nil && tc.want == nil:
			// ok
		case got == nil || tc.want == nil:
			t.Errorf("parseChainIDQuery(%q) = %v, want %v", tc.in, got, tc.want)
		case *got != *tc.want:
			t.Errorf("parseChainIDQuery(%q) = %d, want %d", tc.in, *got, *tc.want)
		}
	}
}
