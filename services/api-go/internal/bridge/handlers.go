package bridge

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

var (
	addressRE  = regexp.MustCompile(`^0x[a-fA-F0-9]{40}$`)
	transferRE = regexp.MustCompile(`^0x[a-fA-F0-9]{64}$`)
)

// Router mounts /api/bridge.
//
// Reads are PUBLIC (route availability and transfer state are public chain
// data, and the page loads them pre-login) and so is build-deposit — it returns
// unsigned calldata the user's own wallet must sign, exactly like /api/swap's
// builders. Nothing here moves funds or holds a key.
func Router(svc *Service) chi.Router {
	r := chi.NewRouter()
	r.Post("/routes", svc.handleRoutes)
	r.Post("/build-deposit", svc.handleBuildDeposit)
	r.Get("/status/{transferId}", svc.handleStatus)
	r.Get("/transfers", svc.handleTransfers)
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

func (s *Service) handleRoutes(w http.ResponseWriter, r *http.Request) {
	var body RoutesRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	resp, err := s.GetRoutes(r.Context(), body)
	if errors.Is(err, ErrInvalidAmount) {
		writeErr(w, http.StatusBadRequest, ErrInvalidAmount.Error())
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "failed to resolve bridge routes")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Service) handleBuildDeposit(w http.ResponseWriter, r *http.Request) {
	var body BuildDepositRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	resp, err := s.BuildDeposit(r.Context(), body)
	switch {
	case errors.Is(err, ErrInvalidAmount):
		writeErr(w, http.StatusBadRequest, ErrInvalidAmount.Error())
	case errors.Is(err, ErrRouteUnavailable):
		// 409, not 400: the request is well-formed, the ROUTE is not currently
		// executable. The client should re-read /routes for the blocker list.
		writeErr(w, http.StatusConflict, ErrRouteUnavailable.Error())
	case err != nil:
		writeErr(w, http.StatusBadRequest, err.Error())
	default:
		writeJSON(w, http.StatusOK, resp)
	}
}

func (s *Service) handleStatus(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "transferId"))
	if !transferRE.MatchString(id) {
		writeErr(w, http.StatusBadRequest, "transferId must be a 0x-prefixed 32-byte hex id")
		return
	}
	resp, err := s.GetStatus(r.Context(), id)
	switch {
	case errors.Is(err, ErrNoTransfer):
		writeErr(w, http.StatusNotFound, ErrNoTransfer.Error())
	case errors.Is(err, ErrStoreUnavailable):
		// 503, not 404: "we cannot look" must never read as "it does not exist".
		writeErr(w, http.StatusServiceUnavailable, ErrStoreUnavailable.Error())
	case err != nil:
		writeErr(w, http.StatusInternalServerError, "failed to read transfer status")
	default:
		writeJSON(w, http.StatusOK, resp)
	}
}

func (s *Service) handleTransfers(w http.ResponseWriter, r *http.Request) {
	address := strings.TrimSpace(r.URL.Query().Get("address"))
	if !addressRE.MatchString(address) {
		writeErr(w, http.StatusBadRequest, "address must be a 0x-prefixed address")
		return
	}
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			writeErr(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = n
	}

	rows, err := s.ListTransfers(r.Context(), address, limit)
	switch {
	case errors.Is(err, ErrStoreUnavailable):
		writeErr(w, http.StatusServiceUnavailable, ErrStoreUnavailable.Error())
	case err != nil:
		writeErr(w, http.StatusInternalServerError, "failed to list transfers")
	default:
		writeJSON(w, http.StatusOK, rows)
	}
}
