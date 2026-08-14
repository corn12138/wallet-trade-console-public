package pricefeed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestParseResolutionRejectsUnknownInsteadOfCoercing(t *testing.T) {
	for _, ok := range SupportedResolutions() {
		if got, err := ParseResolution(string(ok)); err != nil || got != ok {
			t.Errorf("ParseResolution(%q) = (%q, %v)", ok, got, err)
		}
	}
	// The trading package coerces unknown values to a default; this one must
	// not, or a client would silently get a different bucket width than asked.
	for _, bad := range []string{"", "2m", "1w", "abc", "1M"} {
		if _, err := ParseResolution(bad); err == nil {
			t.Errorf("ParseResolution(%q) should have failed", bad)
		}
	}
}

func TestBucketStartIsEpochAnchoredUTC(t *testing.T) {
	// 2026-08-06T13:47:31Z
	ts := time.Date(2026, 8, 6, 13, 47, 31, 500_000_000, time.UTC)

	cases := []struct {
		res  Resolution
		want time.Time
	}{
		{Res1m, time.Date(2026, 8, 6, 13, 47, 0, 0, time.UTC)},
		{Res5m, time.Date(2026, 8, 6, 13, 45, 0, 0, time.UTC)},
		{Res15m, time.Date(2026, 8, 6, 13, 45, 0, 0, time.UTC)},
		{Res1h, time.Date(2026, 8, 6, 13, 0, 0, 0, time.UTC)},
		{Res4h, time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)},
		{Res1d, time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC)},
	}
	for _, tc := range cases {
		if got := tc.res.BucketStart(ts); !got.Equal(tc.want) {
			t.Errorf("%s: want %s, got %s", tc.res, tc.want, got)
		}
	}
}

func TestBucketStartNormalizesNonUTCInput(t *testing.T) {
	// Same instant expressed in a +08:00 zone must land in the same bucket, or
	// a worker running with a local TZ would write shifted candles.
	zone := time.FixedZone("UTC+8", 8*3600)
	utc := time.Date(2026, 8, 6, 13, 47, 31, 0, time.UTC)
	local := utc.In(zone)

	if a, b := Res1h.BucketStart(utc), Res1h.BucketStart(local); !a.Equal(b) {
		t.Errorf("bucket differs by zone: %s vs %s", a, b)
	}
}

func TestSortCandlesDescNewestFirst(t *testing.T) {
	candles := []Candle{{Timestamp: 100}, {Timestamp: 300}, {Timestamp: 200}}
	SortCandlesDesc(candles)
	for i := 1; i < len(candles); i++ {
		if candles[i-1].Timestamp < candles[i].Timestamp {
			t.Fatalf("not descending: %v", candles)
		}
	}
}

func TestNormalizeSymbolDoesNotRewriteSeparators(t *testing.T) {
	if got := NormalizeSymbol("  eth-usd "); got != "ETH-USD" {
		t.Errorf("want ETH-USD, got %q", got)
	}
	// ETH_USD is a venue pair, not the product's canonical symbol; silently
	// remapping it would resolve a caller's bug into the wrong market.
	if got := NormalizeSymbol("eth_usd"); got != "ETH_USD" {
		t.Errorf("want ETH_USD unchanged, got %q", got)
	}
}

func TestParseSourceDefaultsToMarketAndRejectsUnknown(t *testing.T) {
	if s, ok := parseSource(""); !ok || s != SourceMarket {
		t.Errorf("empty source should default to market, got (%q, %v)", s, ok)
	}
	if s, ok := parseSource("ORACLE"); !ok || s != SourceOracle {
		t.Errorf("oracle should parse case-insensitively, got (%q, %v)", s, ok)
	}
	// "onchain" is served by the trading package, not here — accepting it
	// would promise data this endpoint never returns.
	for _, bad := range []string{"onchain", "binance", "made-up"} {
		if _, ok := parseSource(bad); ok {
			t.Errorf("source %q should be rejected", bad)
		}
	}
}

func TestConfigFromEnvDefaults(t *testing.T) {
	t.Setenv("PRICEFEED_ENABLED", "true")
	cfg := ConfigFromEnv()

	if !cfg.Enabled {
		t.Error("PRICEFEED_ENABLED=true should enable the worker")
	}
	if len(cfg.Symbols) != 2 {
		t.Errorf("want the 2 default symbols, got %v", cfg.Symbols)
	}
	if len(cfg.Resolutions) != len(SupportedResolutions()) {
		t.Errorf("want all resolutions by default, got %v", cfg.Resolutions)
	}
	if cfg.MarketInterval != time.Minute {
		t.Errorf("market interval default: got %s", cfg.MarketInterval)
	}
	// Feeds are derived from symbols, so both default markets get an oracle.
	if len(cfg.Feeds) != 2 {
		t.Errorf("want 2 derived feeds, got %d", len(cfg.Feeds))
	}
}

func TestConfigDisabledByDefault(t *testing.T) {
	t.Setenv("PRICEFEED_ENABLED", "")
	if ConfigFromEnv().Enabled {
		t.Error("the price feed must be opt-in, not on by default")
	}
}

func TestConfigSymbolsFilterDerivedFeeds(t *testing.T) {
	t.Setenv("PRICEFEED_ENABLED", "true")
	t.Setenv("PRICEFEED_SYMBOLS", "ETH-USD")
	cfg := ConfigFromEnv()

	if len(cfg.Feeds) != 1 || cfg.Feeds[0].Symbol != "ETH-USD" {
		t.Errorf("feeds should follow PRICEFEED_SYMBOLS, got %+v", cfg.Feeds)
	}
}

func TestConfigDropsUnknownResolutionsRatherThanCoercing(t *testing.T) {
	t.Setenv("PRICEFEED_ENABLED", "true")
	t.Setenv("PRICEFEED_RESOLUTIONS", "1m,2m,1h")
	cfg := ConfigFromEnv()

	if len(cfg.Resolutions) != 2 {
		t.Fatalf("want 1m + 1h, got %v", cfg.Resolutions)
	}
	if cfg.Resolutions[0] != Res1m || cfg.Resolutions[1] != Res1h {
		t.Errorf("unexpected resolutions %v", cfg.Resolutions)
	}
}

// A nil pool must degrade to an honest "unavailable", never to a silent empty
// series that looks like "this market has no data".
func TestServiceWithoutDatabaseReportsUnavailable(t *testing.T) {
	svc := NewService(NewStore(nil), "gateio", func() time.Time { return time.Unix(0, 0) })

	for _, source := range []Source{SourceMarket, SourceOracle} {
		series, err := svc.Candles(context.Background(), source, "ETH-USD", Res1m, 10)
		if err != nil {
			t.Fatalf("%s: unexpected error %v", source, err)
		}
		if series.Status != StatusUnavailable {
			t.Errorf("%s: want status %q, got %q", source, StatusUnavailable, series.Status)
		}
		if series.Candles == nil {
			t.Errorf("%s: candles must be an empty array, never null", source)
		}
		if series.Source != string(source) {
			t.Errorf("%s: response must echo its source, got %q", source, series.Source)
		}
	}

	spot, err := svc.SpotPrice(context.Background(), SourceOracle, "ETH-USD")
	if err != nil {
		t.Fatalf("SpotPrice: %v", err)
	}
	if spot.Status != StatusUnavailable {
		t.Errorf("spot: want %q, got %q", StatusUnavailable, spot.Status)
	}
}

func TestServiceRejectsUnknownSource(t *testing.T) {
	svc := NewService(NewStore(nil), "gateio", nil)
	if _, err := svc.Candles(context.Background(), Source("nope"), "ETH-USD", Res1m, 10); err == nil {
		t.Fatal("unknown source must error")
	}
}

func TestHandlerValidatesInput(t *testing.T) {
	svc := NewService(NewStore(nil), "gateio", nil)
	router := Router(svc)

	cases := []struct {
		name  string
		query string
		want  int
	}{
		{"missing symbol", "/candles?resolution=1m", http.StatusBadRequest},
		{"bad resolution", "/candles?symbol=ETH-USD&resolution=2m", http.StatusBadRequest},
		{"bad source", "/candles?symbol=ETH-USD&source=binance", http.StatusBadRequest},
		{"bad limit", "/candles?symbol=ETH-USD&limit=-5", http.StatusBadRequest},
		{"valid", "/candles?symbol=ETH-USD&resolution=1m&limit=10", http.StatusOK},
		{"spot needs symbols", "/spot", http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.query, nil))
			if rec.Code != tc.want {
				t.Errorf("want %d, got %d (%s)", tc.want, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestSourcesEndpointDocumentsVolumeHonestly(t *testing.T) {
	router := Router(NewService(NewStore(nil), "gateio", nil))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sources", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
	var body struct {
		Sources []struct {
			ID        string `json:"id"`
			HasVolume bool   `json:"hasVolume"`
		} `json:"sources"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	byID := map[string]bool{}
	for _, s := range body.Sources {
		byID[s.ID] = s.HasVolume
	}
	// An oracle reports a price, not traded size — claiming volume here is the
	// exact kind of invented number this package exists to avoid.
	if byID[string(SourceOracle)] {
		t.Error("oracle source must not claim to carry volume")
	}
	if !byID[string(SourceMarket)] {
		t.Error("market source should report that it carries volume")
	}
}
