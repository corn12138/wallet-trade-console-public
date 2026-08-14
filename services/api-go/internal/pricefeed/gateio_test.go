package pricefeed

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

// gateioSample is a verbatim two-row response from the live endpoint. Keeping a
// real payload here is the point: Gate.io's row order is NOT OHLC, and a
// hand-written fixture would happily agree with a wrong decoder.
//
// Row layout: [ts, quoteVol, close, high, low, open, baseVol, closed]
const gateioSample = `[
 ["1786024800","281332.75616300","1906.06","1907.88","1905.25","1907.67","147.52230000","true"],
 ["1786024860","163422.57211900","1906.46","1907.67","1905.88","1905.95","85.11000000","false"]
]`

func newGateioTestServer(t *testing.T, body string, status int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != "/spot/candlesticks" {
			t.Errorf("unexpected path %q", got)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

func TestGateioFetchCandlesMapsFieldsPositionally(t *testing.T) {
	srv := newGateioTestServer(t, gateioSample, http.StatusOK)
	defer srv.Close()

	p := NewGateioProvider(srv.URL, nil, 5*time.Second)
	candles, err := p.FetchCandles(context.Background(), "ETH-USD", Res1m, 2)
	if err != nil {
		t.Fatalf("FetchCandles: %v", err)
	}
	if len(candles) != 2 {
		t.Fatalf("want 2 candles, got %d", len(candles))
	}

	first := candles[0]
	if first.Timestamp != 1786024800*1000 {
		t.Errorf("timestamp: want %d, got %d", 1786024800*1000, first.Timestamp)
	}
	// The whole point of the fixture: open is column 5, close column 2,
	// high column 3, low column 4.
	if first.Open != "1907.67" {
		t.Errorf("open: want 1907.67, got %s", first.Open)
	}
	if first.High != "1907.88" {
		t.Errorf("high: want 1907.88, got %s", first.High)
	}
	if first.Low != "1905.25" {
		t.Errorf("low: want 1905.25, got %s", first.Low)
	}
	if first.Close != "1906.06" {
		t.Errorf("close: want 1906.06, got %s", first.Close)
	}
	if first.Volume != "147.52230000" {
		t.Errorf("base volume: want 147.52230000, got %s", first.Volume)
	}
	if first.QuoteVolume != "281332.75616300" {
		t.Errorf("quote volume: want 281332.75616300, got %s", first.QuoteVolume)
	}
	if !first.Closed {
		t.Error("first candle should be closed")
	}
	if candles[1].Closed {
		t.Error("second candle is still forming and must not be marked closed")
	}
}

func TestGateioSanityHighLowBracketOpenClose(t *testing.T) {
	srv := newGateioTestServer(t, gateioSample, http.StatusOK)
	defer srv.Close()

	p := NewGateioProvider(srv.URL, nil, 5*time.Second)
	candles, err := p.FetchCandles(context.Background(), "ETH-USD", Res1m, 2)
	if err != nil {
		t.Fatalf("FetchCandles: %v", err)
	}
	// A mis-ordered decode almost always breaks this invariant, so it guards
	// the mapping independently of the exact fixture values.
	for i, c := range candles {
		high, low := mustFloat(t, c.High), mustFloat(t, c.Low)
		open, closeP := mustFloat(t, c.Open), mustFloat(t, c.Close)
		if high < low {
			t.Errorf("candle %d: high %v < low %v", i, high, low)
		}
		if open > high || open < low {
			t.Errorf("candle %d: open %v outside [%v, %v]", i, open, low, high)
		}
		if closeP > high || closeP < low {
			t.Errorf("candle %d: close %v outside [%v, %v]", i, closeP, low, high)
		}
	}
}

func TestGateioUnsupportedSymbol(t *testing.T) {
	p := NewGateioProvider("http://unused.invalid", nil, time.Second)
	if p.Supports("DOGE-USD") {
		t.Fatal("DOGE-USD should be unsupported")
	}
	_, err := p.FetchCandles(context.Background(), "DOGE-USD", Res1m, 10)
	if !errors.Is(err, ErrUnsupportedSymbol) {
		t.Fatalf("want ErrUnsupportedSymbol, got %v", err)
	}
}

func TestGateioNonOKStatusIsAnError(t *testing.T) {
	srv := newGateioTestServer(t, `{"label":"TOO_MANY_REQUESTS"}`, http.StatusTooManyRequests)
	defer srv.Close()

	p := NewGateioProvider(srv.URL, nil, 5*time.Second)
	if _, err := p.FetchCandles(context.Background(), "ETH-USD", Res1m, 10); err == nil {
		t.Fatal("a 429 must surface as an error, never as an empty-but-successful series")
	}
}

func TestGateioShortRowIsAnError(t *testing.T) {
	srv := newGateioTestServer(t, `[["1786024800","1","2"]]`, http.StatusOK)
	defer srv.Close()

	p := NewGateioProvider(srv.URL, nil, 5*time.Second)
	if _, err := p.FetchCandles(context.Background(), "ETH-USD", Res1m, 10); err == nil {
		t.Fatal("a truncated row must fail loudly rather than fabricate a gap")
	}
}

func mustFloat(t *testing.T, s string) float64 {
	t.Helper()
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return f
}
