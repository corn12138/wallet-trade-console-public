package web3auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth/authtest"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/golang-jwt/jwt/v5"
)

const sessionTestChain = 11155111

// signIn runs a real SIWE challenge and signature through VerifySiwe.
func signIn(t *testing.T, svc *Service) (VerifyResponse, string, VerifyInput, error) {
	t.Helper()
	priv, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	addr := addrFromPriv(t, priv)
	challenge, err := svc.GenerateNonceFor(context.Background(), addr)
	if err != nil {
		t.Fatal(err)
	}
	message := buildMessage(addr, sessionTestChain, challenge)
	in := VerifyInput{Message: message, Signature: signEthMessage(t, priv, message)}
	out, err := svc.VerifySiwe(context.Background(), in)
	return out, strings.ToLower(addr), in, err
}

func call(t *testing.T, handler http.Handler, method, path, token string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec, body
}

func TestVerifyPersistsTheSessionTheTokenIsBoundTo(t *testing.T) {
	svc, f := withFixture(t, NewService(2*time.Hour, 0, []int{sessionTestChain}))
	out, wallet, _, err := signIn(t, svc)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := f.Verifier.Authenticate(context.Background(), out.Token)
	if err != nil {
		t.Fatalf("an issued token must authenticate its own session: %v", err)
	}
	stored := f.Sessions.All()
	if len(stored) != 1 {
		t.Fatalf("want exactly one persisted session, got %d", len(stored))
	}
	ref := stored[0].Ref
	want := auth.SessionRef{ID: claims.SessionID, Wallet: wallet, ChainID: sessionTestChain, Version: 1, Epoch: authtest.Epoch, KeyID: authtest.KeyID}
	if ref != want {
		t.Fatalf("stored session %+v, want %+v", ref, want)
	}
	if claims.Issuer != authtest.Issuer || len(claims.Audience) != 1 || claims.Audience[0] != authtest.Audience {
		t.Fatalf("issuer/audience not from the policy: %q %v", claims.Issuer, claims.Audience)
	}
	if !claims.ExpiresAt.Time.Equal(stored[0].Expires.Truncate(time.Second)) {
		t.Fatalf("token expiry %v differs from the session's %v", claims.ExpiresAt.Time, stored[0].Expires)
	}
	if got := claims.ExpiresAt.Sub(claims.IssuedAt.Time); got != 2*time.Hour {
		t.Fatalf("lifetime %v, want the configured session TTL", got)
	}
	if out.SessionExpiresAt != stored[0].Expires.UTC().Format(time.RFC3339) {
		t.Fatalf("sessionExpiresAt %q does not describe the stored session", out.SessionExpiresAt)
	}
}

// A token whose session could not be recorded could never be revoked, so none
// is issued; the consumed challenge is not reusable afterwards either.
func TestVerifyIssuesNothingWhenTheSessionCannotBeRecorded(t *testing.T) {
	svc, f := withFixture(t, NewService(0, 0, []int{sessionTestChain}))
	f.Sessions.SetDown(true)
	out, _, in, err := signIn(t, svc)
	if !errors.Is(err, ErrSessionStoreUnavailable) || out.Token != "" {
		t.Fatalf("store down: token=%q err=%v", out.Token, err)
	}
	f.Sessions.SetDown(false)
	if _, err := svc.VerifySiwe(context.Background(), in); !errors.Is(err, ErrNonceUnknown) {
		t.Fatalf("the signed challenge must not be replayable after the failure: %v", err)
	}
	if n := len(f.Sessions.All()); n != 0 {
		t.Fatalf("no session may exist, got %d", n)
	}
}

func jsonString(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}

func TestVerifyHandlerAnswers503WithoutAToken(t *testing.T) {
	for name, prepare := range map[string]func(*Service, *authtest.Fixture){
		"session store down": func(_ *Service, f *authtest.Fixture) { f.Sessions.SetDown(true) },
		"no credentials":     func(s *Service, _ *authtest.Fixture) { s.credentials = nil },
		"no session store":   func(s *Service, _ *authtest.Fixture) { s.sessions = nil },
	} {
		t.Run(name, func(t *testing.T) {
			svc, f := withFixture(t, NewService(0, 0, []int{sessionTestChain}))
			prepare(svc, f)
			priv, _ := secp256k1.GeneratePrivateKey()
			addr := addrFromPriv(t, priv)
			challenge, err := svc.GenerateNonceFor(context.Background(), addr)
			if err != nil {
				t.Fatal(err)
			}
			message := buildMessage(addr, sessionTestChain, challenge)
			body := `{"message":` + jsonString(message) + `,"signature":"` + signEthMessage(t, priv, message) + `"}`
			rec := httptest.NewRecorder()
			Router(svc).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/verify", strings.NewReader(body)))
			if rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "token") {
				t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// Both /me routes use the guards' session-checking authentication.
func TestMeAnswersOnlyForALiveSession(t *testing.T) {
	for name, mount := range map[string]func(*Service) http.Handler{
		"/api/web3-auth/me": func(s *Service) http.Handler { return Router(s) },
		"/api/auth/me":      func(s *Service) http.Handler { return AuthAliasRouter(s) },
	} {
		t.Run(name, func(t *testing.T) {
			svc, f := withFixture(t, NewService(0, 0, []int{sessionTestChain}))
			out, wallet, _, err := signIn(t, svc)
			if err != nil {
				t.Fatal(err)
			}
			h := mount(svc)
			rec, body := call(t, h, http.MethodGet, "/me", out.Token)
			if rec.Code != 200 || body["address"] != wallet || body["chainId"] != float64(sessionTestChain) {
				t.Fatalf("live session: %d %v", rec.Code, body)
			}
			if rec.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatalf("cache-control %q", rec.Header().Get("Cache-Control"))
			}
			if rec, _ := call(t, h, http.MethodGet, "/me", ""); rec.Code != 401 {
				t.Fatalf("no bearer: %d", rec.Code)
			}
			f.Sessions.SetDown(true)
			if rec, _ := call(t, h, http.MethodGet, "/me", out.Token); rec.Code != 503 {
				t.Fatalf("store down: %d", rec.Code)
			}
			f.Sessions.SetDown(false)
			claims, _ := f.Verifier.VerifyStrict(out.Token)
			f.Sessions.RevokeID(claims.SessionID)
			if rec, _ := call(t, h, http.MethodGet, "/me", out.Token); rec.Code != 401 {
				t.Fatalf("revoked: %d", rec.Code)
			}
			svc.credentials = nil
			if rec, _ := call(t, h, http.MethodGet, "/me", out.Token); rec.Code != 503 {
				t.Fatalf("no verifier: %d", rec.Code)
			}
		})
	}
}

func TestLogoutRevokesTheCallersSessionIdempotently(t *testing.T) {
	svc, _ := withFixture(t, NewService(0, 0, []int{sessionTestChain}))
	out, _, _, err := signIn(t, svc)
	if err != nil {
		t.Fatal(err)
	}
	other, _, _, err := signIn(t, svc)
	if err != nil {
		t.Fatal(err)
	}
	h := Router(svc)
	rec, body := call(t, h, http.MethodPost, "/logout", out.Token)
	if rec.Code != 200 || body["revoked"] != true || body["alreadyRevoked"] != false {
		t.Fatalf("first logout: %d %v", rec.Code, body)
	}
	if rec.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("cache-control %q", rec.Header().Get("Cache-Control"))
	}
	if rec, _ := call(t, h, http.MethodGet, "/me", out.Token); rec.Code != 401 {
		t.Fatalf("the logged-out token must stop working: %d", rec.Code)
	}
	rec, body = call(t, h, http.MethodPost, "/logout", out.Token)
	if rec.Code != 200 || body["revoked"] != true || body["alreadyRevoked"] != true {
		t.Fatalf("repeated logout: %d %v", rec.Code, body)
	}
	if rec, _ := call(t, h, http.MethodGet, "/me", other.Token); rec.Code != 200 {
		t.Fatalf("another session must be unaffected: %d", rec.Code)
	}
}

func TestLogoutFailureModes(t *testing.T) {
	svc, f := withFixture(t, NewService(0, 0, []int{sessionTestChain}))
	out, _, _, err := signIn(t, svc)
	if err != nil {
		t.Fatal(err)
	}
	h := Router(svc)
	if rec, _ := call(t, h, http.MethodPost, "/logout", ""); rec.Code != 401 {
		t.Fatalf("no bearer: %d", rec.Code)
	}
	if rec, _ := call(t, h, http.MethodPost, "/logout", "not-a-token"); rec.Code != 401 {
		t.Fatalf("malformed: %d", rec.Code)
	}
	expired := f.ValidClaims(t, "0x00000000000000000000000000000000000000a1")
	expired.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Hour))
	expired.IssuedAt = jwt.NewNumericDate(time.Now().Add(-2 * time.Hour))
	if rec, _ := call(t, h, http.MethodPost, "/logout", authtest.Sign(t, jwt.SigningMethodHS256, authtest.KeyID, authtest.Secret, expired)); rec.Code != 401 {
		t.Fatalf("expired: %d", rec.Code)
	}
	f.Sessions.SetDown(true)
	rec, _ := call(t, h, http.MethodPost, "/logout", out.Token)
	if rec.Code != 503 {
		t.Fatalf("store down must answer 503 (revocation unknown), got %d", rec.Code)
	}
	f.Sessions.SetDown(false)
	if rec, _ := call(t, h, http.MethodGet, "/me", out.Token); rec.Code != 200 {
		t.Fatalf("a failed logout must not be reported as done; session still live: %d", rec.Code)
	}
	svc.sessions = nil
	if rec, _ := call(t, h, http.MethodPost, "/logout", out.Token); rec.Code != 503 {
		t.Fatalf("no store: %d", rec.Code)
	}
}

// Even a validly signed token that names another wallet's session cannot end
// that session: revocation is keyed by session id and wallet together.
func TestLogoutCannotEndAnotherWalletsSession(t *testing.T) {
	svc, f := withFixture(t, NewService(0, 0, []int{sessionTestChain}))
	victim, _, _, err := signIn(t, svc)
	if err != nil {
		t.Fatal(err)
	}
	victimClaims, _ := f.Verifier.VerifyStrict(victim.Token)
	forged := f.ValidClaims(t, "0x00000000000000000000000000000000000000a1")
	forged.SessionID = victimClaims.SessionID
	token := authtest.Sign(t, jwt.SigningMethodHS256, authtest.KeyID, authtest.Secret, forged)
	rec, body := call(t, Router(svc), http.MethodPost, "/logout", token)
	if rec.Code != 200 || body["alreadyRevoked"] != true {
		t.Fatalf("cross-wallet logout: %d %v", rec.Code, body)
	}
	if rec, _ := call(t, Router(svc), http.MethodGet, "/me", victim.Token); rec.Code != 200 {
		t.Fatalf("the victim's session must survive: %d", rec.Code)
	}
}

// A validly signed token whose claims differ from its session (another
// version or chain) cannot end that session: revocation is bound to the exact
// session the token names. Found by the FQ-01 route matrix (attempt 4).
func TestLogoutIsBoundToTheExactSession(t *testing.T) {
	svc, f := withFixture(t, NewService(0, 0, []int{sessionTestChain}))
	out, _, _, err := signIn(t, svc)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := f.Verifier.VerifyStrict(out.Token)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*auth.Claims){
		"other version": func(c *auth.Claims) { c.SessionVersion = 2 },
		"other chain":   func(c *auth.Claims) { c.ChainID = 1 },
	} {
		forged := claims
		mutate(&forged)
		token := authtest.Sign(t, jwt.SigningMethodHS256, authtest.KeyID, authtest.Secret, forged)
		rec, body := call(t, Router(svc), http.MethodPost, "/logout", token)
		if rec.Code != 200 || body["alreadyRevoked"] != true {
			t.Fatalf("%s: %d %v", name, rec.Code, body)
		}
		if rec, _ := call(t, Router(svc), http.MethodGet, "/me", out.Token); rec.Code != 200 {
			t.Fatalf("%s: the real session must survive: %d", name, rec.Code)
		}
	}
}
