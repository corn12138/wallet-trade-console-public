package httpx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/activity"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth/authtest"
)

const sessionTestWallet = "0x000000000000000000000000000000000000dead"

// guardedRouter builds the real router; repositories are nil-pool instances as
// in cmd/api without a database, so handlers degrade instead of panicking.
func guardedRouter(verifier *auth.Verifier) http.Handler {
	return NewRouter(Deps{MarketsLoader: emptyLoader{}, AuthVerifier: verifier, ActivityRepo: activity.NewRepository(nil)})
}

func status(t *testing.T, h http.Handler, method, path string, headers map[string]string) int {
	t.Helper()
	code, _ := respond(t, h, method, path, headers)
	return code
}

func respond(t *testing.T, h http.Handler, method, path string, headers map[string]string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// The guard's own answers; a handler behind it may still fail for missing
// test dependencies, which proves the guard let the request through.
const (
	guardUnavailable = "Web3 session verification unavailable"
	guardRejected    = "Missing or invalid web3 authorization"
)

// Private routes follow the persistent session on the real router; public
// market reads never depend on the session store.
func TestGuardedRoutesFollowThePersistentSession(t *testing.T) {
	f := authtest.New(t)
	h := guardedRouter(f.Verifier)
	token, sid := f.Login(t, sessionTestWallet, 11155111)
	bearer := map[string]string{"Authorization": "Bearer " + token}
	private := []struct{ method, path string }{
		{"GET", "/api/portfolio/summary"},
		{"GET", "/api/activity"},
		{"GET", "/api/settings/preferences"},
		{"GET", "/api/wallets"},
		{"POST", "/api/token"},
		{"POST", "/api/media/upload"},
		{"GET", "/api/i18n/admin/locales"},
	}
	// Each route is mounted behind the guard: anonymous is the guard's 401.
	for _, route := range private {
		if got, body := respond(t, h, route.method, route.path, nil); got != http.StatusUnauthorized || !strings.Contains(body, guardRejected) {
			t.Fatalf("%s %s anonymous = %d %s, want the guard's 401", route.method, route.path, got, body)
		}
	}
	for _, route := range private {
		if got, body := respond(t, h, route.method, route.path, bearer); got == http.StatusUnauthorized || strings.Contains(body, guardUnavailable) {
			t.Fatalf("%s %s live session = %d %s, want the guard to pass", route.method, route.path, got, body)
		}
	}
	f.Sessions.SetDown(true)
	for _, route := range private {
		if got, body := respond(t, h, route.method, route.path, bearer); got != http.StatusServiceUnavailable || !strings.Contains(body, guardUnavailable) {
			t.Fatalf("%s %s store down = %d %s, want the guard's 503", route.method, route.path, got, body)
		}
	}
	for _, path := range []string{"/api/markets/symbols?chainId=1", "/api/token", "/healthz"} {
		if got := status(t, h, "GET", path, nil); got == http.StatusUnauthorized || got == http.StatusServiceUnavailable {
			t.Fatalf("public %s with the store down = %d", path, got)
		}
	}
	f.Sessions.SetDown(false)
	f.Sessions.RevokeID(sid)
	for _, route := range private {
		if got, body := respond(t, h, route.method, route.path, bearer); got != http.StatusUnauthorized || !strings.Contains(body, guardRejected) {
			t.Fatalf("%s %s revoked = %d %s, want the guard's 401", route.method, route.path, got, body)
		}
	}
}

// No environment turns a header into an identity, and a router built without
// a verifier fails closed instead of mounting private routes bare.
func TestNoHeaderIdentityAndNoBareMounts(t *testing.T) {
	for _, env := range []string{"production", "staging", "test", "development", ""} {
		t.Run("NODE_ENV="+env, func(t *testing.T) {
			t.Setenv("NODE_ENV", env)
			h := guardedRouter(httpxAuth.Verifier)
			for _, wallet := range []string{sessionTestWallet, "0x0000000000000000000000000000000000000000"} {
				if got := status(t, h, "GET", "/api/portfolio/summary", map[string]string{"X-Wallet-Address": wallet}); got != http.StatusUnauthorized {
					t.Fatalf("header %s = %d, want 401", wallet, got)
				}
			}
		})
	}
	token, _ := httpxAuth.Login(t, sessionTestWallet, 11155111)
	h := guardedRouter(nil)
	for _, route := range []struct{ method, path string }{{"GET", "/api/portfolio/summary"}, {"POST", "/api/media/upload"}, {"GET", "/api/i18n/admin/locales"}} {
		if got, body := respond(t, h, route.method, route.path, map[string]string{"Authorization": "Bearer " + token}); got != http.StatusServiceUnavailable || !strings.Contains(body, guardUnavailable) {
			t.Fatalf("no verifier %s %s = %d %s, want the guard's 503", route.method, route.path, got, body)
		}
		if got := status(t, h, route.method, route.path, nil); got != http.StatusUnauthorized {
			t.Fatalf("no verifier, no token %s %s = %d, want 401", route.method, route.path, got)
		}
	}
}

func TestSessionWalletResolver(t *testing.T) {
	f := authtest.New(t)
	resolve := sessionWalletResolver(f.Verifier)
	token, sid := f.Login(t, sessionTestWallet, 1)
	request := func(values ...string) *http.Request {
		req := httptest.NewRequest("GET", "/", nil)
		for _, value := range values {
			req.Header.Add("Authorization", value)
		}
		return req
	}
	if got := resolve(request("Bearer " + token)); got != sessionTestWallet {
		t.Fatalf("live session resolved %q", got)
	}
	for name, req := range map[string]*http.Request{
		"no header":        request(),
		"duplicate header": request("Bearer "+token, "Bearer "+token),
		"wrong scheme":     request("Token " + token),
		"garbage":          request("Bearer garbage"),
	} {
		if got := resolve(req); got != "" {
			t.Fatalf("%s resolved %q", name, got)
		}
	}
	f.Sessions.SetDown(true)
	if got := resolve(request("Bearer " + token)); got != "" {
		t.Fatalf("store down resolved %q", got)
	}
	f.Sessions.SetDown(false)
	f.Sessions.RevokeID(sid)
	if got := resolve(request("Bearer " + token)); got != "" {
		t.Fatalf("revoked resolved %q", got)
	}
	if got := sessionWalletResolver(nil)(request("Bearer " + token)); got != "" {
		t.Fatalf("nil verifier resolved %q", got)
	}
}
