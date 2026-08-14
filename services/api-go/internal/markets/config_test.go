package markets

import "testing"

func TestDefinitions_NoFilter(t *testing.T) {
	got := Definitions(nil)
	if len(got) != len(SupportedChainIDs)*len(marketTemplates) {
		t.Fatalf("expected %d definitions, got %d", len(SupportedChainIDs)*len(marketTemplates), len(got))
	}

	// Order matters: outer = chainId, inner = template.
	want := []struct {
		symbol  string
		chainID int
	}{
		{"ETH-USD", 11155111},
		{"BTC-USD", 11155111},
		{"ETH-USD", 31337},
		{"BTC-USD", 31337},
	}
	for i, w := range want {
		if got[i].Symbol != w.symbol || got[i].ChainID != w.chainID {
			t.Errorf("def[%d] = {%s,%d}, want {%s,%d}", i, got[i].Symbol, got[i].ChainID, w.symbol, w.chainID)
		}
	}
}

func TestDefinitions_FilterMatchesOneChain(t *testing.T) {
	chain := 31337
	got := Definitions(&chain)
	if len(got) != len(marketTemplates) {
		t.Fatalf("expected %d definitions for chain %d, got %d", len(marketTemplates), chain, len(got))
	}
	for _, def := range got {
		if def.ChainID != chain {
			t.Errorf("filter leaked chain %d", def.ChainID)
		}
	}
}

func TestDefinitions_FilterUnknownChainYieldsEmpty(t *testing.T) {
	chain := 99999
	got := Definitions(&chain)
	if len(got) != 0 {
		t.Errorf("expected empty for unknown chain; got %d", len(got))
	}
}
