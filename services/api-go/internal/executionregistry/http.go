package executionregistry

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

const maxCheckBody = 8 << 10

// Router mounts GET /current and POST /check (public, no-store). A nil
// service keeps the topology and answers 503.
func Router(svc *Service) chi.Router {
	r := chi.NewRouter()
	r.Get("/current", svc.current)
	r.Post("/check", svc.check)
	return r
}

func headers(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, reason, message string, extra map[string]any) {
	headers(w)
	w.WriteHeader(status)
	body := map[string]any{"statusCode": status, "code": code, "message": message,
		"requestId": middleware.GetReqID(r.Context()), "retryable": status == http.StatusServiceUnavailable}
	if reason != "" {
		body["reason"] = reason
	}
	for k, v := range extra {
		body[k] = v
	}
	_ = json.NewEncoder(w).Encode(body)
}

func (s *Service) current(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("chainId")
	chainID, err := strconv.Atoi(raw)
	if err != nil || chainID <= 0 || strconv.Itoa(chainID) != raw || len(r.URL.Query()["chainId"]) != 1 {
		writeError(w, r, http.StatusBadRequest, "INVALID_CHAIN_ID", "", "chainId must be one positive integer", nil)
		return
	}
	if s == nil {
		writeError(w, r, http.StatusServiceUnavailable, "REGISTRY_UNAVAILABLE", ReasonAuthorityUnavailable, "execution configuration unavailable", nil)
		return
	}
	out, err := s.Current(r.Context(), chainID)
	if err != nil {
		if !errors.Is(err, ErrNoActiveRevision) {
			slog.WarnContext(r.Context(), "execution registry read failed", "err", err)
		}
		writeError(w, r, http.StatusServiceUnavailable, "REGISTRY_UNAVAILABLE", unavailableReason(err), "execution configuration unavailable", nil)
		return
	}
	headers(w)
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(out)
}

func (s *Service) check(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		writeError(w, r, http.StatusBadRequest, "INVALID_INPUT", "", "application/json is required", nil)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxCheckBody))
	var req CheckRequest
	if err != nil || decodeStrict(bytes.TrimSpace(raw), &req) != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_INPUT", "", "malformed or oversized check request", nil)
		return
	}
	if s == nil {
		writeError(w, r, http.StatusServiceUnavailable, "REGISTRY_UNAVAILABLE", ReasonAuthorityUnavailable, "execution configuration unavailable", nil)
		return
	}
	out := s.Check(r.Context(), req)
	extra := map[string]any{}
	if out.ActiveRevision > 0 {
		extra["activeRevision"] = strconv.FormatInt(out.ActiveRevision, 10)
		extra["activationVersion"] = strconv.FormatInt(out.ActivationVersion, 10)
	}
	if out.HTTPStatus == http.StatusOK {
		headers(w)
		w.WriteHeader(http.StatusOK)
		extra["ok"] = true
		_ = json.NewEncoder(w).Encode(extra)
		return
	}
	writeError(w, r, out.HTTPStatus, out.Code, out.Reason, "execution configuration check refused", extra)
}
