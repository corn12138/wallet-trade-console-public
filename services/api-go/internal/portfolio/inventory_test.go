package portfolio

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/settings"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/staking"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/token"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/trading"
)

func TestInventory_ProtocolAmountsNeverBecomePersonalUSD(t *testing.T) {
	ctx := context.Background()
	chain := 11155111
	when := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	size := "1000" + strings.Repeat("0", 30)
	rawHolding := "100000000000000000000000000001"
	f := &inventoryFixture{
		stakes:    []staking.UserStake{{ID: "s", Amount: "100.000000000000000001", PoolName: "STK Pool", PoolChainID: chain, StakedAt: when}},
		positions: []trading.PerpPositionForAccount{{ID: "p", ChainID: chain, Token: "ETH-USD", Size: size, Collateral: "100" + strings.Repeat("0", 30), Status: "OPEN", UpdatedAt: when}},
		created: []token.Token{
			{ID: "c", ChainID: chain, Symbol: "CREATED", MarketCap: ptrDec("1000000000"), CreatedAt: when},
			{ID: "z", ChainID: chain, Symbol: "ZERO", CreatedAt: when},
		},
		holdings: []token.HoldingForOwner{
			{TokenID: "z", UserAddress: validPortfolioAddr, Balance: "0", HolderUpdated: when},
			{TokenID: "h", UserAddress: validPortfolioAddr, Balance: rawHolding, TokenSymbol: "HELD", TokenChainID: chain, TokenIsOfficial: true, TokenAddress: ptrStr("0xabc"), HolderUpdated: when},
		},
	}
	got, err := fixtureService(f).GetAssets(ctx, validPortfolioAddr, &chain)
	if err != nil {
		t.Fatal(err)
	}
	if got.ValuationVersion != 1 || got.Valuation.State != "unavailable" || got.Valuation.ValuedCount != 0 || got.Valuation.UnpricedCount != 5 || got.Valuation.KnownSubtotalUSD != nil {
		t.Fatalf("valuation = %+v", got)
	}
	want := map[string]struct{ raw, unit, reason string }{
		"stake:s":                         {"100.000000000000000001", BalanceToken, ReasonMissingUSDPrice},
		"perp:p":                          {size, BalanceUSD30, ReasonPositionEquityUnavailable},
		"token:c":                         {"", BalanceBaseUnit, ReasonHolderValuationUnavailable},
		"token:z":                         {"0", BalanceBaseUnit, ReasonHolderValuationUnavailable},
		"holding:h:" + validPortfolioAddr: {rawHolding, BalanceBaseUnit, ReasonHolderValuationUnavailable},
	}
	for _, item := range got.Items {
		w, ok := want[item.ID]
		if !ok || item.RawBalance != w.raw || item.BalanceUnit != w.unit || item.ValueUSD != nil || item.ValuationStatus != ValuationUnavailable || item.ValuationReason == nil || *item.ValuationReason != w.reason {
			t.Errorf("item = %+v, want %+v", item, w)
		}
	}
	if got.Coverage.Scope != "indexed-protocol-assets" || got.Coverage.Sources.readyCount() != 4 || got.Coverage.Limits != (CoverageLimits{20, 40}) {
		t.Errorf("coverage = %+v", got.Coverage)
	}
	if f.createdQuery.Limit != 20 || f.createdQuery.ChainID != &chain {
		t.Errorf("created query = %+v", f.createdQuery)
	}
	summary, err := fixtureService(f).GetSummary(ctx, validPortfolioAddr, &chain)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(summary.Valuation, got.Valuation) || !reflect.DeepEqual(summary.Coverage, got.Coverage) || summary.PortfolioValueUSD != nil {
		t.Errorf("summary does not match asset valuation: %+v", summary)
	}
}

func TestInventory_StakingChainIsolationAndCrossChainRead(t *testing.T) {
	f := &inventoryFixture{stakes: []staking.UserStake{{ID: "a", PoolChainID: 1}, {ID: "b", PoolChainID: 2}}}
	chain := 2
	got, err := fixtureService(f).GetAssets(context.Background(), validPortfolioAddr, &chain)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 1 || got.Items[0].ChainID != 2 {
		t.Fatalf("chain scoped = %+v", got.Items)
	}
	got, err = fixtureService(f).GetAssets(context.Background(), validPortfolioAddr, nil)
	if err != nil || len(got.Items) != 2 {
		t.Fatalf("cross chain = %+v, %v", got.Items, err)
	}
}

func TestInventory_SourceAvailabilityIsIndependentOfRows(t *testing.T) {
	for _, name := range []string{"ready empty", "partial empty", "all missing", "all unavailable", "unexpected failure"} {
		t.Run(name, func(t *testing.T) {
			f := &inventoryFixture{}
			svc := fixtureService(f)
			wantReady := 4
			var wantErr error
			switch name {
			case "partial empty":
				f.stakesErr = staking.ErrPoolUnavailable
				wantReady = 3
			case "all missing":
				svc = NewServiceFromDeps(Deps{Settings: f})
				wantErr = ErrSourcesUnavailable
			case "all unavailable":
				f.stakesErr = staking.ErrPoolUnavailable
				f.positionsErr = trading.ErrPoolUnavailable
				f.createdErr = token.ErrPoolUnavailable
				f.holdingsErr = token.ErrPoolUnavailable
				wantErr = ErrSourcesUnavailable
			case "unexpected failure":
				wantErr = errors.New("query failure")
				f.stakesErr = wantErr
			}
			got, err := svc.GetAssets(context.Background(), validPortfolioAddr, nil)
			if !errors.Is(err, wantErr) {
				t.Fatalf("err = %v, want %v", err, wantErr)
			}
			if wantErr != nil {
				return
			}
			if got.Coverage.Sources.readyCount() != wantReady || got.Valuation.State != "empty" || got.Valuation.KnownSubtotalUSD != nil || got.Items == nil {
				t.Fatalf("inventory = %+v", got)
			}
		})
	}
}

func TestInventory_VisibilityFailsBeforeAssetReads(t *testing.T) {
	for _, failure := range []error{settings.ErrPoolUnavailable, errors.New("visibility query failed")} {
		for _, summary := range []bool{false, true} {
			f := &inventoryFixture{settingsErr: failure, stakes: []staking.UserStake{{ID: "secret"}}}
			svc := fixtureService(f)
			var err error
			if summary {
				_, err = svc.GetSummary(context.Background(), validPortfolioAddr, nil)
			} else {
				_, err = svc.GetAssets(context.Background(), validPortfolioAddr, nil)
			}
			var visibilityErr *visibilityReadError
			if !errors.As(err, &visibilityErr) || !errors.Is(err, failure) {
				t.Fatalf("err = %v", err)
			}
			if !reflect.DeepEqual(f.calls, []string{"settings"}) {
				t.Fatalf("read assets without visibility: %v", f.calls)
			}
		}
	}
}

func TestInventory_HiddenRowsExcludedFromMetadata(t *testing.T) {
	f := &inventoryFixture{stakes: []staking.UserStake{{ID: "s", PoolChainID: 1, PoolName: "STK"}}, hidden: []settings.HiddenAsset{{AssetKey: "1:all-wallets:symbol:STK"}}}
	got, err := fixtureService(f).GetAssets(context.Background(), validPortfolioAddr, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 0 || got.Valuation.UnpricedCount != 0 || got.Valuation.State != "empty" || got.FilterSummary.HiddenMatchedCount != 1 {
		t.Fatalf("got %+v", got)
	}
	summary, err := fixtureService(f).GetSummary(context.Background(), validPortfolioAddr, nil)
	if err != nil || summary.DataCompleteness != "partial-live-filtered" {
		t.Fatalf("summary = %+v, %v", summary, err)
	}
}

func TestInventory_QueryCapsBlockCompatibilityTotal(t *testing.T) {
	for _, f := range []*inventoryFixture{
		{created: []token.Token{{ID: "c"}}, createdTotal: 21},
		{holdings: make([]token.HoldingForOwner, 40)},
	} {
		got, err := fixtureService(f).loadAssetSources(context.Background(), validPortfolioAddr, nil)
		if err != nil || !got.truncated {
			t.Fatalf("sources = %+v, %v", got, err)
		}
	}
}
