package portfolio

import (
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/token"
)

func TestAssetRows_CreatedHoldingUsesLatestTimestamp(t *testing.T) {
	whole := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	fractional := whole.Add(123 * time.Millisecond)
	for _, test := range []struct {
		name            string
		created, holder time.Time
	}{
		{"holding newer", whole, fractional},
		{"creation newer", fractional, whole},
	} {
		t.Run(test.name, func(t *testing.T) {
			rows := assetRows(validPortfolioAddr, nil, nil, nil,
				[]token.Token{{ID: "created", CreatedAt: test.created}},
				[]token.HoldingForOwner{{TokenID: "created", Balance: "1", HolderUpdated: test.holder}},
			)
			if len(rows) != 1 || rows[0].UpdatedAt != "2026-10-03T00:00:00.123Z" {
				t.Fatalf("merged rows = %+v", rows)
			}
		})
	}
}

func TestVisibility_UpdatedAtSortsByInstant(t *testing.T) {
	for _, known := range []bool{false, true} {
		rows := []RawAsset{
			{ID: "whole", UpdatedAt: "2026-10-03T00:00:00Z"},
			{ID: "fractional-b", UpdatedAt: "2026-10-03T00:00:00.123Z"},
			{ID: "fractional-a", UpdatedAt: "2026-10-03T08:00:00.123000000+08:00"},
			{ID: "fractional-latest", UpdatedAt: "2026-10-03T00:00:00.123000001Z"},
			{ID: "missing"},
		}
		if known {
			for i := range rows {
				rows[i].ValueUSD = ptrFloat(0)
				rows[i].ValuationStatus = ValuationKnown
			}
		}
		got := applyVisibility(rows, nil, "standard")
		want := []string{"fractional-latest", "fractional-a", "fractional-b", "whole", "missing"}
		for i, id := range want {
			if got.Items[i].ID != id {
				t.Fatalf("known=%v items = %+v", known, got.Items)
			}
		}
	}
}
