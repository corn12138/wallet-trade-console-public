package ai

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/txreview"
	"github.com/go-chi/chi/v5"
)

// ExplainRequest is the wire input for POST /api/security/tx-review/explain.
//
// It carries a ReviewInput — the SAME body /tx-review takes — not a finished
// review. The TD sketched this endpoint as accepting the client's Result, and
// that shape has two problems worth the extra RPC round trip to avoid: it lets
// any authenticated caller push arbitrary text through a paid model call, and
// it means the prose could describe a verdict the server never computed. Here
// the server re-derives the review it is about to explain, so the model only
// ever sees text this server produced.
type ExplainRequest struct {
	txreview.ReviewInput
	Locale string `json:"locale,omitempty"`
}

// ExplainResponse returns the review alongside the explanation. The review is
// the authority; the explanation is presentation and may be null.
type ExplainResponse struct {
	Review txreview.Result `json:"review"`
	Result
}

// RegisterGuarded is the registrar form, matching txreview.RegisterGuarded:
// chi rejects two Mount() calls on the same prefix, so the /security mount
// fans in this route the same way it fans in /tx-review.
func RegisterGuarded(svc *Service) func(r chi.Router) {
	return func(r chi.Router) {
		r.Post("/tx-review/explain", makeExplainHandler(svc))
	}
}

func makeExplainHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req ExplainRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		// Same owner-pinning as /tx-review: a caller cannot review — or have
		// explained — a transaction as another wallet.
		owner, err := auth.ResolveOwner(auth.AddressFromContext(r.Context()), req.FromAddress)
		if err != nil {
			mapResolveOwnerErr(w, err)
			return
		}
		req.FromAddress = owner

		if svc == nil || svc.reviewer == nil {
			writeJSONError(w, http.StatusInternalServerError, "tx-review is not configured")
			return
		}
		review, err := svc.reviewer.ReviewTransaction(r.Context(), req.ReviewInput)
		if errors.Is(err, txreview.ErrInvalidInput) {
			writeJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err != nil {
			slog.ErrorContext(r.Context(), "ai: underlying review failed", "err", err)
			writeJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}

		// ExplainReview never errors — a missing explanation is a 200 with
		// source="static", because the review alone is a complete answer.
		writeJSON(w, http.StatusOK, ExplainResponse{
			Review: review,
			Result: svc.ExplainReview(r.Context(), review, req.Locale),
		})
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"statusCode": status,
		"message":    message,
	})
}

// mapResolveOwnerErr mirrors txreview's mapping so both routes answer an auth
// problem identically (401 missing / 403 mismatch / 400 malformed).
func mapResolveOwnerErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrMissingAuthenticated):
		writeJSONError(w, http.StatusUnauthorized, err.Error())
	case errors.Is(err, auth.ErrOwnerMismatch):
		writeJSONError(w, http.StatusForbidden, err.Error())
	default:
		writeJSONError(w, http.StatusBadRequest, err.Error())
	}
}
