package users

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
)

type stubStore struct {
	findFn   func(context.Context, string) (User, error)
	updateFn func(context.Context, string, UpdateInput) (User, error)
	deleteFn func(context.Context, string) error
}

func (s stubStore) FindByID(ctx context.Context, id string) (User, error) {
	if s.findFn != nil {
		return s.findFn(ctx, id)
	}
	return User{ID: id, Email: "a@b.co", Username: "alice", Roles: []string{"user"}}, nil
}
func (s stubStore) Update(ctx context.Context, id string, in UpdateInput) (User, error) {
	if s.updateFn != nil {
		return s.updateFn(ctx, id, in)
	}
	out := User{ID: id, Email: "a@b.co", Username: "alice", Roles: []string{"user"}}
	if in.Email != nil {
		out.Email = *in.Email
	}
	if in.Username != nil {
		out.Username = *in.Username
	}
	return out, nil
}
func (s stubStore) Delete(ctx context.Context, id string) error {
	if s.deleteFn != nil {
		return s.deleteFn(ctx, id)
	}
	return nil
}

// stubGuard ignores auth; just pre-populates the user-id context so
// /me handlers behave as if authenticated.
func stubGuard(userID string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := auth.WithUser(r.Context(), auth.UserClaims{Sub: userID})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func TestRouter_GetByID_PublicNoAuthNeeded(t *testing.T) {
	mux := Router(stubStore{}, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/u-1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var v PublicView
	_ = json.NewDecoder(rec.Body).Decode(&v)
	if v.ID != "u-1" {
		t.Errorf("ID = %q, want u-1", v.ID)
	}
}

func TestRouter_GetByID_NotFound(t *testing.T) {
	stub := stubStore{findFn: func(context.Context, string) (User, error) {
		return User{}, ErrNotFound
	}}
	mux := Router(stub, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/missing", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestRouter_GetMe_NoGuardFallsThroughToParam(t *testing.T) {
	// When no access guard is wired (no JWT_SECRET), /me isn't
	// explicitly registered — chi falls through to the /:id route and
	// treats "me" as a user ID. In production JWT_SECRET is set and
	// /me is mounted before /:id, so the parameterized lookup never
	// fires. The DB will return 404 anyway since no user has id="me".
	stub := stubStore{findFn: func(_ context.Context, id string) (User, error) {
		if id == "me" {
			return User{}, ErrNotFound
		}
		return User{ID: id, Email: "e", Username: "u"}, nil
	}}
	mux := Router(stub, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/me", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 (fall-through, missing user)", rec.Code)
	}
}

func TestRouter_GetMe_GuardSuppliesContext(t *testing.T) {
	mux := Router(stubStore{}, stubGuard("u-42"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/me", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var v PublicView
	_ = json.NewDecoder(rec.Body).Decode(&v)
	if v.ID != "u-42" {
		t.Errorf("ID = %q, want u-42", v.ID)
	}
}

func TestRouter_PatchMe_BadJSON(t *testing.T) {
	mux := Router(stubStore{}, stubGuard("u-1"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/me", strings.NewReader("{")))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestRouter_PatchMe_DuplicateMapped400(t *testing.T) {
	stub := stubStore{updateFn: func(context.Context, string, UpdateInput) (User, error) {
		return User{}, ErrDuplicate
	}}
	mux := Router(stub, stubGuard("u-1"))
	body := `{"email":"new@example.com"}`
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/me", strings.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (duplicate)", rec.Code)
	}
}

func TestRouter_PatchMe_NotFound(t *testing.T) {
	stub := stubStore{updateFn: func(context.Context, string, UpdateInput) (User, error) {
		return User{}, ErrNotFound
	}}
	mux := Router(stub, stubGuard("u-1"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/me", strings.NewReader(`{}`)))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestRouter_PatchMe_Happy(t *testing.T) {
	var captured UpdateInput
	stub := stubStore{updateFn: func(_ context.Context, id string, in UpdateInput) (User, error) {
		captured = in
		return User{ID: id, Email: *in.Email, Username: "u"}, nil
	}}
	mux := Router(stub, stubGuard("u-1"))
	body := `{"email":"new@example.com","bio":"hi"}`
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/me", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	if captured.Email == nil || *captured.Email != "new@example.com" {
		t.Errorf("Email = %v, want *new@example.com", captured.Email)
	}
}

func TestRouter_DeleteMe_404OnMissing(t *testing.T) {
	stub := stubStore{deleteFn: func(context.Context, string) error {
		return ErrNotFound
	}}
	mux := Router(stub, stubGuard("u-1"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/me", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestRouter_DeleteMe_Happy(t *testing.T) {
	mux := Router(stubStore{}, stubGuard("u-1"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/me", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", rec.Code, rec.Body.String())
	}
}

// Verify the public ErrNotFound import path is reachable for callers.
var _ = errors.Is
