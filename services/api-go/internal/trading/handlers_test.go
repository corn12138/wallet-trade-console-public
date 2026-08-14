package trading

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeMarketsProvider lets the Service /markets path execute without
// the full markets package. Returns the seeded rows verbatim.
type fakeMarketsProvider struct {
	rows []MarketSnapshotRow
	err  error
}

func (f fakeMarketsProvider) GetMarketsFull(ctx context.Context, chainID *int) ([]MarketSnapshotRow, error) {
	return f.rows, f.err
}

func TestService_GetMarketsDelegatesToProvider(t *testing.T) {
	want := []MarketSnapshotRow{
		{Symbol: "ETH-USD", ChainID: 11155111, IndexToken: "0xWETH", CollateralToken: "0xUSDC", FundingRate: "0", Volume24h: "100", LongOpenInterest: "50", ShortOpenInterest: "25"},
	}
	svc := NewService(NewRepository(nil), fakeMarketsProvider{rows: want})
	got, err := svc.GetMarkets(context.Background(), nil)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(got) != 1 || got[0].Symbol != "ETH-USD" {
		t.Errorf("got %+v, want one ETH-USD row", got)
	}
}

func TestService_GetMarketsNilProviderDegradesToEmpty(t *testing.T) {
	svc := NewService(NewRepository(nil), nil)
	got, err := svc.GetMarkets(context.Background(), nil)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("got %v, want empty slice (never nil)", got)
	}
}

func TestService_PositionsNilPoolDegradesToEmpty(t *testing.T) {
	svc := NewService(NewRepository(nil), nil)
	got, err := svc.GetPositions(context.Background(), "0xabc", nil)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestService_PendingOrdersNilPoolDegradesToEmpty(t *testing.T) {
	svc := NewService(NewRepository(nil), nil)
	got, err := svc.GetPendingOrders(context.Background(), "0xabc", "ETH-USD", nil)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestService_TradeHistoryNilPoolDegradesToEmpty(t *testing.T) {
	svc := NewService(NewRepository(nil), nil)
	got, err := svc.GetTradeHistory(context.Background(), "0xabc", "", nil)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestService_OrderbookNilPoolDegradesToEmptyArrays(t *testing.T) {
	svc := NewService(NewRepository(nil), nil)
	got, err := svc.GetOrderbook(context.Background(), "ETH-USD", nil)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	// Always { "bids": [], "asks": [] }, never nil — FE relies on
	// arrays for the orderbook chart's empty state.
	if got.Bids == nil || got.Asks == nil {
		t.Errorf("got %+v, want non-nil arrays", got)
	}
}

func TestService_RecentMarketTradesNilPoolDegradesToEmpty(t *testing.T) {
	svc := NewService(NewRepository(nil), nil)
	got, err := svc.GetRecentMarketTrades(context.Background(), "ETH-USD", nil, 50)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestService_MarketStatsNilPoolReturnsZeros(t *testing.T) {
	svc := NewService(NewRepository(nil), nil)
	got, err := svc.GetMarketStats(context.Background(), nil)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	// bigsum.Sum on an empty slice returns "0" — confirms the degraded
	// path threads through without panicking.
	if got.TotalVolume != "0" || got.TotalOpenInterest != "0" {
		t.Errorf("got %+v, want zeros", got)
	}
}

func TestParseTradingCandleResolution(t *testing.T) {
	cases := []struct {
		in   string
		want TradingCandleResolution
	}{
		{"1m", TradingCandle1m},
		{"15m", TradingCandle15m},
		{"4h", TradingCandle4h},
		{"1d", TradingCandle1d},
		// Default + unknown both fall back to 15m (perp-trading default).
		{"", TradingCandle15m},
		{"7d", TradingCandle15m},
		{"garbage", TradingCandle15m},
	}
	for _, tc := range cases {
		if got := ParseTradingCandleResolution(tc.in); got != tc.want {
			t.Errorf("ParseTradingCandleResolution(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

func TestService_GetCandlesEmptySymbolReturnsEmpty(t *testing.T) {
	svc := NewService(NewRepository(nil), nil)
	got, err := svc.GetCandles(context.Background(), "", TradingCandle15m, 100, 0, 0, nil)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestService_GetCandlesNilPoolDegradesToEmpty(t *testing.T) {
	svc := NewService(NewRepository(nil), nil)
	got, err := svc.GetCandles(context.Background(), "ETH-USD", TradingCandle15m, 100, 0, 0, nil)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestClampListLimit(t *testing.T) {
	cases := []struct {
		raw          string
		defaultLimit int
		max          int
		want         int
	}{
		{"", 20, 200, 20},       // empty → default
		{"abc", 20, 200, 20},    // non-numeric → default
		{"-5", 20, 200, 20},     // non-positive → default
		{"50", 20, 200, 50},     // valid → returned
		{"99999", 20, 200, 200}, // clamped to max
	}
	for _, tc := range cases {
		if got := clampListLimit(tc.raw, tc.defaultLimit, tc.max); got != tc.want {
			t.Errorf("clampListLimit(%q, %d, %d) = %d, want %d", tc.raw, tc.defaultLimit, tc.max, got, tc.want)
		}
	}
}

func TestParseChainIDQuery(t *testing.T) {
	// JS truthy coercion: "0" → no filter.
	if got := parseChainIDQuery("0"); got != nil {
		t.Errorf("'0' should yield nil filter; got %v", *got)
	}
	if got := parseChainIDQuery(""); got != nil {
		t.Errorf("'' should yield nil filter; got %v", *got)
	}
	if got := parseChainIDQuery("notnumber"); got != nil {
		t.Errorf("non-numeric should yield nil filter; got %v", *got)
	}
	if got := parseChainIDQuery("11155111"); got == nil || *got != 11155111 {
		t.Errorf("got %v, want 11155111", got)
	}
}

func TestAggregateTradingCandles_BasicOHLC(t *testing.T) {
	// 3 trades inside the same 15m bucket: prices 100, 150, 120.
	// open=100, close=120, high=150, low=100, volume=30.
	bucketStart := int64(1700000000_000) // arbitrary
	res := int64(15 * 60 * 1000)
	trades := []TradeCandleRow{
		{Price: "100", SizeDelta: "10", CreatedAt: msToTime(bucketStart + 1)},
		{Price: "150", SizeDelta: "10", CreatedAt: msToTime(bucketStart + 2)},
		{Price: "120", SizeDelta: "10", CreatedAt: msToTime(bucketStart + 3)},
	}
	got := aggregateTradingCandles(trades, res, 10)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1 candle", len(got))
	}
	c := got[0]
	if c.Open != "100" || c.Close != "120" || c.High != "150" || c.Low != "100" || c.Volume != "30" || c.Trades != 3 {
		t.Errorf("got %+v, want OHLCV(100,150,100,120,30) trades=3", c)
	}
}

func TestAggregateTradingCandles_SortsDescAndClamps(t *testing.T) {
	// Two buckets — newer should come first; limit clamps the slice.
	res := int64(15 * 60 * 1000)
	older := int64(1700000000_000)
	newer := older + res*2
	trades := []TradeCandleRow{
		{Price: "100", SizeDelta: "1", CreatedAt: msToTime(older)},
		{Price: "200", SizeDelta: "1", CreatedAt: msToTime(newer)},
	}
	got := aggregateTradingCandles(trades, res, 10)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Timestamp <= got[1].Timestamp {
		t.Errorf("got DESC = %d, %d — want newer first", got[0].Timestamp, got[1].Timestamp)
	}
	// Clamp to 1
	one := aggregateTradingCandles(trades, res, 1)
	if len(one) != 1 {
		t.Errorf("len = %d, want 1 (clamped)", len(one))
	}
}

// ---------- handler tests ----------

func TestHandler_MarketsReturnsEmptyWhenNoProvider(t *testing.T) {
	svc := NewService(NewRepository(nil), nil)
	mux := Router(svc, nil)
	req := httptest.NewRequest(http.MethodGet, "/markets", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body []MarketSnapshotRow
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body) != 0 {
		t.Errorf("len = %d, want 0", len(body))
	}
}

func TestHandler_OrderbookMissingSymbolReturns400(t *testing.T) {
	svc := NewService(NewRepository(nil), nil)
	mux := Router(svc, nil)
	req := httptest.NewRequest(http.MethodGet, "/orderbook", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestHandler_MarketTradesMissingSymbolReturns400(t *testing.T) {
	svc := NewService(NewRepository(nil), nil)
	mux := Router(svc, nil)
	req := httptest.NewRequest(http.MethodGet, "/market-trades", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestHandler_CandlesEmptySymbolReturnsEmpty(t *testing.T) {
	// /candles with no symbol returns [] (matches NestJS) rather than
	// 400. The chart hides itself based on empty data.
	svc := NewService(NewRepository(nil), nil)
	mux := Router(svc, nil)
	req := httptest.NewRequest(http.MethodGet, "/candles", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.HasPrefix(rec.Body.String(), "[]") {
		t.Errorf("body = %q, want []", rec.Body.String())
	}
}

func TestHandler_StatsReturnsZeroesUnderDegradedMode(t *testing.T) {
	svc := NewService(NewRepository(nil), nil)
	mux := Router(svc, nil)
	req := httptest.NewRequest(http.MethodGet, "/stats", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body MarketStatsView
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.TotalVolume != "0" || body.TotalOpenInterest != "0" {
		t.Errorf("got %+v, want zeros", body)
	}
}

func msToTime(ms int64) time.Time {
	return time.UnixMilli(ms)
}
