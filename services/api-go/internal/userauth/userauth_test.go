package userauth

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
)

// stubService wraps Service but with a real nil users.Repository — the
// HTTP layer should reject before touching the DB.
func newDegradedRouter() http.Handler {
	svc := NewService(nil, nil, nil)
	return Router(svc, nil, nil)
}

func TestHandler_LoginInvalidJSON_400(t *testing.T) {
	mux := newDegradedRouter()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/login", strings.NewReader("{")))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestHandler_LoginMissingFields_400(t *testing.T) {
	mux := newDegradedRouter()
	body, _ := json.Marshal(LoginInput{})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(body)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestHandler_LoginDegradedService_500(t *testing.T) {
	mux := newDegradedRouter()
	body, _ := json.Marshal(LoginInput{UsernameOrEmail: "u", Password: "p"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader(body)))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 (degraded: nil repo)", rec.Code)
	}
}

func TestHandler_ValidateTokenBad_401(t *testing.T) {
	v := auth.NewUserVerifier("a", "b", 0, 0)
	svc := NewService(nil, v, nil)
	mux := Router(svc, nil, nil)
	body, _ := json.Marshal(map[string]string{"token": "not-a-jwt"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/validate-token", bytes.NewReader(body)))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestHandler_RegisterValidation(t *testing.T) {
	mux := newDegradedRouter()
	cases := []struct {
		name string
		body string
	}{
		{"bad email", `{"email":"not-an-email","username":"u","password":"longenough"}`},
		{"missing username", `{"email":"a@b.co","username":"","password":"longenough"}`},
		{"short password", `{"email":"a@b.co","username":"u","password":"short"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(c.body)))
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestAccessMiddleware_RejectsMissingBearer(t *testing.T) {
	v := auth.NewUserVerifier("a", "b", 0, 0)
	mw := AccessMiddleware(v, nil)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	rec := httptest.NewRecorder()
	mw(next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestAccessMiddleware_PutsUserIDOnContext(t *testing.T) {
	v := auth.NewUserVerifier("a", "b", 0, 0)
	access, _, _ := v.IssueTokens("user-42", "alice", "a@b.co", []string{"user"})
	mw := AccessMiddleware(v, nil)

	var capturedID string
	var capturedRoles []string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedID = auth.UserIDFromContext(r.Context())
		capturedRoles = auth.UserRolesFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+access)
	rec := httptest.NewRecorder()
	mw(next).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if capturedID != "user-42" {
		t.Errorf("UserID = %q, want user-42", capturedID)
	}
	if len(capturedRoles) != 1 || capturedRoles[0] != "user" {
		t.Errorf("Roles = %v, want [user]", capturedRoles)
	}
}

func TestRefreshMiddleware_AcceptsCookie(t *testing.T) {
	v := auth.NewUserVerifier("a", "b", 0, 0)
	_, refresh, _ := v.IssueTokens("u", "n", "e", nil)
	mw := RefreshMiddleware(v)

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if auth.UserIDFromContext(r.Context()) != "u" {
			t.Errorf("user id missing from context")
		}
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.AddCookie(&http.Cookie{Name: "refreshToken", Value: refresh})
	rec := httptest.NewRecorder()
	mw(next).ServeHTTP(rec, req)
	if !called {
		t.Errorf("next not called; status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestValidEmail(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"alice@example.com", true},
		{"alice+tag@example.co.uk", true},
		{"", false},
		{"not-an-email", false},
		{"no-at-domain", false},
		{"Alice <alice@example.com>", false}, // strict: bare address only
	}
	for _, c := range cases {
		if got := validEmail(c.in); got != c.want {
			t.Errorf("validEmail(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// readCookie is reused in handleRefreshGuarded; smoke-test its behavior.
func TestReadCookie(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "refreshToken", Value: "abc"})
	if v := readCookie(req, "refreshToken"); v != "abc" {
		t.Errorf("readCookie = %q, want abc", v)
	}
	if v := readCookie(req, "missing"); v != "" {
		t.Errorf("readCookie(missing) = %q, want empty", v)
	}
}

// Ensure that the chi Router wiring exposes the expected paths even
// when no guard middlewares are provided (the 4 public routes still
// register).
func TestRouter_PublicRoutesPresent(t *testing.T) {
	mux := newDegradedRouter()
	for _, path := range []string{"/login", "/register", "/refresh-token", "/validate-token"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{"))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		// We expect at least a 400 (invalid JSON), not 404.
		if rec.Code == http.StatusNotFound {
			t.Errorf("POST %s → 404 (route not registered)", path)
		}
	}
}

func TestRouter_GuardedRoutesOnlyWhenGuard(t *testing.T) {
	svc := NewService(nil, nil, nil)
	// No guard middlewares → /logout, /refresh, /profile NOT mounted.
	mux := Router(svc, nil, nil)
	for _, p := range []string{"/logout", "/refresh", "/profile"} {
		method := http.MethodPost
		if p == "/profile" {
			method = http.MethodGet
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(method, p, nil))
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s → %d, want 404 (no guard, no route)", method, p, rec.Code)
		}
	}
}

// context import keeps the file compiling if future tests need it.
var _ = context.Background
