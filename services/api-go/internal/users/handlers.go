package users

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/go-chi/chi/v5"
)

// Store is the surface the handlers use (tests pass a stub).
type Store interface {
	FindByID(ctx context.Context, id string) (User, error)
	Update(ctx context.Context, id string, in UpdateInput) (User, error)
	Delete(ctx context.Context, id string) error
}

// Router mounts /api/users with 4 NestJS routes. /me variants are
// guarded; /:id is public (matches the NestJS controller — only the
// ApiBearerAuth() decorator marks it as documented, the actual guard
// is the app-level JwtAuthGuard which is mirrored later in Phase 7a).
func Router(store Store, accessGuard func(http.Handler) http.Handler) chi.Router {
	r := chi.NewRouter()

	// /me routes require an access token. We rely on the caller to
	// pass a non-nil accessGuard when JWT_SECRET is wired; without it,
	// the /me routes refuse to mount (matches /auth's behaviour).
	if accessGuard != nil {
		r.With(accessGuard).Get("/me", getMe(store))
		r.With(accessGuard).Patch("/me", patchMe(store))
		r.With(accessGuard).Delete("/me", deleteMe(store))
	}

	// /:id is public; chi routes resolve in registration order so /me
	// above takes precedence over the parameterized segment.
	r.Get("/{id}", getByID(store))
	return r
}

func getMe(store Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := auth.UserIDFromContext(r.Context())
		if userID == "" {
			writeError(w, http.StatusUnauthorized, "未认证")
			return
		}
		view, err := lookup(r.Context(), store, userID)
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "用户不存在")
			return
		}
		if err != nil {
			slog.ErrorContext(r.Context(), "users get me failed", "err", err)
			writeError(w, http.StatusInternalServerError, "failed to load user")
			return
		}
		writeJSON(w, http.StatusOK, view)
	}
}

func patchMe(store Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := auth.UserIDFromContext(r.Context())
		if userID == "" {
			writeError(w, http.StatusUnauthorized, "未认证")
			return
		}
		var in UpdateInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if in.Email != nil {
			v := strings.TrimSpace(*in.Email)
			in.Email = &v
		}
		if in.Username != nil {
			v := strings.TrimSpace(*in.Username)
			in.Username = &v
		}
		if store == nil {
			writeError(w, http.StatusServiceUnavailable, "database unavailable")
			return
		}
		updated, err := store.Update(r.Context(), userID, in)
		if errors.Is(err, ErrPoolUnavailable) {
			writeError(w, http.StatusServiceUnavailable, "database unavailable")
			return
		}
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "用户不存在")
			return
		}
		if errors.Is(err, ErrDuplicate) {
			writeError(w, http.StatusBadRequest, "该电子邮箱或用户名已被使用")
			return
		}
		if err != nil {
			slog.ErrorContext(r.Context(), "users patch me failed", "err", err)
			writeError(w, http.StatusInternalServerError, "failed to update user")
			return
		}
		writeJSON(w, http.StatusOK, updated.Public())
	}
}

func deleteMe(store Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := auth.UserIDFromContext(r.Context())
		if userID == "" {
			writeError(w, http.StatusUnauthorized, "未认证")
			return
		}
		if store == nil {
			writeError(w, http.StatusServiceUnavailable, "database unavailable")
			return
		}
		err := store.Delete(r.Context(), userID)
		if errors.Is(err, ErrPoolUnavailable) {
			writeError(w, http.StatusServiceUnavailable, "database unavailable")
			return
		}
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "用户不存在")
			return
		}
		if err != nil {
			slog.ErrorContext(r.Context(), "users delete me failed", "err", err)
			writeError(w, http.StatusInternalServerError, "failed to delete user")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"message": "用户删除成功"})
	}
}

func getByID(store Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if id == "" {
			writeError(w, http.StatusBadRequest, "id is required")
			return
		}
		view, err := lookup(r.Context(), store, id)
		if errors.Is(err, ErrPoolUnavailable) {
			writeError(w, http.StatusServiceUnavailable, "database unavailable")
			return
		}
		if errors.Is(err, ErrNotFound) {
			writeError(w, http.StatusNotFound, "用户不存在")
			return
		}
		if err != nil {
			slog.ErrorContext(r.Context(), "users get by id failed", "err", err)
			writeError(w, http.StatusInternalServerError, "failed to load user")
			return
		}
		writeJSON(w, http.StatusOK, view)
	}
}

func lookup(ctx context.Context, store Store, id string) (PublicView, error) {
	if store == nil {
		return PublicView{}, ErrPoolUnavailable
	}
	u, err := store.FindByID(ctx, id)
	if err != nil {
		return PublicView{}, err
	}
	return u.Public(), nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Error("users response encode failed", "err", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"statusCode": status,
		"message":    message,
	})
}
