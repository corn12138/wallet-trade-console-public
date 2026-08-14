package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const testSecret = "test-jwt-secret-shared-with-nestjs"

// signToken builds an HS256 JWT with the given claims. Tests use this
// to drive the verifier without depending on NestJS being up.
func signToken(t *testing.T, sub string, chainID int, jwtType string, expiresIn time.Duration) string {
	t.Helper()
	claims := Claims{
		Sub:     sub,
		ChainID: chainID,
		Type:    jwtType,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(expiresIn)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(testSecret))
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return signed
}

func TestVerify_HappyPath(t *testing.T) {
	v := NewVerifier(testSecret)
	tok := signToken(t, "0x000000000000000000000000000000000000DEAD", 11155111, "web3", time.Hour)
	claims, err := v.Verify(tok)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	// Address must be lowercased on extraction.
	if claims.Sub != "0x000000000000000000000000000000000000dead" {
		t.Errorf("sub = %q, want lowercase", claims.Sub)
	}
	if claims.ChainID != 11155111 {
		t.Errorf("chainID = %d, want 11155111", claims.ChainID)
	}
}

func TestVerify_RejectsWrongSecret(t *testing.T) {
	v := NewVerifier("different-secret")
	tok := signToken(t, "0x000000000000000000000000000000000000DEAD", 1, "web3", time.Hour)
	if _, err := v.Verify(tok); err != ErrInvalidToken {
		t.Errorf("err = %v, want ErrInvalidToken", err)
	}
}

func TestVerify_RejectsExpired(t *testing.T) {
	v := NewVerifier(testSecret)
	tok := signToken(t, "0x000000000000000000000000000000000000DEAD", 1, "web3", -time.Minute)
	if _, err := v.Verify(tok); err != ErrInvalidToken {
		t.Errorf("err = %v, want ErrInvalidToken", err)
	}
}

func TestVerify_RejectsWrongType(t *testing.T) {
	v := NewVerifier(testSecret)
	tok := signToken(t, "0x000000000000000000000000000000000000DEAD", 1, "user", time.Hour)
	if _, err := v.Verify(tok); err != ErrWrongType {
		t.Errorf("err = %v, want ErrWrongType", err)
	}
}

func TestVerify_RejectsBadAddress(t *testing.T) {
	v := NewVerifier(testSecret)
	tok := signToken(t, "not-an-address", 1, "web3", time.Hour)
	if _, err := v.Verify(tok); err != ErrInvalidAddr {
		t.Errorf("err = %v, want ErrInvalidAddr", err)
	}
}

func TestVerify_EmptySecretRejects(t *testing.T) {
	v := NewVerifier("")
	if _, err := v.Verify("anything"); err != ErrInvalidToken {
		t.Errorf("err = %v, want ErrInvalidToken", err)
	}
}

// Middleware integration tests use a tiny captureHandler that records
// the address it received via context.
type captureHandler struct {
	gotAddr    string
	gotChainID int
	called     bool
}

func (c *captureHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.called = true
	c.gotAddr = AddressFromContext(r.Context())
	c.gotChainID = ChainIDFromContext(r.Context())
	w.WriteHeader(http.StatusOK)
}

func TestMiddleware_BearerPath(t *testing.T) {
	t.Setenv("NODE_ENV", "production")
	v := NewVerifier(testSecret)
	tok := signToken(t, "0x000000000000000000000000000000000000DEAD", 31337, "web3", time.Hour)
	next := &captureHandler{}
	mw := Middleware(v)(next)
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if next.gotAddr != "0x000000000000000000000000000000000000dead" {
		t.Errorf("addr = %q, want lowercase 0x...dead", next.gotAddr)
	}
	if next.gotChainID != 31337 {
		t.Errorf("chainID = %d, want 31337", next.gotChainID)
	}
}

func TestMiddleware_NoBearerInProductionReturns401(t *testing.T) {
	t.Setenv("NODE_ENV", "production")
	v := NewVerifier(testSecret)
	next := &captureHandler{}
	mw := Middleware(v)(next)
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req.Header.Set("X-Wallet-Address", "0x000000000000000000000000000000000000bEEF")
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 (header fallback must NOT work in prod)", rec.Code)
	}
	if next.called {
		t.Errorf("next handler ran in prod without a JWT — guard is leaky")
	}
	// The 401 body must be JSON carrying a `message` (the FE reads `.message`),
	// not the old plaintext "unauthorized" — golden-run-2026-06-08.md blocker A.
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("401 body is not JSON: %v (body=%q)", err, rec.Body.String())
	}
	if msg, _ := body["message"].(string); msg == "" {
		t.Errorf("401 body has no message; got %v", body)
	}
	if sc, _ := body["statusCode"].(float64); int(sc) != http.StatusUnauthorized {
		t.Errorf("401 body statusCode = %v, want 401", body["statusCode"])
	}
}

func TestMiddleware_XWalletAddressFallbackInStaging(t *testing.T) {
	t.Setenv("NODE_ENV", "staging")
	v := NewVerifier(testSecret)
	next := &captureHandler{}
	mw := Middleware(v)(next)
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req.Header.Set("X-Wallet-Address", "0x000000000000000000000000000000000000bEEF")
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if next.gotAddr != "0x000000000000000000000000000000000000beef" {
		t.Errorf("addr = %q, want lowercase beef", next.gotAddr)
	}
}

func TestMiddleware_ZeroAddressFallbackInDevelopment(t *testing.T) {
	t.Setenv("NODE_ENV", "development")
	v := NewVerifier(testSecret)
	next := &captureHandler{}
	mw := Middleware(v)(next)
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if next.gotAddr != "0x0000000000000000000000000000000000000000" {
		t.Errorf("addr = %q, want zero address", next.gotAddr)
	}
}

func TestMiddleware_MalformedBearerReturns401(t *testing.T) {
	t.Setenv("NODE_ENV", "production")
	v := NewVerifier(testSecret)
	next := &captureHandler{}
	mw := Middleware(v)(next)
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req.Header.Set("Authorization", "Bearer not.a.real.jwt")
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestWithAddress(t *testing.T) {
	// Bypass middleware for unit-testing downstream handlers.
	ctx := WithAddress(context.Background(), "0xABCD000000000000000000000000000000000000")
	if got := AddressFromContext(ctx); got != "0xabcd000000000000000000000000000000000000" {
		t.Errorf("got %q, want lowercase 0xabcd...", got)
	}
}

func TestResolveOwner(t *testing.T) {
	cases := []struct {
		name      string
		auth      string
		requested string
		want      string
		wantErr   error
	}{
		{
			name: "no requested returns authenticated",
			auth: "0xABCD000000000000000000000000000000000000",
			want: "0xabcd000000000000000000000000000000000000",
		},
		{
			name:      "matching requested (different case)",
			auth:      "0xabcd000000000000000000000000000000000000",
			requested: "0xABCD000000000000000000000000000000000000",
			want:      "0xabcd000000000000000000000000000000000000",
		},
		{
			name:      "mismatched requested is forbidden",
			auth:      "0xabcd000000000000000000000000000000000000",
			requested: "0x000000000000000000000000000000000000DEAD",
			wantErr:   ErrOwnerMismatch,
		},
		{
			name:      "invalid requested is bad-request",
			auth:      "0xabcd000000000000000000000000000000000000",
			requested: "not-an-address",
			wantErr:   ErrInvalidRequestedAddress,
		},
		{
			name:    "missing authenticated is unauthorized",
			auth:    "",
			wantErr: ErrMissingAuthenticated,
		},
		{
			name:    "non-address authenticated is unauthorized",
			auth:    "not-an-address",
			wantErr: ErrMissingAuthenticated,
		},
	}
	for _, tc := range cases {
		got, err := ResolveOwner(tc.auth, tc.requested)
		if tc.wantErr != nil {
			if err != tc.wantErr {
				t.Errorf("%s: err = %v, want %v", tc.name, err, tc.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: err = %v, want nil", tc.name, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}
