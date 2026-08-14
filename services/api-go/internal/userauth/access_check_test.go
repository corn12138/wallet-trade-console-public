package userauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/users"
)

type stubUserChecker struct {
	user users.User
	err  error
}

func (s stubUserChecker) FindByID(context.Context, string) (users.User, error) {
	return s.user, s.err
}

// TestAccessMiddleware_UserExistenceCheck locks the guard-parity addendum
// (2026-06-08): a valid access token for a deleted or disabled user is rejected
// (401), matching NestJS JwtStrategy.validate; a nil checker keeps the prior
// pure-JWT behaviour (back-compat).
func TestAccessMiddleware_UserExistenceCheck(t *testing.T) {
	v := auth.NewUserVerifier("a", "b", 0, 0)
	access, _, err := v.IssueTokens("user-42", "alice", "a@b.co", []string{"user"})
	if err != nil {
		t.Fatalf("issue tokens: %v", err)
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	do := func(checker AccessUserChecker) int {
		mw := AccessMiddleware(v, checker)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("Authorization", "Bearer "+access)
		rec := httptest.NewRecorder()
		mw(next).ServeHTTP(rec, req)
		return rec.Code
	}

	if got := do(stubUserChecker{user: users.User{ID: "user-42", Roles: []string{"user"}}}); got != http.StatusOK {
		t.Errorf("existing enabled user = %d, want 200", got)
	}
	if got := do(stubUserChecker{err: errors.New("not found")}); got != http.StatusUnauthorized {
		t.Errorf("missing user = %d, want 401", got)
	}
	if got := do(stubUserChecker{user: users.User{ID: "user-42", Roles: []string{"user", "disabled"}}}); got != http.StatusUnauthorized {
		t.Errorf("disabled user = %d, want 401", got)
	}
	if got := do(nil); got != http.StatusOK {
		t.Errorf("nil checker (pure-JWT) = %d, want 200", got)
	}
}
