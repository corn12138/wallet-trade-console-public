package portfolio

import (
	"strings"
	"testing"
)

func TestBuildHiddenAssetCandidateKeys_OrderAndShape(t *testing.T) {
	addr := "0xABCDEF1234567890ABCDEF1234567890ABCDEF12"
	wallet := "0xABCDEF1234567890ABCDEF1234567890ABCDEF12"
	keys := buildHiddenAssetCandidateKeys(11155111, &addr, "HONEY", &wallet)
	want := []string{
		// scope=all-wallets × {compound (asset address), addressOnly, symbolOnly}
		"11155111:all-wallets:0xabcdef1234567890abcdef1234567890abcdef12",
		"11155111:all-wallets:symbol:HONEY",
		// scope=walletAddress × same identities
		"11155111:0xabcdef1234567890abcdef1234567890abcdef12:0xabcdef1234567890abcdef1234567890abcdef12",
		"11155111:0xabcdef1234567890abcdef1234567890abcdef12:symbol:HONEY",
	}
	// Note: when assetAddress is set, the "compound" identity equals the
	// "addressOnly" identity (the symbol is dropped), so the LinkedHashSet
	// de-dupes — first entry stays.
	if len(keys) != len(want) {
		t.Fatalf("got %d keys, want %d: %v", len(keys), len(want), keys)
	}
	for i, k := range want {
		if keys[i] != k {
			t.Errorf("[%d] = %q, want %q", i, keys[i], k)
		}
	}
}

func TestBuildHiddenAssetCandidateKeys_SymbolOnlyAndCaseFolding(t *testing.T) {
	keys := buildHiddenAssetCandidateKeys(1, nil, "honey", nil)
	// scope=all-wallets only; identity=symbol:HONEY (uppercased)
	if len(keys) != 1 {
		t.Fatalf("got %d keys, want 1: %v", len(keys), keys)
	}
	if keys[0] != "1:all-wallets:symbol:HONEY" {
		t.Errorf("got %q", keys[0])
	}
}

func TestBuildHiddenAssetCandidateKeys_BlankSymbolMapsToUnknown(t *testing.T) {
	keys := buildHiddenAssetCandidateKeys(1, nil, "   ", nil)
	if keys[0] != "1:all-wallets:symbol:UNKNOWN" {
		t.Errorf("got %q, want UNKNOWN identity", keys[0])
	}
}

func TestNormalizeSpamFilterLevel(t *testing.T) {
	cases := map[string]string{
		"":         "standard",
		"standard": "standard",
		"relaxed":  "relaxed",
		"strict":   "strict",
		"BANANA":   "standard",
	}
	for in, want := range cases {
		if got := normalizeSpamFilterLevel(in); got != want {
			t.Errorf("normalizeSpamFilterLevel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestShouldFilterAsSpam_NonDiscoveredAlwaysPasses(t *testing.T) {
	a := RawAsset{
		Symbol:   "AIRDROP",
		Name:     "Free Claim",
		Metadata: &RawAssetMeta{Origin: "created-by-owner"},
	}
	if shouldFilterAsSpam(a, "strict") {
		t.Errorf("created-by-owner must not be spam-filtered even with suspicious labels")
	}
}

func TestShouldFilterAsSpam_RelaxedOnlyLabelTags(t *testing.T) {
	// Wallet-discovered with two signals: suspicious-label + thin-market.
	// In relaxed mode only suspicious-label / spam-tag count → filter.
	a := RawAsset{
		Symbol: "AIRDROP",
		Name:   "Free reward",
		Metadata: &RawAssetMeta{
			Origin:     "wallet-discovered",
			IsOfficial: false,
		},
	}
	if !shouldFilterAsSpam(a, "relaxed") {
		t.Errorf("relaxed must drop on suspicious-label")
	}
	// Pure thin-market only → relaxed should NOT drop.
	b := RawAsset{
		Symbol:   "OBSCURE",
		Name:     "Some Token",
		Status:   "launched",
		ValueUSD: 0,
		Metadata: &RawAssetMeta{Origin: "wallet-discovered"},
	}
	if shouldFilterAsSpam(b, "relaxed") {
		t.Errorf("relaxed must NOT drop on thin-market alone")
	}
}

func TestShouldFilterAsSpam_StandardDropsAllNonThin(t *testing.T) {
	// Wallet-discovered, unverified-status (not active/launched), not
	// official: signal=unverified-status. Standard drops because at
	// least one signal is non-thin-market.
	a := RawAsset{
		Symbol:   "TOKEN",
		Name:     "Token",
		Status:   "burning",
		Metadata: &RawAssetMeta{Origin: "wallet-discovered"},
	}
	if !shouldFilterAsSpam(a, "standard") {
		t.Errorf("standard must drop on unverified-status")
	}
}

func TestShouldFilterAsSpam_StrictDropsEverythingWithSignal(t *testing.T) {
	// Wallet-discovered with only the thin-market signal — strict drops.
	a := RawAsset{
		Symbol:   "ZERO",
		Name:     "Zero",
		Status:   "active",
		ValueUSD: 0,
		Metadata: &RawAssetMeta{Origin: "wallet-discovered"},
	}
	if !shouldFilterAsSpam(a, "strict") {
		t.Errorf("strict must drop on any signal")
	}
}

func TestDetectSpamSignals_SuspiciousAndTags(t *testing.T) {
	a := RawAsset{
		Symbol: "AIRDROP-OK",
		Name:   "Free Token",
		Status: "active",
		Metadata: &RawAssetMeta{
			Origin: "wallet-discovered",
			Tags:   []string{"verified", "spam"},
		},
		Address: ptrStr("0xabc"),
	}
	signals := detectSpamSignals(a)
	got := strings.Join(sortStrings(signals), ",")
	if !strings.Contains(got, "suspicious-label") || !strings.Contains(got, "spam-tag") {
		t.Errorf("expected suspicious-label + spam-tag in %q", got)
	}
}

func TestApplyVisibility_HiddenMatchRemoves(t *testing.T) {
	addr := "0xabc"
	wallet := "0xowner"
	in := []RawAsset{{
		ID:            "x",
		AssetType:     "token",
		Symbol:        "X",
		Name:          "X",
		ChainID:       1,
		Status:        "active",
		Address:       &addr,
		WalletAddress: &wallet,
		UpdatedAt:     "2026-05-22T00:00:00Z",
	}}
	got := applyVisibility(in, []string{"1:all-wallets:0xabc"}, "standard")
	if got.FilterSummary.HiddenMatchedCount != 1 || len(got.Items) != 0 {
		t.Errorf("got %+v, want hidden=1 visible=0", got)
	}
}

func TestApplyVisibility_SortByValueDescThenUpdated(t *testing.T) {
	wallet := "0xowner"
	in := []RawAsset{
		{ID: "a", Symbol: "A", ChainID: 1, Status: "active", ValueUSD: 100, UpdatedAt: "2026-01-01T00:00:00Z", WalletAddress: &wallet},
		{ID: "b", Symbol: "B", ChainID: 1, Status: "active", ValueUSD: 200, UpdatedAt: "2025-01-01T00:00:00Z", WalletAddress: &wallet},
		{ID: "c", Symbol: "C", ChainID: 1, Status: "active", ValueUSD: 100, UpdatedAt: "2027-01-01T00:00:00Z", WalletAddress: &wallet},
	}
	got := applyVisibility(in, nil, "standard")
	if len(got.Items) != 3 {
		t.Fatalf("len = %d, want 3", len(got.Items))
	}
	// Expected: b (200), c (100, 2027), a (100, 2026)
	wantIDs := []string{"b", "c", "a"}
	for i, want := range wantIDs {
		if got.Items[i].ID != want {
			t.Errorf("sorted[%d] = %q, want %q", i, got.Items[i].ID, want)
		}
	}
}

func sortStrings(in []string) []string {
	out := append([]string{}, in...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}
