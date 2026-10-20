package auth_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth/authtest"
	"github.com/golang-jwt/jwt/v5"
)

const walletA = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const walletB = "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func TestIssuedTokenAuthenticatesItsOwnSession(t *testing.T) {
	f := authtest.New(t)
	token, sid := f.Login(t, "0xAAAAaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 11155111)
	claims, err := f.Verifier.Authenticate(context.Background(), token)
	if err != nil || claims.Sub != walletA || claims.SessionID != sid || claims.KeyID != authtest.KeyID || claims.AuthEpoch != authtest.Epoch {
		t.Fatalf("claims %+v err %v", claims, err)
	}
}

// Every property the policy fixes is enforced in both directions: the issuer
// produces it and the verifier rejects any deviation.
func TestVerifyStrictRejectsEveryDeviation(t *testing.T) {
	f := authtest.New(t)
	other := []byte(strings.Repeat("o", 48))
	cases := []struct {
		name   string
		method jwt.SigningMethod
		kid    string
		secret []byte
		mutate func(*auth.Claims)
	}{
		{"HS384", jwt.SigningMethodHS384, authtest.KeyID, authtest.Secret, nil},
		{"HS512", jwt.SigningMethodHS512, authtest.KeyID, authtest.Secret, nil},
		{"wrong secret", jwt.SigningMethodHS256, authtest.KeyID, other, nil},
		{"unknown key id", jwt.SigningMethodHS256, "test-k9", authtest.Secret, nil},
		{"missing key id", jwt.SigningMethodHS256, "", authtest.Secret, nil},
		{"wrong issuer", jwt.SigningMethodHS256, authtest.KeyID, authtest.Secret, func(c *auth.Claims) { c.Issuer = "someone-else" }},
		{"missing issuer", jwt.SigningMethodHS256, authtest.KeyID, authtest.Secret, func(c *auth.Claims) { c.Issuer = "" }},
		{"wrong audience", jwt.SigningMethodHS256, authtest.KeyID, authtest.Secret, func(c *auth.Claims) { c.Audience = jwt.ClaimStrings{"other-app"} }},
		{"extra audience", jwt.SigningMethodHS256, authtest.KeyID, authtest.Secret, func(c *auth.Claims) { c.Audience = append(c.Audience, "other-app") }},
		{"missing exp", jwt.SigningMethodHS256, authtest.KeyID, authtest.Secret, func(c *auth.Claims) { c.ExpiresAt = nil }},
		{"expired", jwt.SigningMethodHS256, authtest.KeyID, authtest.Secret, func(c *auth.Claims) { c.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Minute)) }},
		{"missing iat", jwt.SigningMethodHS256, authtest.KeyID, authtest.Secret, func(c *auth.Claims) { c.IssuedAt = nil }},
		{"iat beyond leeway", jwt.SigningMethodHS256, authtest.KeyID, authtest.Secret, func(c *auth.Claims) { c.IssuedAt = jwt.NewNumericDate(time.Now().Add(2 * time.Minute)) }},
		{"wrong type", jwt.SigningMethodHS256, authtest.KeyID, authtest.Secret, func(c *auth.Claims) { c.Type = "access" }},
		{"zero wallet", jwt.SigningMethodHS256, authtest.KeyID, authtest.Secret, func(c *auth.Claims) { c.Sub = "0x0000000000000000000000000000000000000000" }},
		{"malformed wallet", jwt.SigningMethodHS256, authtest.KeyID, authtest.Secret, func(c *auth.Claims) { c.Sub = "0x1234" }},
		{"non-positive chain", jwt.SigningMethodHS256, authtest.KeyID, authtest.Secret, func(c *auth.Claims) { c.ChainID = 0 }},
		{"missing session id", jwt.SigningMethodHS256, authtest.KeyID, authtest.Secret, func(c *auth.Claims) { c.SessionID = "" }},
		{"zero session version", jwt.SigningMethodHS256, authtest.KeyID, authtest.Secret, func(c *auth.Claims) { c.SessionVersion = 0 }},
		{"older auth epoch", jwt.SigningMethodHS256, authtest.KeyID, authtest.Secret, func(c *auth.Claims) { c.AuthEpoch = authtest.Epoch - 1 }},
		{"newer auth epoch", jwt.SigningMethodHS256, authtest.KeyID, authtest.Secret, func(c *auth.Claims) { c.AuthEpoch = authtest.Epoch + 1 }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			claims := f.ValidClaims(t, walletA)
			if c.mutate != nil {
				c.mutate(&claims)
			}
			token := authtest.Sign(t, c.method, c.kid, c.secret, claims)
			if _, err := f.Verifier.VerifyStrict(token); err == nil {
				t.Fatal("deviation accepted")
			}
		})
	}
	within := f.ValidClaims(t, walletA)
	within.IssuedAt = jwt.NewNumericDate(time.Now().Add(10 * time.Second))
	if _, err := f.Verifier.VerifyStrict(authtest.Sign(t, jwt.SigningMethodHS256, authtest.KeyID, authtest.Secret, within)); err != nil {
		t.Fatalf("iat within leeway must pass: %v", err)
	}
	unsigned := jwt.NewWithClaims(jwt.SigningMethodNone, f.ValidClaims(t, walletA))
	unsigned.Header["kid"] = authtest.KeyID
	none, err := unsigned.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Verifier.VerifyStrict(none); err == nil {
		t.Fatal("alg none accepted")
	}
}

func TestPreviousKeyVerifiesOnlyUntilItsRetirement(t *testing.T) {
	previous := []byte(strings.Repeat("p", 48))
	now := time.Now()
	clock := now
	f := authtest.New(t, func(p *auth.Policy) {
		p.Previous = []auth.Key{{ID: "test-k0", Secret: previous, Retire: now.Add(time.Hour)}}
		p.Now = func() time.Time { return clock }
	})
	token := authtest.Sign(t, jwt.SigningMethodHS256, "test-k0", previous, f.ValidClaims(t, walletA))
	if _, err := f.Verifier.VerifyStrict(token); err != nil {
		t.Fatalf("previous key inside its window: %v", err)
	}
	clock = now.Add(2 * time.Hour)
	if _, err := f.Verifier.VerifyStrict(token); err == nil {
		t.Fatal("a retired key must not verify")
	}
}

func TestAuthenticateChecksThePersistentSession(t *testing.T) {
	f := authtest.New(t)
	token, sid := f.Login(t, walletA, 11155111)
	f.Sessions.RevokeID(sid)
	if _, err := f.Verifier.Authenticate(context.Background(), token); !errors.Is(err, auth.ErrSessionInvalid) {
		t.Fatalf("revoked session: %v", err)
	}
	unknown := authtest.Sign(t, jwt.SigningMethodHS256, authtest.KeyID, authtest.Secret, func() auth.Claims {
		c := f.ValidClaims(t, walletA)
		c.SessionID = authtest.SessionID(t)
		return c
	}())
	if _, err := f.Verifier.Authenticate(context.Background(), unknown); !errors.Is(err, auth.ErrSessionInvalid) {
		t.Fatalf("unknown session: %v", err)
	}
	swapped := f.ValidClaims(t, walletA)
	swapped.Sub = walletB
	if _, err := f.Verifier.Authenticate(context.Background(), authtest.Sign(t, jwt.SigningMethodHS256, authtest.KeyID, authtest.Secret, swapped)); !errors.Is(err, auth.ErrSessionInvalid) {
		t.Fatalf("a session belongs to one wallet: %v", err)
	}
	live, _ := f.Login(t, walletA, 11155111)
	f.Sessions.SetDown(true)
	if _, err := f.Verifier.Authenticate(context.Background(), live); !errors.Is(err, auth.ErrSessionUnavailable) {
		t.Fatalf("an unreachable store must fail closed as unavailable: %v", err)
	}
	none := authtest.New(t, func(p *auth.Policy) { p.Sessions = nil })
	token, _ = none.Login(t, walletA, 1)
	if _, err := none.Verifier.Authenticate(context.Background(), token); !errors.Is(err, auth.ErrSessionUnavailable) {
		t.Fatalf("no session store must mean no private access: %v", err)
	}
}

func TestPolicyRejectsUnsafeConfiguration(t *testing.T) {
	good := auth.Policy{Issuer: "i", Audience: "a", Current: auth.Key{ID: "k1", Secret: authtest.Secret}, Epoch: 1, Leeway: time.Second}
	cases := map[string]func(*auth.Policy){
		"empty issuer":            func(p *auth.Policy) { p.Issuer = " " },
		"empty audience":          func(p *auth.Policy) { p.Audience = "" },
		"zero epoch":              func(p *auth.Policy) { p.Epoch = 0 },
		"short secret":            func(p *auth.Policy) { p.Current.Secret = []byte("short") },
		"bad key id":              func(p *auth.Policy) { p.Current.ID = "k 1" },
		"current with retirement": func(p *auth.Policy) { p.Current.Retire = time.Now() },
		"previous without cutoff": func(p *auth.Policy) { p.Previous = []auth.Key{{ID: "k0", Secret: authtest.Secret}} },
		"duplicate key id":        func(p *auth.Policy) { p.Previous = []auth.Key{{ID: "k1", Secret: authtest.Secret, Retire: time.Now()}} },
		"excessive leeway":        func(p *auth.Policy) { p.Leeway = time.Hour },
	}
	if _, err := auth.NewVerifier(good); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range cases {
		policy := good
		mutate(&policy)
		if _, err := auth.NewVerifier(policy); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	f := authtest.New(t)
	if _, err := f.Verifier.Issue(auth.SessionRef{ID: authtest.SessionID(t), Wallet: walletA, ChainID: 1, Version: 1, Epoch: authtest.Epoch - 1, KeyID: authtest.KeyID}, time.Now(), time.Now().Add(time.Hour)); err == nil {
		t.Fatal("issuing under another epoch must fail")
	}
}
