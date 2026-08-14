package mobiledoc

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
)

// successEnvelope mirrors legacy NestJS mobile/types/client.types.ts
// SuccessResponse<T> — { success, data, message?, traceId, timestamp }.
type successEnvelope struct {
	Success   bool   `json:"success"`
	Data      any    `json:"data"`
	Message   string `json:"message,omitempty"`
	TraceID   string `json:"traceId"`
	Timestamp string `json:"timestamp"`
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Error("mobiledoc response encode failed", "err", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"statusCode": status, "message": message})
}

func writeSuccess(w http.ResponseWriter, status int, data any, message string) {
	writeJSON(w, status, successEnvelope{
		Success:   true,
		Data:      data,
		Message:   message,
		TraceID:   traceID(),
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
	})
}

func traceID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// mapErrStatus turns a store error into (status, message) for the raw handlers.
func mapErrStatus(err error) (int, string) {
	switch {
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound, err.Error()
	case errors.Is(err, ErrPoolUnavailable):
		return http.StatusServiceUnavailable, "database unavailable"
	case errors.Is(err, ErrInvalidInput):
		return http.StatusBadRequest, err.Error()
	default:
		return http.StatusInternalServerError, "internal error"
	}
}

func decodeJSON(r *http.Request, dst any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	return dec.Decode(dst)
}

func parseQuery(r *http.Request) QueryParams {
	q := QueryParams{Page: 1, PageSize: 10, Published: true}
	v := r.URL.Query()
	if p, err := strconv.Atoi(v.Get("page")); err == nil && p >= 1 {
		q.Page = p
	}
	if ps, err := strconv.Atoi(v.Get("pageSize")); err == nil && ps >= 1 {
		if ps > 100 {
			ps = 100
		}
		q.PageSize = ps
	}
	q.Category = v.Get("category")
	q.Search = v.Get("search")
	q.Tag = v.Get("tag")
	if v.Has("isHot") {
		b := v.Get("isHot") == "true"
		q.IsHot = &b
	}
	if v.Has("published") {
		q.Published = v.Get("published") != "false"
	}
	return q
}

func limitParam(r *http.Request, def int) int {
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 {
		return n
	}
	return def
}

// ---- legacy MobileController (raw responses) -------------------------------

func (s *Service) legacyFindAll(w http.ResponseWriter, r *http.Request) {
	out, err := s.store.FindAll(r.Context(), parseQuery(r))
	if err != nil && !errors.Is(err, ErrPoolUnavailable) {
		slog.ErrorContext(r.Context(), "mobiledoc list failed", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to load docs")
		return
	}
	writeJSON(w, http.StatusOK, out) // degraded nil-pool → empty PaginatedResult
}

func (s *Service) legacyCreate(w http.ResponseWriter, r *http.Request) {
	var in CreateInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := in.validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	doc, err := s.store.Create(r.Context(), in)
	if err != nil {
		st, msg := mapErrStatus(err)
		writeError(w, st, msg)
		return
	}
	writeJSON(w, http.StatusCreated, doc)
}

func (s *Service) legacyCreateMany(w http.ResponseWriter, r *http.Request) {
	var ins []CreateInput
	if err := decodeJSON(r, &ins); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	for _, in := range ins {
		if err := in.validate(); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	count, err := s.store.CreateMany(r.Context(), ins)
	if err != nil {
		st, msg := mapErrStatus(err)
		writeError(w, st, msg)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"count": count})
}

func (s *Service) legacyStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.GetStatsByCategory(r.Context())
	if err != nil && !errors.Is(err, ErrPoolUnavailable) {
		writeError(w, http.StatusInternalServerError, "failed to load stats")
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (s *Service) legacyHot(w http.ResponseWriter, r *http.Request) {
	docs, err := s.store.GetHotDocs(r.Context(), limitParam(r, 5))
	if err != nil && !errors.Is(err, ErrPoolUnavailable) {
		writeError(w, http.StatusInternalServerError, "failed to load hot docs")
		return
	}
	if docs == nil {
		docs = []MobileDoc{}
	}
	writeJSON(w, http.StatusOK, docs)
}

func (s *Service) legacyFindOne(w http.ResponseWriter, r *http.Request) {
	doc, err := s.store.FindOne(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		st, msg := mapErrStatus(err)
		writeError(w, st, msg)
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

func (s *Service) legacyRelated(w http.ResponseWriter, r *http.Request) {
	docs, err := s.store.GetRelatedDocs(r.Context(), chi.URLParam(r, "id"), limitParam(r, 5))
	if err != nil {
		st, msg := mapErrStatus(err)
		writeError(w, st, msg)
		return
	}
	if docs == nil {
		docs = []MobileDoc{}
	}
	writeJSON(w, http.StatusOK, docs)
}

func (s *Service) legacyClear(w http.ResponseWriter, r *http.Request) {
	if err := s.store.ClearAll(r.Context()); err != nil {
		st, msg := mapErrStatus(err)
		writeError(w, st, msg)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"message": "所有文档清空成功"})
}

func (s *Service) legacyUpdate(w http.ResponseWriter, r *http.Request) {
	var patch UpdateInput
	if err := decodeJSON(r, &patch); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	doc, err := s.store.Update(r.Context(), chi.URLParam(r, "id"), patch)
	if err != nil {
		st, msg := mapErrStatus(err)
		writeError(w, st, msg)
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

func (s *Service) legacyRemove(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Remove(r.Context(), chi.URLParam(r, "id")); err != nil {
		st, msg := mapErrStatus(err)
		writeError(w, st, msg)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"message": "文档删除成功"})
}

func (s *Service) legacyUnpublish(w http.ResponseWriter, r *http.Request) {
	doc, err := s.store.SoftRemove(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		st, msg := mapErrStatus(err)
		writeError(w, st, msg)
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

// ---- MobileV1Controller (SuccessResponse envelope) -------------------------

func (s *Service) v1FindAll(w http.ResponseWriter, r *http.Request) {
	out, err := s.store.FindAll(r.Context(), parseQuery(r))
	if err != nil && !errors.Is(err, ErrPoolUnavailable) {
		writeError(w, http.StatusInternalServerError, "failed to load docs")
		return
	}
	writeSuccess(w, http.StatusOK, out, "")
}

func (s *Service) v1FindOne(w http.ResponseWriter, r *http.Request) {
	doc, err := s.store.FindOne(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		st, msg := mapErrStatus(err)
		writeError(w, st, msg)
		return
	}
	writeSuccess(w, http.StatusOK, doc, "")
}

func (s *Service) v1Create(w http.ResponseWriter, r *http.Request) {
	var in CreateInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := in.validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	doc, err := s.store.Create(r.Context(), in)
	if err != nil {
		st, msg := mapErrStatus(err)
		writeError(w, st, msg)
		return
	}
	writeSuccess(w, http.StatusCreated, doc, "文档创建成功")
}

func (s *Service) v1CreateMany(w http.ResponseWriter, r *http.Request) {
	var ins []CreateInput
	if err := decodeJSON(r, &ins); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	for _, in := range ins {
		if err := in.validate(); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	count, err := s.store.CreateMany(r.Context(), ins)
	if err != nil {
		st, msg := mapErrStatus(err)
		writeError(w, st, msg)
		return
	}
	writeSuccess(w, http.StatusCreated, map[string]any{"count": count}, "成功创建 "+strconv.FormatInt(count, 10)+" 个文档")
}

func (s *Service) v1Update(w http.ResponseWriter, r *http.Request) {
	var patch UpdateInput
	if err := decodeJSON(r, &patch); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	doc, err := s.store.Update(r.Context(), chi.URLParam(r, "id"), patch)
	if err != nil {
		st, msg := mapErrStatus(err)
		writeError(w, st, msg)
		return
	}
	writeSuccess(w, http.StatusOK, doc, "文档更新成功")
}

func (s *Service) v1Remove(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Remove(r.Context(), chi.URLParam(r, "id")); err != nil {
		st, msg := mapErrStatus(err)
		writeError(w, st, msg)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) v1Categories(w http.ResponseWriter, r *http.Request) {
	cats, err := s.store.GetCategories(r.Context())
	if err != nil && !errors.Is(err, ErrPoolUnavailable) {
		writeError(w, http.StatusInternalServerError, "failed to load categories")
		return
	}
	writeSuccess(w, http.StatusOK, cats, "")
}

// ---- WebV1Controller (SuccessResponse + web-enhanced detail) ---------------

type webMeta struct {
	WordCount         int       `json:"wordCount"`
	EstimatedReadTime int       `json:"estimatedReadTime"`
	LastModified      time.Time `json:"lastModified"`
	Version           int       `json:"version"`
	CanEdit           bool      `json:"canEdit"`
	CanDelete         bool      `json:"canDelete"`
	ShareURL          string    `json:"shareUrl"`
}

type webEnhancedDoc struct {
	MobileDoc
	Web webMeta `json:"_web"`
}

func (s *Service) webFindAll(w http.ResponseWriter, r *http.Request) {
	out, err := s.store.FindAll(r.Context(), parseQuery(r))
	if err != nil && !errors.Is(err, ErrPoolUnavailable) {
		writeError(w, http.StatusInternalServerError, "failed to load docs")
		return
	}
	writeSuccess(w, http.StatusOK, out, "")
}

func (s *Service) webFindOne(w http.ResponseWriter, r *http.Request) {
	doc, err := s.store.FindOne(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		st, msg := mapErrStatus(err)
		writeError(w, st, msg)
		return
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	wordCount := len(doc.Content)
	enhanced := webEnhancedDoc{
		MobileDoc: doc,
		Web: webMeta{
			WordCount:         wordCount,
			EstimatedReadTime: (wordCount + 499) / 500, // ceil(len/500)
			LastModified:      doc.UpdatedAt,
			Version:           1,
			CanEdit:           true,
			CanDelete:         true,
			ShareURL:          scheme + "://" + r.Host + "/docs/" + doc.ID,
		},
	}
	writeSuccess(w, http.StatusOK, enhanced, "")
}

func (s *Service) webCreate(w http.ResponseWriter, r *http.Request) {
	var in CreateInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if err := in.validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	doc, err := s.store.Create(r.Context(), in)
	if err != nil {
		st, msg := mapErrStatus(err)
		writeError(w, st, msg)
		return
	}
	writeSuccess(w, http.StatusCreated, doc, "文档创建成功")
}

func (s *Service) webUpdate(w http.ResponseWriter, r *http.Request) {
	var patch UpdateInput
	if err := decodeJSON(r, &patch); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	doc, err := s.store.Update(r.Context(), chi.URLParam(r, "id"), patch)
	if err != nil {
		st, msg := mapErrStatus(err)
		writeError(w, st, msg)
		return
	}
	writeSuccess(w, http.StatusOK, doc, "文档更新成功")
}

func (s *Service) webRemove(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Remove(r.Context(), chi.URLParam(r, "id")); err != nil {
		st, msg := mapErrStatus(err)
		writeError(w, st, msg)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) webStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.GetStats(r.Context())
	if err != nil && !errors.Is(err, ErrPoolUnavailable) {
		writeError(w, http.StatusInternalServerError, "failed to load stats")
		return
	}
	writeSuccess(w, http.StatusOK, stats, "")
}

func (s *Service) webSearch(w http.ResponseWriter, r *http.Request) {
	q := parseQuery(r)
	q.Search = r.URL.Query().Get("q")
	out, err := s.store.FindAll(r.Context(), q)
	if err != nil && !errors.Is(err, ErrPoolUnavailable) {
		writeError(w, http.StatusInternalServerError, "failed to search docs")
		return
	}
	writeSuccess(w, http.StatusOK, out, "")
}

// ---- route registration ----------------------------------------------------

// RegisterMobile adds the legacy /docs/* and the /v1/* routes to a router the
// caller mounts at /api/mobile. guarded wraps the non-@Public routes (parity
// with the content app's global JwtAuthGuard); nil guarded mounts them open.
func RegisterMobile(r chi.Router, s *Service, guarded func(http.Handler) http.Handler) {
	g := func(h http.HandlerFunc) http.Handler {
		if guarded != nil {
			return guarded(h)
		}
		return h
	}
	r.Route("/docs", func(d chi.Router) {
		// @Public reads + create/batch/clear
		d.Get("/", s.legacyFindAll)
		d.Post("/", s.legacyCreate)
		d.Post("/batch", s.legacyCreateMany)
		d.Get("/stats", s.legacyStats)
		d.Get("/hot", s.legacyHot)
		d.Delete("/clear", s.legacyClear)
		d.Get("/{id}", s.legacyFindOne)
		d.Get("/{id}/related", s.legacyRelated)
		// guarded mutations (no @Public)
		d.Method(http.MethodPatch, "/{id}", g(s.legacyUpdate))
		d.Method(http.MethodDelete, "/{id}", g(s.legacyRemove))
		d.Method(http.MethodPatch, "/{id}/unpublish", g(s.legacyUnpublish))
	})
	// /api/mobile/v1/* — all guarded (no @Public on MobileV1Controller).
	r.Route("/v1", func(v chi.Router) {
		v.Method(http.MethodGet, "/docs", g(s.v1FindAll))
		v.Method(http.MethodGet, "/categories", g(s.v1Categories))
		v.Method(http.MethodPost, "/docs", g(s.v1Create))
		v.Method(http.MethodPost, "/docs/batch", g(s.v1CreateMany))
		v.Method(http.MethodGet, "/docs/{id}", g(s.v1FindOne))
		v.Method(http.MethodPut, "/docs/{id}", g(s.v1Update))
		v.Method(http.MethodDelete, "/docs/{id}", g(s.v1Remove))
	})
}

// WebV1Router builds the /api/web/v1 router (all routes guarded; no @Public on
// WebV1Controller).
func WebV1Router(s *Service, guarded func(http.Handler) http.Handler) chi.Router {
	g := func(h http.HandlerFunc) http.Handler {
		if guarded != nil {
			return guarded(h)
		}
		return h
	}
	r := chi.NewRouter()
	r.Route("/docs", func(d chi.Router) {
		d.Method(http.MethodGet, "/", g(s.webFindAll))
		d.Method(http.MethodGet, "/stats", g(s.webStats))
		d.Method(http.MethodGet, "/search", g(s.webSearch))
		d.Method(http.MethodGet, "/{id}", g(s.webFindOne))
		d.Method(http.MethodPost, "/", g(s.webCreate))
		d.Method(http.MethodPut, "/{id}", g(s.webUpdate))
		d.Method(http.MethodDelete, "/{id}", g(s.webRemove))
	})
	return r
}
