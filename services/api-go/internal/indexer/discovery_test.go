package indexer

import (
	"context"
	"encoding/hex"
	"strings"
	"testing"
)

// fakeGetPairCaller answers getPair(a,b) from a static map keyed by the
// lowercased unordered token pair. Any other call returns the zero address.
type fakeGetPairCaller struct {
	pairs map[string]string // "tokenA|tokenB" (sorted, lowercased) -> pair addr
	calls int
}

func (f *fakeGetPairCaller) EthCall(_ context.Context, to, dataHex, _ string) ([]byte, error) {
	f.calls++
	// getPair(address,address): 4-byte selector + two 32-byte addresses.
	raw, _ := hex.DecodeString(strings.TrimPrefix(dataHex, "0x"))
	if len(raw) < 4+64 {
		return make([]byte, 32), nil
	}
	a := "0x" + hex.EncodeToString(raw[4+12:4+32])
	b := "0x" + hex.EncodeToString(raw[4+32+12:4+64])
	key := pairKey(a, b)
	pair, ok := f.pairs[key]
	if !ok {
		return make([]byte, 32), nil // zero address
	}
	out := make([]byte, 32)
	addr, _ := hex.DecodeString(strings.TrimPrefix(pair, "0x"))
	copy(out[12:], addr)
	return out, nil
}

func pairKey(a, b string) string {
	a, b = strings.ToLower(a), strings.ToLower(b)
	if a > b {
		a, b = b, a
	}
	return a + "|" + b
}

func TestDiscoverDexPairs_ReturnsNonZeroPairs(t *testing.T) {
	usdc := "0x0000000000000000000000000000000000000a11"
	weth := "0x0000000000000000000000000000000000000b22"
	wbtc := "0x0000000000000000000000000000000000000c33"
	pairUSDCWETH := "0x00000000000000000000000000000000000fFFF1"
	pairWBTCWETH := "0x00000000000000000000000000000000000fFFF2"

	caller := &fakeGetPairCaller{pairs: map[string]string{
		pairKey(usdc, weth): pairUSDCWETH,
		pairKey(wbtc, weth): pairWBTCWETH,
		// usdc/wbtc intentionally has no pool -> zero address, skipped.
	}}

	got := DiscoverDexPairs(context.Background(), caller, "0xFactory", []string{usdc, weth, wbtc})
	if len(got) != 2 {
		t.Fatalf("expected 2 discovered pairs, got %d (%v)", len(got), got)
	}
	found := map[string]bool{}
	for _, p := range got {
		found[strings.ToLower(p)] = true
	}
	if !found[strings.ToLower(pairUSDCWETH)] || !found[strings.ToLower(pairWBTCWETH)] {
		t.Errorf("missing expected pairs; got %v", got)
	}
	// 3 tokens -> C(3,2) = 3 combinations probed.
	if caller.calls != 3 {
		t.Errorf("expected 3 getPair calls, got %d", caller.calls)
	}
}

func TestDiscoverDexPairs_NoFactoryOrTokens(t *testing.T) {
	caller := &fakeGetPairCaller{pairs: map[string]string{}}
	if got := DiscoverDexPairs(context.Background(), caller, "", []string{"0xa", "0xb"}); got != nil {
		t.Errorf("no factory should return nil, got %v", got)
	}
	if got := DiscoverDexPairs(context.Background(), caller, "0xFactory", []string{"0xa"}); got != nil {
		t.Errorf("single token should return nil, got %v", got)
	}
	if got := DiscoverDexPairs(context.Background(), nil, "0xFactory", []string{"0xa", "0xb"}); got != nil {
		t.Errorf("nil caller should return nil, got %v", got)
	}
}

func TestDedupAddresses(t *testing.T) {
	got := dedupAddresses(
		"0xAbC0000000000000000000000000000000000001",
		"0xabc0000000000000000000000000000000000001", // case dup
		"",
		ZeroAddress,
		"0x0000000000000000000000000000000000000002",
	)
	if len(got) != 2 {
		t.Fatalf("expected 2 unique non-zero addresses, got %d (%v)", len(got), got)
	}
	if got[0] != "0xAbC0000000000000000000000000000000000001" {
		t.Errorf("first address should preserve original casing/order, got %q", got[0])
	}
}
