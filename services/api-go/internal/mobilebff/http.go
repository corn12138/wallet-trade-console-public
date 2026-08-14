package mobilebff

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

// Register mounts the 13 mobile-bff routes on a router the caller mounts at
// /api/mobile (alongside mobiledoc + mobilecontrol). All @Public.
func Register(r chi.Router, s *Service) {
	r.Get("/feed/home", s.handleFeedHome)
	r.Get("/article/{id}", s.handleArticleDetail)
	r.Get("/comment/sheet", s.handleCommentSheet)
	r.Post("/comment/create", s.handleCommentCreate)
	r.Get("/search/index", s.handleSearchIndex)
	r.Get("/message/center", s.handleMessageCenter)
	r.Post("/message/read", s.handleMessageRead)
	r.Get("/profile/home", s.handleProfileHome)
	r.Get("/settings/index", s.handleSettingsIndex)
	r.Get("/topic/{slug}", s.handleTopicLanding)
	r.Get("/video/feed", s.handleVideoFeed)
	r.Get("/video/{id}", s.handleVideoDetail)
	r.Post("/share/prepare", s.handleSharePrepare)
}

func (s *Service) handleFeedHome(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.GetFeedHome(r.Context()))
}

func (s *Service) handleArticleDetail(w http.ResponseWriter, r *http.Request) {
	out, err := s.GetArticleDetail(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Service) handleCommentSheet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.GetCommentSheet(r.Context(), r, r.URL.Query().Get("articleId")))
}

func (s *Service) handleCommentCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ArticleID string `json:"articleId"`
		ParentID  string `json:"parentId"`
		Content   string `json:"content"`
		Source    string `json:"source"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	out, err := s.CreateComment(r.Context(), r, body.ArticleID, body.ParentID, body.Content)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Service) handleSearchIndex(w http.ResponseWriter, r *http.Request) {
	limit := parseSearchResultLimit(r.URL.Query().Get("limit"))
	writeJSON(w, http.StatusOK, s.GetSearchIndex(r.Context(), r.URL.Query().Get("query"), limit))
}

func (s *Service) handleMessageCenter(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.GetMessageCenter(r.Context(), r))
}

func (s *Service) handleMessageRead(w http.ResponseWriter, r *http.Request) {
	var body MessageReadRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	out, err := s.MarkMessagesRead(r.Context(), r, body)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Service) handleProfileHome(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.GetProfileHome(r.Context(), r))
}

func (s *Service) handleSettingsIndex(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.GetSettingsIndex(r.Context(), r, r.URL.Query().Get("ownerAddress")))
}

func (s *Service) handleTopicLanding(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.GetTopicLanding(r.Context(), chi.URLParam(r, "slug")))
}

func (s *Service) handleVideoFeed(w http.ResponseWriter, r *http.Request) {
	limit := 0
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil {
		limit = n
	}
	writeJSON(w, http.StatusOK, s.GetVideoFeed(r.URL.Query().Get("cursor"), limit))
}

func (s *Service) handleVideoDetail(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.GetVideoDetail(chi.URLParam(r, "id")))
}

func (s *Service) handleSharePrepare(w http.ResponseWriter, r *http.Request) {
	var body SharePrepareRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(body.ResourceID) == "" {
		writeError(w, http.StatusBadRequest, "resourceId is required")
		return
	}
	out, err := s.PrepareShare(r.Context(), body)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// ---- shared helpers --------------------------------------------------------

func decodeJSON(r *http.Request, dst any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(dst)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Error("mobilebff response encode failed", "err", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"statusCode": status, "message": message})
}

func writeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrLoginRequired):
		writeError(w, http.StatusUnauthorized, "login-required")
	case errors.Is(err, ErrBadRequest):
		writeError(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "bad request: "))
	case errors.Is(err, ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	default:
		slog.Error("mobilebff handler failed", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}
