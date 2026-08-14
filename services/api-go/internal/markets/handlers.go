package markets

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/trading"
	"github.com/go-chi/chi/v5"
)

// symbolRegex matches the NestJS SYMBOL_RE in markets.controller.ts.
// Allowed: uppercase alphanumerics + `. _ : / -`. Anchored both ends.
var symbolRegex = regexp.MustCompile(`^[A-Z0-9._:/\-]+$`)

const maxSymbolLength = 64

// Router exposes the markets endpoints on a chi sub-router so the
// caller can mount it at any prefix.
func Router(svc *Service) chi.Router {
	r := chi.NewRouter()
	r.Get("/symbols", makeSymbolsHandler(svc))
	r.Get("/snapshot", makeSnapshotHandler(svc))
	r.Get("/snapshot/{symbol}", makeSymbolSnapshotHandler(svc))
	return r
}

func makeSymbolsHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		chainID := parseChainIDQuery(r.URL.Query().Get("chainId"))
		symbols, err := svc.GetSymbols(chainID)
		if err != nil {
			slog.ErrorContext(r.Context(), "markets/symbols failed", "err", err)
			http.Error(w, "failed to load markets", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, symbols)
	}
}

func makeSnapshotHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		chainID := parseChainIDQuery(r.URL.Query().Get("chainId"))
		snapshot, err := svc.GetSnapshot(r.Context(), chainID)
		if err != nil {
			slog.ErrorContext(r.Context(), "markets/snapshot failed", "err", err)
			http.Error(w, "failed to load snapshot", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, snapshot)
	}
}

func makeSymbolSnapshotHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw := chi.URLParam(r, "symbol")
		normalized := strings.ToUpper(strings.TrimSpace(raw))
		if !isValidSymbol(normalized) {
			http.Error(w, "symbol must be a short alphanumeric identifier", http.StatusBadRequest)
			return
		}
		chainID := parseChainIDQuery(r.URL.Query().Get("chainId"))
		snapshot, err := svc.GetRealtimeSnapshot(r.Context(), trading.NormalizeSymbol(normalized), chainID)
		if err != nil {
			slog.ErrorContext(r.Context(), "markets/snapshot/{symbol} failed", "err", err)
			http.Error(w, "failed to load realtime snapshot", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, snapshot)
	}
}

// isValidSymbol mirrors the controller-level guard in
// markets.controller.ts: non-empty, length ≤ 64, regex-matched.
func isValidSymbol(normalized string) bool {
	if normalized == "" {
		return false
	}
	if len(normalized) > maxSymbolLength {
		return false
	}
	return symbolRegex.MatchString(normalized)
}

// parseChainIDQuery mirrors `parseNumberParam` + the JS `chainId ? … : true`
// truthy coercion from markets.controller.ts:
//   - empty                  → nil ("no filter")
//   - non-numeric            → nil too (don't 400 — old clients send "undefined")
//   - "0"                    → nil ("no filter", matches JS falsy coercion)
//   - other numeric          → int pointer
func parseChainIDQuery(raw string) *int {
	if raw == "" {
		return nil
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil {
		return nil
	}
	if parsed == 0 {
		return nil
	}
	return &parsed
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Error("markets response encode failed", "err", err)
	}
}
