package portfolio

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
)

func TestValuation_SubtotalOverflowDegradesRowsAndCountsConsistently(t *testing.T) {
	got := applyVisibility([]RawAsset{
		{ID: "b", ValueUSD: ptrFloat(math.MaxFloat64), ValuationStatus: ValuationKnown},
		{ID: "a", ValueUSD: ptrFloat(math.MaxFloat64), ValuationStatus: ValuationKnown},
	}, nil, "standard")
	if got.Valuation.State != "unavailable" || got.Valuation.KnownSubtotalUSD != nil || got.Valuation.ValuedCount != 0 || got.Valuation.UnpricedCount != 2 {
		t.Fatalf("valuation = %+v", got.Valuation)
	}
	for _, item := range got.Items {
		if item.ValueUSD != nil || item.ValuationStatus != ValuationUnavailable || item.ValuationReason == nil {
			t.Fatalf("item = %+v", item)
		}
	}
	if got.Items[0].ID != "a" {
		t.Fatal("degraded rows must use stable unknown ordering")
	}
	if _, err := json.Marshal(got); err != nil {
		t.Fatal(err)
	}
}

func TestValuation_EmptyUnknownMixedAndKnownZero(t *testing.T) {
	for _, test := range []struct {
		name             string
		rows             []RawAsset
		state            string
		valued, unpriced int
		subtotal         *float64
	}{
		{"empty", nil, "empty", 0, 0, nil},
		{"unknown", []RawAsset{{ID: "u"}}, "unavailable", 0, 1, nil},
		{"zero", []RawAsset{{ID: "z", ValueUSD: ptrFloat(0), ValuationStatus: ValuationKnown}}, "complete", 1, 0, ptrFloat(0)},
		{"mixed", []RawAsset{{ID: "u"}, {ID: "z", ValueUSD: ptrFloat(0), ValuationStatus: ValuationKnown}, {ID: "k", ValueUSD: ptrFloat(25), ValuationStatus: ValuationKnown}}, "partial", 2, 1, ptrFloat(25)},
		{"complete", []RawAsset{{ID: "a", ValueUSD: ptrFloat(10), ValuationStatus: ValuationKnown}, {ID: "b", ValueUSD: ptrFloat(20), ValuationStatus: ValuationKnown}}, "complete", 2, 0, ptrFloat(30)},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := applyVisibility(test.rows, nil, "standard").Valuation
			want := Valuation{State: test.state, KnownSubtotalUSD: test.subtotal, ValuedCount: test.valued, UnpricedCount: test.unpriced}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("valuation = %+v, want %+v", got, want)
			}
		})
	}
}

func TestValuation_MalformedKnownValuesCannotReachJSON(t *testing.T) {
	for _, value := range []*float64{nil, ptrFloat(-1), ptrFloat(math.Inf(1)), ptrFloat(math.NaN())} {
		got := applyVisibility([]RawAsset{{ID: "bad", ValueUSD: value, ValuationStatus: ValuationKnown}}, nil, "standard")
		item := got.Items[0]
		if item.ValueUSD != nil || item.ValuationStatus != ValuationUnavailable || item.ValuationReason == nil || got.Valuation.State != "unavailable" {
			t.Fatalf("got %+v", got)
		}
	}
}

func TestValuation_KnownReasonNullAndStableUnknownSorting(t *testing.T) {
	rows := []RawAsset{
		{ID: "z", ValueUSD: ptrFloat(0), ValuationStatus: ValuationKnown, ValuationReason: ptrStr(ReasonMissingUSDPrice)},
		{ID: "b", UpdatedAt: "2026-01-01"},
		{ID: "a", UpdatedAt: "2026-01-01"},
		{ID: "latest", UpdatedAt: "2026-10-01"},
		{ID: "positive", ValueUSD: ptrFloat(12), ValuationStatus: ValuationKnown},
	}
	got := applyVisibility(rows, nil, "standard")
	want := []string{"positive", "z", "latest", "a", "b"}
	for i, id := range want {
		if got.Items[i].ID != id {
			t.Fatalf("items=%+v", got.Items)
		}
	}
	if got.Items[1].ValuationReason != nil {
		t.Fatal("known zero must not carry an unavailable reason")
	}
}

func TestValuation_HiddenKnownValueDoesNotEnterSubtotal(t *testing.T) {
	got := applyVisibility([]RawAsset{
		{ID: "hidden", ChainID: 1, Symbol: "HIDDEN", ValueUSD: ptrFloat(100), ValuationStatus: ValuationKnown},
		{ID: "visible", ChainID: 1, Symbol: "VISIBLE", ValueUSD: ptrFloat(0), ValuationStatus: ValuationKnown},
	}, []string{"1:all-wallets:symbol:HIDDEN"}, "standard")
	if got.Valuation.ValuedCount != 1 || got.Valuation.KnownSubtotalUSD == nil || *got.Valuation.KnownSubtotalUSD != 0 {
		t.Fatalf("valuation=%+v", got.Valuation)
	}
}

func TestValuation_UnknownAloneIsNotThinMarket(t *testing.T) {
	a := RawAsset{ID: "unpriced", Symbol: "TOKEN", Name: "Token", Status: "active", Address: ptrStr("0xabc"), Metadata: &RawAssetMeta{Origin: "wallet-discovered", MarketCap: ptrFloat(1)}}
	got := applyVisibility([]RawAsset{a}, nil, "strict")
	if len(got.Items) != 1 {
		t.Fatal("missing USD price cannot imply zero-value spam")
	}
	a.Name = "Free reward"
	got = applyVisibility([]RawAsset{a}, nil, "strict")
	if len(got.Items) != 0 || got.FilterSummary.SpamFilteredCount != 1 {
		t.Fatal("unknown valuation must not bypass explicit spam signals")
	}
}

func TestValuation_CompatibilityValueRequiresCompleteReadyUncappedInventory(t *testing.T) {
	a := applyVisibility([]RawAsset{{ID: "z", ValueUSD: ptrFloat(0), ValuationStatus: ValuationKnown}}, nil, "standard")
	a.Coverage.Sources = SourceCoverage{SourceReady, SourceReady, SourceReady, SourceReady}
	if a.compatibleValue() == nil || *a.compatibleValue() != 0 {
		t.Fatal("known zero compatibility value lost")
	}
	a.truncated = true
	if a.compatibleValue() != nil {
		t.Fatal("capped inventory cannot provide compatibility total")
	}
	a.truncated = false
	a.Coverage.Sources.Holdings = SourceUnavailable
	if a.compatibleValue() != nil {
		t.Fatal("partial sources cannot provide compatibility total")
	}
	a.Coverage.Sources.Holdings = SourceReady
	a.Valuation.State = "partial"
	if a.compatibleValue() != nil {
		t.Fatal("partial valuation cannot provide compatibility total")
	}
	a.Valuation.State = "empty"
	if a.compatibleValue() != nil {
		t.Fatal("empty inventory cannot provide compatibility total")
	}
}
