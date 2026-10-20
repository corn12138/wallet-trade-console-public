package auth_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth/authtest"
)

func serve(t *testing.T, mw func(http.Handler) http.Handler, headers map[string][]string) (int, string) {
	t.Helper()
	var seen string
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen = auth.AddressFromContext(r.Context()) }))
	req := httptest.NewRequest(http.MethodGet, "/private", nil)
	for key, values := range headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec.Code, seen
}

func TestStrictMiddlewareAcceptsOnlyLiveSessions(t *testing.T) {
	f := authtest.New(t)
	token, sid := f.Login(t, walletA, 11155111)
	bearer := map[string][]string{"Authorization": {"Bearer " + token}}
	if code, seen := serve(t, auth.StrictMiddleware(f.Verifier), bearer); code != 200 || seen != walletA {
		t.Fatalf("live session: %d %q", code, seen)
	}
	for name, headers := range map[string]map[string][]string{
		"duplicate authorization": {"Authorization": {"Bearer " + token, "Bearer " + token}},
		"malformed":               {"Authorization": {"Token " + token}},
		"wallet header only":      {"X-Wallet-Address": {walletB}},
		"nothing":                 {},
	} {
		if code, seen := serve(t, auth.StrictMiddleware(f.Verifier), headers); code != 401 || seen != "" {
			t.Fatalf("%s: %d %q", name, code, seen)
		}
	}
	f.Sessions.SetDown(true)
	if code, _ := serve(t, auth.StrictMiddleware(f.Verifier), bearer); code != 503 {
		t.Fatalf("store down must be 503, got %d", code)
	}
	f.Sessions.SetDown(false)
	f.Sessions.RevokeID(sid)
	if code, _ := serve(t, auth.StrictMiddleware(f.Verifier), bearer); code != 401 {
		t.Fatalf("revoked session must be 401, got %d", code)
	}
}

// No header, zero address or missing verifier is ever an identity: a nil
// verifier is an outage (503 once a credential is presented), not anonymous.
func TestNoIdentityWithoutAVerifiedSession(t *testing.T) {
	f := authtest.New(t)
	for name, headers := range map[string]map[string][]string{
		"wallet header":      {"X-Wallet-Address": {walletB}},
		"zero wallet header": {"X-Wallet-Address": {"0x0000000000000000000000000000000000000000"}},
		"bearer plus header": {"Authorization": {"Bearer not-a-token"}, "X-Wallet-Address": {walletB}},
	} {
		if code, seen := serve(t, auth.StrictMiddleware(f.Verifier), headers); code != 401 || seen != "" {
			t.Fatalf("%s: %d %q", name, code, seen)
		}
	}
	token, _ := f.Login(t, walletA, 1)
	if code, seen := serve(t, auth.StrictMiddleware(nil), map[string][]string{"Authorization": {"Bearer " + token}}); code != 503 || seen != "" {
		t.Fatalf("nil verifier with a credential: %d %q", code, seen)
	}
	if code, _ := serve(t, auth.StrictMiddleware(nil), map[string][]string{"X-Wallet-Address": {walletB}}); code != 401 {
		t.Fatalf("nil verifier without a credential: %d", code)
	}
}

func TestStrictFailureSeesUnavailability(t *testing.T) {
	f := authtest.New(t)
	token, _ := f.Login(t, walletA, 1)
	f.Sessions.SetDown(true)
	var got error
	mw := auth.StrictMiddlewareWithFailure(f.Verifier, func(w http.ResponseWriter, _ *http.Request, err error) {
		got = err
		w.WriteHeader(599)
	})
	serve(t, mw, map[string][]string{"Authorization": {"Bearer " + token}})
	if !errors.Is(got, auth.ErrSessionUnavailable) {
		t.Fatalf("failure must receive ErrSessionUnavailable, got %v", got)
	}
}

// A handler that waited (for example for a database lock) re-checks the
// session, so a logout or revocation during the wait is honoured.
func TestRevalidateStrictSeesRevocationAfterAdmission(t *testing.T) {
	f := authtest.New(t)
	token, sid := f.Login(t, walletA, 1)
	var revalidate func() error
	mw := auth.StrictMiddleware(f.Verifier)
	handler := mw(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		revalidate = func() error { _, err := auth.RevalidateStrict(r.Context(), f.Verifier); return err }
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	handler.ServeHTTP(httptest.NewRecorder(), req)
	if err := revalidate(); err != nil {
		t.Fatal(err)
	}
	f.Sessions.RevokeID(sid)
	if err := revalidate(); !errors.Is(err, auth.ErrSessionInvalid) {
		t.Fatalf("revocation after admission must be seen: %v", err)
	}
	if _, err := auth.RevalidateStrict(auth.WithAddress(context.Background(), walletA), f.Verifier); err == nil {
		t.Fatal("an address without a credential is not authentication")
	}
}
