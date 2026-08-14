package pricefeed

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

// Router mounts /api/prices.
//
// Every route is a PUBLIC read: reference prices are public market data, the
// same class as /api/markets/snapshot and the token catalog reads, and the
// /trade chart loads them pre-login. Nothing here is user-scoped, so no guard
// is wired.
func Router(svc *Service) chi.Router {
	r := chi.NewRouter()
	r.Get("/candles", svc.handleCandles)
	r.Get("/spot", svc.handleSpot)
	r.Get("/coverage", svc.handleCoverage)
	r.Get("/sources", svc.handleSources)
	return r
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeErr(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{
		"statusCode": status,
		"message":    message,
		"source":     "api-go",
	})
}

// parseSource maps the query parameter onto a Source. Default is market: it is
// the dense series the chart wants, and asking for it by accident is harmless
// because the response always states which source it served.
func parseSource(raw string) (Source, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(SourceMarket):
		return SourceMarket, true
	case string(SourceOracle):
		return SourceOracle, true
	default:
		return "", false
	}
}

// GET /api/prices/candles?symbol=ETH-USD&resolution=1m&limit=200&source=market
func (s *Service) handleCandles(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	symbol := NormalizeSymbol(q.Get("symbol"))
	if symbol == "" {
		writeErr(w, http.StatusBadRequest, "symbol is required")
		return
	}
	res, err := ParseResolution(orDefault(q.Get("resolution"), string(Res1m)))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	source, ok := parseSource(q.Get("source"))
	if !ok {
		writeErr(w, http.StatusBadRequest, "source must be 'market' or 'oracle'")
		return
	}
	limit := 200
	if raw := strings.TrimSpace(q.Get("limit")); raw != "" {
		n, convErr := strconv.Atoi(raw)
		if convErr != nil || n <= 0 {
			writeErr(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = n
	}

	series, err := s.Candles(r.Context(), source, symbol, res, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to read candles")
		return
	}
	writeJSON(w, http.StatusOK, series)
}

// GET /api/prices/spot?symbols=ETH-USD,BTC-USD&source=market
func (s *Service) handleSpot(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	source, ok := parseSource(q.Get("source"))
	if !ok {
		writeErr(w, http.StatusBadRequest, "source must be 'market' or 'oracle'")
		return
	}

	raw := strings.TrimSpace(q.Get("symbols"))
	if raw == "" {
		raw = strings.TrimSpace(q.Get("symbol"))
	}
	if raw == "" {
		writeErr(w, http.StatusBadRequest, "symbols is required")
		return
	}

	out := []Spot{}
	for part := range strings.SplitSeq(raw, ",") {
		symbol := NormalizeSymbol(part)
		if symbol == "" {
			continue
		}
		spot, err := s.SpotPrice(r.Context(), source, symbol)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "failed to read spot price")
			return
		}
		out = append(out, spot)
	}
	writeJSON(w, http.StatusOK, out)
}

// GET /api/prices/coverage — what is actually stored, per source.
func (s *Service) handleCoverage(w http.ResponseWriter, r *http.Request) {
	coverage, err := s.Coverage(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to read coverage")
		return
	}
	writeJSON(w, http.StatusOK, coverage)
}

// GET /api/prices/sources — the source vocabulary and what each one means, so
// a client can render honest labels without hardcoding prose.
func (s *Service) handleSources(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"sources": []map[string]any{
			{
				"id":          string(SourceMarket),
				"provider":    s.provider,
				"kind":        "off-chain-spot-market",
				"hasVolume":   true,
				"description": "Real OHLCV from a public spot venue. Dense and volume-bearing; off-chain reference data, not this product's own trades.",
			},
			{
				"id":          string(SourceOracle),
				"kind":        "on-chain-oracle",
				"hasVolume":   false,
				"description": "Chainlink aggregator rounds read on-chain. Trust-minimized, but updates only on heartbeat/deviation and carries no traded volume.",
			},
			{
				"id":          string(SourceOnchain),
				"kind":        "product-onchain-trades",
				"hasVolume":   true,
				"description": "This product's own perp trades. Served by GET /api/trading/candles; empty until real trades are executed.",
			},
		},
		"resolutions": SupportedResolutions(),
	})
}

func orDefault(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}
