package trading

import "testing"

func TestNormalizeSymbol(t *testing.T) {
	cases := map[string]string{
		"eth-usd":     "ETH-USD",
		"  btc-usd  ": "BTC-USD",
		"ALREADY":     "ALREADY",
		"mIxEd CaSe":  "MIXED CASE",
		"":            "",
	}
	for in, want := range cases {
		if got := NormalizeSymbol(in); got != want {
			t.Errorf("NormalizeSymbol(%q) = %q, want %q", in, got, want)
		}
	}
}

func sp(s string) *string { return &s }

func TestDeriveMidPrice(t *testing.T) {
	if got := DeriveMidPrice(nil, sp("10")); got != nil {
		t.Errorf("nil bid → want nil; got %v", *got)
	}
	if got := DeriveMidPrice(sp("10"), nil); got != nil {
		t.Errorf("nil ask → want nil; got %v", *got)
	}
	if got := DeriveMidPrice(sp("100"), sp("200")); got == nil || *got != "150" {
		t.Errorf("expected 150; got %v", got)
	}
	if got := DeriveMidPrice(sp("not"), sp("200")); got != nil {
		t.Errorf("malformed bid → want nil; got %v", *got)
	}
}

func TestDeriveSpread(t *testing.T) {
	if got := DeriveSpread(nil, sp("10")); got != nil {
		t.Errorf("nil bid → want nil; got %v", *got)
	}
	if got := DeriveSpread(sp("100"), sp("150")); got == nil || *got != "50" {
		t.Errorf("expected 50; got %v", got)
	}
}
