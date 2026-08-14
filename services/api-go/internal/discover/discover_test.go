package discover

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/markets"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/staking"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/token"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/web3events"
)

type fakeMarkets struct {
	rows []markets.MarketFullView
	err  error
}

func (f fakeMarkets) GetMarketsFull(_ context.Context, _ *int) ([]markets.MarketFullView, error) {
	return f.rows, f.err
}

type fakeTokens struct {
	rows []token.TrendingTokenFull
	err  error
}

func (f fakeTokens) ListTrendingFull(_ context.Context, _ *int, _ int) ([]token.TrendingTokenFull, error) {
	return f.rows, f.err
}

type fakeStaking struct {
	rows []staking.PoolView
	err  error
}

func (f fakeStaking) ListActivePools(_ context.Context, _ *int) ([]staking.PoolView, error) {
	return f.rows, f.err
}

type fakeEvents struct {
	stats web3events.Stats
	err   error
}

func (f fakeEvents) GetStats(_ context.Context, _ int) (web3events.Stats, error) {
	return f.stats, f.err
}

func ptrFloat(v float64) *float64 { return &v }
func ptrString(v string) *string  { return &v }

func pinClock(s *Service, when string) {
	t, err := time.Parse(time.RFC3339, when)
	if err != nil {
		panic(err)
	}
	s.SetClock(func() time.Time { return t })
}

func TestGetHome_HappyPath(t *testing.T) {
	svc := NewService(
		fakeMarkets{rows: []markets.MarketFullView{
			{Symbol: "ETH-USD", ChainID: 11155111, Volume24h: "100", FundingRate: "0", LongOpenInterest: "30", ShortOpenInterest: "20"},
			{Symbol: "BTC-USD", ChainID: 11155111, Volume24h: "200", FundingRate: "0", LongOpenInterest: "50", ShortOpenInterest: "10"},
		}},
		fakeTokens{rows: []token.TrendingTokenFull{
			{ID: "t1", Symbol: "ALPHA", Name: "Alpha", Address: ptrString("0xA"), PriceChange24h: ptrFloat(15.5), Volume24h: ptrFloat(1000), MarketCap: ptrFloat(5000), IsOfficial: true, Status: "LAUNCHED"},
			{ID: "t2", Symbol: "BETA", Name: "Beta", Address: nil, PriceChange24h: nil, Volume24h: nil, MarketCap: nil, IsOfficial: false, Status: "PENDING"},
		}},
		fakeStaking{rows: []staking.PoolView{
			{ID: "p1", Name: "Pool 1", PoolType: "staking", APY: ptrFloat(12.5), TVL: ptrFloat(100000), ChainID: 11155111, Status: "active"},
		}},
		fakeEvents{stats: web3events.Stats{TotalEvents: 42, IndexerBlock: "9000"}},
	)
	pinClock(svc, "2026-05-20T08:00:00Z")

	got, err := svc.GetHome(context.Background(), nil)
	if err != nil {
		t.Fatalf("GetHome: %v", err)
	}

	if got.GeneratedAt != "2026-05-20T08:00:00Z" {
		t.Errorf("generatedAt = %q", got.GeneratedAt)
	}
	if len(got.TrendingTokens) != 2 {
		t.Fatalf("trendingTokens count = %d", len(got.TrendingTokens))
	}
	if got.TrendingTokens[0].Symbol != "ALPHA" || got.TrendingTokens[0].PriceChange24h != 15.5 || got.TrendingTokens[0].Badges[0] != "verified" || got.TrendingTokens[0].Badges[1] != "launched" {
		t.Errorf("alpha trending mismatch: %+v", got.TrendingTokens[0])
	}
	if got.TrendingTokens[1].PriceChange24h != 0 || got.TrendingTokens[1].Badges[0] != "community" || got.TrendingTokens[1].Badges[1] != "pending" {
		t.Errorf("beta nullable coercion broken: %+v", got.TrendingTokens[1])
	}
	if len(got.CuratedDapps) != 3 || got.CuratedDapps[0].ID != "swap" {
		t.Errorf("curatedDapps not preserved: %+v", got.CuratedDapps)
	}
	// Honesty guard on the curated cards. This used to pin bridge to "roadmap",
	// correct while no adapter existed and false once BridgeGateway shipped —
	// understating a working feature is the same defect as overstating one.
	// What is actually invariant is that every status is one the frontend knows
	// how to render, and that a card's summary never contradicts its status.
	for _, d := range got.CuratedDapps {
		if d.Status != "live" && d.Status != "roadmap" {
			t.Errorf("%s curated card has unknown status %q", d.ID, d.Status)
		}
		if d.Status == "live" && strings.Contains(strings.ToLower(d.Summary), "roadmap") {
			t.Errorf("%s is live but its summary still calls it roadmap: %q", d.ID, d.Summary)
		}
		if d.Status == "roadmap" && strings.TrimSpace(d.Summary) == "" {
			t.Errorf("%s is roadmap but does not say why", d.ID)
		}
	}
	if len(got.EarnProducts) != 1 || got.EarnProducts[0].APY != 12.5 || got.EarnProducts[0].TVL != 100000 {
		t.Errorf("earnProducts mismatch: %+v", got.EarnProducts)
	}
	if len(got.MarketPulse) != 2 || got.MarketPulse[0].Symbol != "ETH-USD" || got.MarketPulse[0].OpenInterest.Long != "30" {
		t.Errorf("marketPulse mismatch: %+v", got.MarketPulse)
	}
	if len(got.RiskMarkers) != 2 || got.RiskMarkers[0].ID != "events" || got.RiskMarkers[0].Value.(int64) != 42 || got.RiskMarkers[1].Value.(string) != "9000" {
		t.Errorf("riskMarkers mismatch: %+v", got.RiskMarkers)
	}
}

func TestGetHome_NilDepsDegrade(t *testing.T) {
	// No deps wired at all → response still shaped correctly with empty
	// slices and the static curatedDapps.
	svc := NewService(nil, nil, nil, nil)
	pinClock(svc, "2026-05-20T08:00:00Z")

	got, err := svc.GetHome(context.Background(), nil)
	if err != nil {
		t.Fatalf("GetHome: %v", err)
	}
	if len(got.TrendingTokens) != 0 || len(got.EarnProducts) != 0 || len(got.MarketPulse) != 0 {
		t.Errorf("expected empty feeds; got %+v", got)
	}
	if len(got.CuratedDapps) != 3 {
		t.Errorf("curatedDapps should always render")
	}
	// RiskMarkers are computed from web3Events; with nil repo, Stats zero-value:
	// totalEvents=0, indexerBlock="".
	if len(got.RiskMarkers) != 2 || got.RiskMarkers[0].Value.(int64) != 0 {
		t.Errorf("riskMarkers should default-zero; got %+v", got.RiskMarkers)
	}
}

func TestGetHome_PoolUnavailableDegrades(t *testing.T) {
	// Each repo present but unavailable → response still shaped, no error.
	svc := NewService(
		fakeMarkets{}, // returns empty rows, nil err
		fakeTokens{err: token.ErrPoolUnavailable},
		fakeStaking{err: staking.ErrPoolUnavailable},
		fakeEvents{err: web3events.ErrPoolUnavailable},
	)
	pinClock(svc, "2026-05-20T08:00:00Z")

	got, err := svc.GetHome(context.Background(), nil)
	if err != nil {
		t.Fatalf("expected no error on pool-unavailable; got %v", err)
	}
	if len(got.TrendingTokens) != 0 || len(got.EarnProducts) != 0 {
		t.Errorf("expected degraded empty feeds")
	}
}

func TestGetHome_RealErrorPropagates(t *testing.T) {
	sentinel := errors.New("db blew up")
	svc := NewService(
		fakeMarkets{},
		fakeTokens{err: sentinel},
		nil, nil,
	)
	if _, err := svc.GetHome(context.Background(), nil); !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel; got %v", err)
	}
}

func TestGetHome_MarketPulseTruncatedToFive(t *testing.T) {
	rows := []markets.MarketFullView{}
	for i := 0; i < 7; i++ {
		rows = append(rows, markets.MarketFullView{Symbol: "X", ChainID: 1, Volume24h: "0", FundingRate: "0", LongOpenInterest: "0", ShortOpenInterest: "0"})
	}
	svc := NewService(fakeMarkets{rows: rows}, nil, nil, nil)
	pinClock(svc, "2026-05-20T08:00:00Z")
	got, _ := svc.GetHome(context.Background(), nil)
	if len(got.MarketPulse) != 5 {
		t.Errorf("marketPulse should cap at 5; got %d", len(got.MarketPulse))
	}
}

func TestGetHome_EarnProductsTruncatedToFour(t *testing.T) {
	rows := []staking.PoolView{}
	for i := 0; i < 6; i++ {
		rows = append(rows, staking.PoolView{ID: "x", Name: "p", PoolType: "staking", ChainID: 1, Status: "active"})
	}
	svc := NewService(fakeMarkets{}, nil, fakeStaking{rows: rows}, nil)
	pinClock(svc, "2026-05-20T08:00:00Z")
	got, _ := svc.GetHome(context.Background(), nil)
	if len(got.EarnProducts) != 4 {
		t.Errorf("earnProducts should cap at 4; got %d", len(got.EarnProducts))
	}
}

func TestHomeHandler_200(t *testing.T) {
	svc := NewService(fakeMarkets{}, nil, nil, nil)
	pinClock(svc, "2026-05-20T08:00:00Z")

	req := httptest.NewRequest(http.MethodGet, "/home?chainId=11155111", nil)
	rr := httptest.NewRecorder()
	Router(svc).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got Home
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, rr.Body.String())
	}
	if len(got.CuratedDapps) != 3 {
		t.Errorf("curatedDapps not in body: %+v", got)
	}
}

func TestParseChainIDQuery(t *testing.T) {
	tests := []struct {
		in   string
		want *int
	}{
		{"", nil},
		{"abc", nil},
		{"0", nil},
		{"11155111", func() *int { v := 11155111; return &v }()},
	}
	for _, tc := range tests {
		got := parseChainIDQuery(tc.in)
		switch {
		case got == nil && tc.want == nil:
		case got == nil || tc.want == nil:
			t.Errorf("%q → %v, want %v", tc.in, got, tc.want)
		case *got != *tc.want:
			t.Errorf("%q → %d, want %d", tc.in, *got, *tc.want)
		}
	}
}
