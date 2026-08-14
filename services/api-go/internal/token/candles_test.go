package token

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseCandleResolution(t *testing.T) {
	cases := []struct {
		in   string
		want CandleResolution
	}{
		{"1m", Resolution1m},
		{"5m", Resolution5m},
		{"15m", Resolution15m},
		{"1h", Resolution1h},
		{"4h", Resolution4h},
		{"1d", Resolution1d},
		{"", Resolution1h},
		{"7d", Resolution1h},
		{"garbage", Resolution1h},
	}
	for _, tc := range cases {
		if got := ParseCandleResolution(tc.in); got != tc.want {
			t.Errorf("ParseCandleResolution(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}
}

func TestTokenCandleLimitBounds(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value int
		want  int
	}{
		{name: "default", value: 0, want: defaultTokenCandleLimit},
		{name: "frontend chart value", value: 200, want: 200},
		{name: "cap oversized", value: maxTokenCandleLimit + 1, want: maxTokenCandleLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := boundedLimit(tc.value, defaultTokenCandleLimit, maxTokenCandleLimit); got != tc.want {
				t.Fatalf("bounded limit = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestBoundedCandleTradeWindow(t *testing.T) {
	const nowMS int64 = 2_000_000
	from, to, empty := boundedCandleTradeWindow(resolutionMS[Resolution1m], 5, 1, 0, nowMS)
	if empty {
		t.Fatal("current window unexpectedly reported empty")
	}
	if want := nowMS - 5*resolutionMS[Resolution1m]; from != want {
		t.Fatalf("from = %d, want %d", from, want)
	}
	if to != 0 {
		t.Fatalf("to = %d, want 0", to)
	}

	const historicalTo int64 = 1_000_000
	from, to, empty = boundedCandleTradeWindow(resolutionMS[Resolution1m], 5, 1, historicalTo, nowMS)
	if empty {
		t.Fatal("historical window unexpectedly reported empty")
	}
	if want := historicalTo - 5*resolutionMS[Resolution1m]; from != want {
		t.Fatalf("historical from = %d, want %d", from, want)
	}
	if to != historicalTo {
		t.Fatalf("historical to = %d, want %d", to, historicalTo)
	}

	from, _, empty = boundedCandleTradeWindow(resolutionMS[Resolution1m], 5, 0, 100_000, nowMS)
	if empty {
		t.Fatal("early historical window unexpectedly reported empty")
	}
	if from != 1 {
		t.Fatalf("early historical from = %d, want 1", from)
	}

	const maxInt64 = int64(1<<63 - 1)
	from, to, empty = boundedCandleTradeWindow(resolutionMS[Resolution1m], 5, maxInt64, maxInt64, nowMS)
	if !empty {
		t.Fatalf("overflowing future window = (%d, %d), want empty", from, to)
	}
	if to != nowMS {
		t.Fatalf("overflowing future to = %d, want safe anchor %d", to, nowMS)
	}
}

func TestCandleAggregationCapacityFailsFast(t *testing.T) {
	for range maxCandleAggregations {
		candleAggregationSlots <- struct{}{}
	}
	defer func() {
		for range maxCandleAggregations {
			<-candleAggregationSlots
		}
	}()

	_, err := (&Repository{}).aggregateFromTrades(context.Background(), "tok-1", Resolution1m, 1, 0, 0)
	if !errors.Is(err, errCandleAggregationBusy) {
		t.Fatalf("err = %v, want %v", err, errCandleAggregationBusy)
	}
}

func TestCandleAggregationSkipsOverflowingFutureWindow(t *testing.T) {
	const maxInt64 = int64(1<<63 - 1)
	got, err := (&Repository{}).aggregateFromTrades(
		context.Background(),
		"tok-1",
		Resolution1m,
		1,
		maxInt64,
		maxInt64,
	)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Fatalf("len = %d, want 0", len(got))
	}
}

func TestGetCandles_NilPoolReturnsEmpty(t *testing.T) {
	svc := NewService(NewRepository(nil))
	got, err := svc.GetCandles(context.Background(), "tok-1", Resolution1h, 100, 0, 0)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestGetStats_NilPoolReturnsZeroValueStats(t *testing.T) {
	svc := NewService(NewRepository(nil))
	addr := "0xdead"
	got, err := svc.GetStats(context.Background(), "tok-1", &addr)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got.TokenID != "tok-1" {
		t.Errorf("tokenId = %q, want tok-1", got.TokenID)
	}
	if got.Address == nil || *got.Address != addr {
		t.Errorf("address = %v, want %q", got.Address, addr)
	}
	if got.LatestPrice != nil {
		t.Errorf("latestPrice = %v, want nil", *got.LatestPrice)
	}
	if got.Volume24h != "0" {
		t.Errorf("volume24h = %q, want \"0\"", got.Volume24h)
	}
	if got.PriceChange24h != 0 {
		t.Errorf("priceChange24h = %v, want 0", got.PriceChange24h)
	}
}

func TestHandler_Candles404OnDegradedMode(t *testing.T) {
	// Address lookup fails first (404), so /candles never reaches the
	// candle layer — matches the controller's two-step lookup contract.
	svc := NewService(NewRepository(nil))
	mux := Router(svc, nil)
	req := httptest.NewRequest(http.MethodGet, "/address/0xabc/candles?resolution=15m&limit=5", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "token not found") {
		t.Errorf("body = %q, want 'token not found'", rec.Body.String())
	}
}

func TestHandler_Stats404OnDegradedMode(t *testing.T) {
	svc := NewService(NewRepository(nil))
	mux := Router(svc, nil)
	req := httptest.NewRequest(http.MethodGet, "/address/0xabc/stats", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestStatsJSONShapeWithNilLatestPriceSerializesAsNull(t *testing.T) {
	// LatestPrice is *string so a degraded-mode response must serialize
	// `"latestPrice": null` to match the NestJS shape (`?? null`). A
	// regression to "" would break the FE's null-guard in the price card.
	s := Stats{
		TokenID:        "tok-1",
		Address:        nil,
		LatestPrice:    nil,
		Volume24h:      "0",
		PriceChange24h: 0,
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"latestPrice":null`) {
		t.Errorf("body = %s, want latestPrice:null", string(raw))
	}
	if !strings.Contains(string(raw), `"address":null`) {
		t.Errorf("body = %s, want address:null", string(raw))
	}
}
