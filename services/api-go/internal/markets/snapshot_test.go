package markets

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/deployments"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/token"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/trading"
)

type fakeTradingRepo struct {
	positions []trading.PerpPosition
	trades    []trading.PerpTrade
	orders    []trading.PerpOrder
	recent    []trading.PerpTradeFull
	err       error
}

func (f fakeTradingRepo) ListOpenPositions(_ context.Context, symbols []string, _ *int) ([]trading.PerpPosition, error) {
	if f.err != nil {
		return nil, f.err
	}
	if len(symbols) == 0 {
		return f.positions, nil
	}
	filtered := make([]trading.PerpPosition, 0, len(f.positions))
	for _, p := range f.positions {
		for _, s := range symbols {
			if p.Token == s {
				filtered = append(filtered, p)
				break
			}
		}
	}
	return filtered, nil
}

func (f fakeTradingRepo) ListRecentTrades(_ context.Context, symbols []string, _ *int, _ time.Time) ([]trading.PerpTrade, error) {
	if f.err != nil {
		return nil, f.err
	}
	if len(symbols) == 0 {
		return f.trades, nil
	}
	filtered := make([]trading.PerpTrade, 0, len(f.trades))
	for _, t := range f.trades {
		for _, s := range symbols {
			if t.Token == s {
				filtered = append(filtered, t)
				break
			}
		}
	}
	return filtered, nil
}

func (f fakeTradingRepo) ListOrderbookOrders(_ context.Context, _ string, _ *int) ([]trading.PerpOrder, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.orders, nil
}

func (f fakeTradingRepo) ListRecentTradesForSymbol(_ context.Context, _ string, _ *int, _ int) ([]trading.PerpTradeFull, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.recent, nil
}

type fakeTokenRepo struct {
	tokens []token.TrendingToken
	err    error
}

func (f fakeTokenRepo) ListTrending(_ context.Context, _ *int, limit int) ([]token.TrendingToken, error) {
	if f.err != nil {
		return nil, f.err
	}
	if limit < len(f.tokens) {
		return f.tokens[:limit], nil
	}
	return f.tokens, nil
}

func freshDeployments() fakeLoader {
	return fakeLoader{registry: map[int]deployments.ChainConfig{
		11155111: {Contracts: map[string]deployments.ContractInfo{
			"MockWETH": {Address: "0xWETH-sep"},
			"MockWBTC": {Address: "0xWBTC-sep"},
			"MockUSDC": {Address: "0xUSDC-sep"},
		}},
		31337: {Contracts: map[string]deployments.ContractInfo{
			"weth": {Address: "0xWETH-loc"},
			"wbtc": {Address: "0xWBTC-loc"},
			"usdc": {Address: "0xUSDC-loc"},
		}},
	}}
}

func ptrFloat(v float64) *float64 { return &v }

func ptrString(v string) *string { return &v }

func pinClock(s *Service, when string) {
	t, err := time.Parse(time.RFC3339, when)
	if err != nil {
		panic(err)
	}
	s.SetClock(func() time.Time { return t })
}

func TestGetSnapshot_HappyPath(t *testing.T) {
	trading := fakeTradingRepo{
		positions: []trading.PerpPosition{
			{Token: "ETH-USD", IsLong: true, Size: "100"},
			{Token: "ETH-USD", IsLong: true, Size: "200"},
			{Token: "ETH-USD", IsLong: false, Size: "50"},
			{Token: "BTC-USD", IsLong: true, Size: "1000"},
			{Token: "ORPHAN", IsLong: true, Size: "999999"}, // not configured — only counts toward totalOI
		},
		trades: []trading.PerpTrade{
			{Token: "ETH-USD", SizeDelta: "10"},
			{Token: "ETH-USD", SizeDelta: "20"},
			{Token: "BTC-USD", SizeDelta: "500"},
			{Token: "ORPHAN", SizeDelta: "777"},
		},
	}
	tokens := fakeTokenRepo{tokens: []token.TrendingToken{
		{Symbol: "TKN1", Address: ptrString("0xTKN1"), PriceChange24h: ptrFloat(12.5)},
		{Symbol: "TKN2", Address: nil, PriceChange24h: nil},
	}}

	svc := NewService(freshDeployments(), trading, tokens)
	pinClock(svc, "2026-05-20T03:00:00Z")

	got, err := svc.GetSnapshot(context.Background(), ptrInt(11155111))
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}

	if got.GeneratedAt != "2026-05-20T03:00:00Z" {
		t.Errorf("generatedAt = %q", got.GeneratedAt)
	}
	if got.Summary.ActiveMarkets != 2 {
		t.Errorf("activeMarkets = %d, want 2 (ETH + BTC on sepolia)", got.Summary.ActiveMarkets)
	}
	// totalOpenInterest pulls from positionsAll = positions filtered by chain
	// only, which in our fake includes ORPHAN as well:
	//   100 + 200 + 50 + 1000 + 999999 = 1001349
	if got.Summary.TotalOpenInterest != "1001349" {
		t.Errorf("totalOpenInterest = %q, want %q", got.Summary.TotalOpenInterest, "1001349")
	}
	if got.Summary.TotalVolume != "1307" {
		t.Errorf("totalVolume = %q, want %q", got.Summary.TotalVolume, "1307")
	}

	if len(got.Symbols) != 2 {
		t.Fatalf("symbols len = %d, want 2", len(got.Symbols))
	}
	eth := got.Symbols[0]
	if eth.Symbol != "ETH-USD" || eth.LongOpenInterest != "300" || eth.ShortOpenInterest != "50" || eth.Volume24h != "30" {
		t.Errorf("ETH symbol mismatch: %+v", eth)
	}
	btc := got.Symbols[1]
	if btc.Symbol != "BTC-USD" || btc.LongOpenInterest != "1000" || btc.ShortOpenInterest != "0" || btc.Volume24h != "500" {
		t.Errorf("BTC symbol mismatch: %+v", btc)
	}

	if len(got.TopMovers) != 2 {
		t.Fatalf("topMovers len = %d", len(got.TopMovers))
	}
	if got.TopMovers[0].Change24h != 12.5 || got.TopMovers[0].Symbol != "TKN1" || got.TopMovers[0].Address == nil || *got.TopMovers[0].Address != "0xTKN1" {
		t.Errorf("TKN1 mismatch: %+v", got.TopMovers[0])
	}
	if got.TopMovers[1].Change24h != 0 || got.TopMovers[1].Address != nil {
		t.Errorf("TKN2 null fields not preserved: %+v", got.TopMovers[1])
	}
}

func TestGetSnapshot_NilReposDegrade(t *testing.T) {
	svc := NewService(freshDeployments(), nil, nil)
	pinClock(svc, "2026-05-20T03:00:00Z")

	got, err := svc.GetSnapshot(context.Background(), nil)
	if err != nil {
		t.Fatalf("GetSnapshot: %v", err)
	}
	if got.Summary.ActiveMarkets != 4 {
		t.Errorf("activeMarkets = %d, want 4 (2 symbols × 2 chains)", got.Summary.ActiveMarkets)
	}
	if got.Summary.TotalVolume != "0" || got.Summary.TotalOpenInterest != "0" {
		t.Errorf("expected zero totals; got %+v", got.Summary)
	}
	if len(got.Symbols) != 4 {
		t.Errorf("symbols len = %d", len(got.Symbols))
	}
	for _, s := range got.Symbols {
		if s.LongOpenInterest != "0" || s.ShortOpenInterest != "0" || s.Volume24h != "0" {
			t.Errorf("expected zeroed symbol; got %+v", s)
		}
	}
	if len(got.TopMovers) != 0 {
		t.Errorf("topMovers should be empty; got %+v", got.TopMovers)
	}
}

func TestGetSnapshot_PoolUnavailableDegrades(t *testing.T) {
	// Repo present but returns ErrPoolUnavailable — service should still
	// return a complete response with zeroed sums (matches NestJS bootstrap
	// degradation when DB envs aren't configured).
	tradingRepo := fakeTradingRepo{err: trading.ErrPoolUnavailable}
	tokenRepo := fakeTokenRepo{err: token.ErrPoolUnavailable}
	svc := NewService(freshDeployments(), tradingRepo, tokenRepo)
	pinClock(svc, "2026-05-20T03:00:00Z")

	got, err := svc.GetSnapshot(context.Background(), nil)
	if err != nil {
		t.Fatalf("expected nil error on pool unavailable; got %v", err)
	}
	if got.Summary.TotalVolume != "0" || got.Summary.TotalOpenInterest != "0" {
		t.Errorf("expected zeroed totals; got %+v", got.Summary)
	}
}

func TestGetSnapshot_RealErrorPropagates(t *testing.T) {
	sentinel := errors.New("db blew up")
	tradingRepo := fakeTradingRepo{err: sentinel}
	svc := NewService(freshDeployments(), tradingRepo, nil)
	if _, err := svc.GetSnapshot(context.Background(), nil); !errors.Is(err, sentinel) {
		t.Errorf("expected sentinel; got %v", err)
	}
}

func TestSnapshotHandler_ContentTypeAndShape(t *testing.T) {
	svc := NewService(freshDeployments(), nil, nil)
	pinClock(svc, "2026-05-20T03:00:00Z")

	req := httptest.NewRequest(http.MethodGet, "/snapshot?chainId=31337", nil)
	rr := httptest.NewRecorder()
	Router(svc).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content-type = %q", ct)
	}
	var got Snapshot
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, rr.Body.String())
	}
	if got.Summary.ActiveMarkets != 2 {
		t.Errorf("expected 2 markets on local chain; got %d", got.Summary.ActiveMarkets)
	}
}
