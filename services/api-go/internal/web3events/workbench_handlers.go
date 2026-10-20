package web3events

import (
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// WithWorkbench enables the owner-scoped action/attempt reads. Without a
// reader the routes stay mounted and answer 503, never an empty list.
func (s *Service) WithWorkbench(reader WorkbenchReader) *Service {
	s.workbench = reader
	return s
}

func privateHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Add("Vary", "Authorization")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
}

func writeWorkbenchJSON(w http.ResponseWriter, payload any) {
	privateHeaders(w)
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Error("web3events workbench encode failed", "err", err)
	}
}

func writeWorkbenchError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	privateHeaders(w)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"statusCode": status,
		"code":       code,
		"message":    message,
		"requestId":  middleware.GetReqID(r.Context()),
		"retryable":  status == http.StatusServiceUnavailable,
	})
}

// workbenchOwner is the verified SIWE wallet; the routes have no owner input.
func (s *Service) workbenchOwner(w http.ResponseWriter, r *http.Request) (string, bool) {
	owner, err := auth.ResolveOwner(auth.AddressFromContext(r.Context()), "")
	if err != nil {
		writeWorkbenchError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "web3 authorization required")
		return "", false
	}
	return owner, true
}

// requiredChainID refuses an absent chain instead of defaulting it: a read
// may never be widened or silently moved to another chain.
func requiredChainID(raw string) (int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw != strings.TrimLeft(raw, "+-") {
		return 0, false
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v <= 0 || v > math.MaxInt32 || strconv.FormatInt(v, 10) != raw {
		return 0, false
	}
	return int(v), true
}

func workbenchLimit(raw string) (int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return defaultWorkbenchLimit, true
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 1 || v > maxWorkbenchLimit || strconv.Itoa(v) != raw {
		return 0, false
	}
	return v, true
}

type workbenchRequest struct {
	owner   string
	chainID int
	limit   int
}

func (s *Service) workbenchRequest(w http.ResponseWriter, r *http.Request) (workbenchRequest, bool) {
	owner, ok := s.workbenchOwner(w, r)
	if !ok {
		return workbenchRequest{}, false
	}
	// r.URL.Query() silently drops a malformed pair, which would turn a
	// broken cursor into a first-page read.
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeWorkbenchError(w, r, http.StatusBadRequest, "INVALID_QUERY", "malformed query string")
		return workbenchRequest{}, false
	}
	chainID, ok := requiredChainID(q.Get("chainId"))
	if !ok || len(q["chainId"]) != 1 {
		writeWorkbenchError(w, r, http.StatusBadRequest, "INVALID_CHAIN_ID", "chainId must be one positive integer")
		return workbenchRequest{}, false
	}
	limit, ok := workbenchLimit(q.Get("limit"))
	if !ok || len(q["limit"]) > 1 {
		writeWorkbenchError(w, r, http.StatusBadRequest, "INVALID_QUERY", "limit must be an integer from 1 to 100")
		return workbenchRequest{}, false
	}
	if len(q["cursor"]) > 1 {
		writeWorkbenchError(w, r, http.StatusBadRequest, "INVALID_CURSOR", "invalid cursor")
		return workbenchRequest{}, false
	}
	return workbenchRequest{owner: owner, chainID: chainID, limit: limit}, true
}

func (s *Service) unavailable(w http.ResponseWriter, r *http.Request, err error) {
	if err != nil && !errors.Is(err, ErrPoolUnavailable) {
		slog.ErrorContext(r.Context(), "web3events workbench read failed", "err", err)
	}
	writeWorkbenchError(w, r, http.StatusServiceUnavailable, "DATA_SOURCE_UNAVAILABLE", "action history is temporarily unavailable")
}

func notFound(w http.ResponseWriter, r *http.Request) {
	writeWorkbenchError(w, r, http.StatusNotFound, "ACTION_NOT_FOUND", "action not found")
}

func limitations() []string { return append([]string(nil), workbenchLimitations...) }

// listActions is GET /web3-events/actions.
func (s *Service) listActions(w http.ResponseWriter, r *http.Request) {
	req, ok := s.workbenchRequest(w, r)
	if !ok {
		return
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if _, known := actionStatusFilters[status]; status != "" && !known || len(r.URL.Query()["status"]) > 1 {
		writeWorkbenchError(w, r, http.StatusBadRequest, "INVALID_QUERY", "unknown status filter")
		return
	}
	var cursor *ActionCursorState
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		decoded, err := DecodeActionCursor(raw, req.owner, req.chainID, status)
		if err != nil {
			writeWorkbenchError(w, r, http.StatusBadRequest, "INVALID_CURSOR", "invalid cursor")
			return
		}
		cursor = &decoded
	}
	if s.workbench == nil {
		s.unavailable(w, r, nil)
		return
	}
	page, err := s.workbench.ListActions(r.Context(), ActionListQuery{Owner: req.owner, ChainID: req.chainID, Status: status, Limit: req.limit, Cursor: cursor})
	if err != nil {
		s.unavailable(w, r, err)
		return
	}
	out := ActionListResponse{Items: make([]ActionView, 0, len(page.Rows)), HasMore: page.HasMore, HasUpdates: page.HasUpdates, AsOf: wireTime(page.AsOf), Limitations: limitations()}
	for _, row := range page.Rows {
		out.Items = append(out.Items, NewActionView(row))
	}
	if page.HasMore && len(page.Rows) > 0 {
		state := ActionCursorState{AsOf: page.AsOf, HighWater: ActionKey{page.Rows[0].CreatedAt, page.Rows[0].ID}}
		if cursor != nil {
			state.AsOf, state.HighWater = cursor.AsOf, cursor.HighWater
		}
		last := page.Rows[len(page.Rows)-1]
		state.After = ActionKey{last.CreatedAt, last.ID}
		next := EncodeActionCursor(req.owner, req.chainID, status, state)
		out.NextCursor = &next
	}
	writeWorkbenchJSON(w, out)
}

func pathActionID(r *http.Request) (string, bool) {
	id := chi.URLParam(r, "actionId")
	return id, actionIDRE.MatchString(id)
}

// getAction is GET /web3-events/actions/{actionId}. Absent, another owner's,
// another chain's and malformed IDs are indistinguishable.
func (s *Service) getAction(w http.ResponseWriter, r *http.Request) {
	req, ok := s.workbenchRequest(w, r)
	if !ok {
		return
	}
	actionID, valid := pathActionID(r)
	if !valid {
		notFound(w, r)
		return
	}
	if s.workbench == nil {
		s.unavailable(w, r, nil)
		return
	}
	row, found, asOf, err := s.workbench.GetAction(r.Context(), req.owner, req.chainID, actionID)
	if err != nil {
		s.unavailable(w, r, err)
		return
	}
	if !found {
		notFound(w, r)
		return
	}
	writeWorkbenchJSON(w, ActionDetailResponse{Item: NewActionView(row), AsOf: wireTime(asOf), Limitations: limitations()})
}

// listAttempts is GET /web3-events/actions/{actionId}/attempts.
func (s *Service) listAttempts(w http.ResponseWriter, r *http.Request) {
	req, ok := s.workbenchRequest(w, r)
	if !ok {
		return
	}
	actionID, valid := pathActionID(r)
	if !valid {
		notFound(w, r)
		return
	}
	var after *AttemptKey
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		decoded, err := DecodeAttemptCursor(raw, req.owner, req.chainID, actionID)
		if err != nil {
			writeWorkbenchError(w, r, http.StatusBadRequest, "INVALID_CURSOR", "invalid cursor")
			return
		}
		after = &decoded
	}
	if s.workbench == nil {
		s.unavailable(w, r, nil)
		return
	}
	page, found, err := s.workbench.ListAttempts(r.Context(), AttemptListQuery{Owner: req.owner, ChainID: req.chainID, ActionID: actionID, Limit: req.limit, After: after})
	if err != nil {
		s.unavailable(w, r, err)
		return
	}
	if !found {
		notFound(w, r)
		return
	}
	v2 := false
	for _, token := range strings.Split(r.Header.Get("X-Workbench-Variants"), ",") {
		if strings.TrimSpace(token) == WorkbenchVariantContextV2 {
			v2 = true
		}
	}
	w.Header().Add("Vary", "X-Workbench-Variants")
	out := AttemptListResponse{Items: make([]AttemptView, 0, len(page.Rows)), HasMore: page.HasMore, AsOf: wireTime(page.AsOf), Limitations: limitations()}
	for _, row := range page.Rows {
		out.Items = append(out.Items, NewAttemptViewFor(row, v2))
	}
	if page.HasMore && len(page.Rows) > 0 {
		last := page.Rows[len(page.Rows)-1]
		next := EncodeAttemptCursor(req.owner, req.chainID, actionID, AttemptKey{Number: last.AttemptNumber, ID: last.ID})
		out.NextCursor = &next
	}
	writeWorkbenchJSON(w, out)
}
