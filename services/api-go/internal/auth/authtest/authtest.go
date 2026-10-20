// Package authtest builds complete web3 credential fixtures for tests: a real
// Policy-based verifier, an in-memory session store that can be revoked or
// taken down, and helpers that issue genuine tokens or deliberately malformed
// ones. Production code never imports it.
package authtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/golang-jwt/jwt/v5"
)

const (
	Issuer   = "wallet-trade-test"
	Audience = "wallet-trade-web3-test"
	KeyID    = "test-k1"
	Epoch    = int64(7)
)

// Secret is the fixture's current HS256 key (non-secret, tests only).
var Secret = []byte(strings.Repeat("s", 48))

type row struct {
	ref     auth.SessionRef
	expires time.Time
	revoked bool
}

// Sessions is an in-memory auth.SessionChecker.
type Sessions struct {
	mu   sync.Mutex
	rows map[string]row
	down bool
	Now  func() time.Time
}

func (s *Sessions) CheckSession(_ context.Context, ref auth.SessionRef) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.down {
		return false, errors.New("authtest: session store down")
	}
	stored, ok := s.rows[ref.ID]
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	return ok && !stored.revoked && now.Before(stored.expires) && stored.ref == ref, nil
}

// Add persists a session row.
func (s *Sessions) Add(ref auth.SessionRef, expires time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rows == nil {
		s.rows = map[string]row{}
	}
	s.rows[ref.ID] = row{ref: ref, expires: expires}
}

// Stored is one persisted session as the store holds it.
type Stored struct {
	Ref     auth.SessionRef
	Expires time.Time
	Revoked bool
}

// All returns every persisted session, for tests that check what was stored.
func (s *Sessions) All() []Stored {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Stored, 0, len(s.rows))
	for _, stored := range s.rows {
		out = append(out, Stored{Ref: stored.ref, Expires: stored.expires, Revoked: stored.revoked})
	}
	return out
}

// RevokeID marks one session revoked (test helper).
func (s *Sessions) RevokeID(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stored := s.rows[id]
	stored.revoked = true
	s.rows[id] = stored
}

// Create persists a session the way the issuer's store does.
func (s *Sessions) Create(_ context.Context, ref auth.SessionRef, _, expiresAt time.Time) error {
	s.mu.Lock()
	down := s.down
	s.mu.Unlock()
	if down {
		return errors.New("authtest: session store down")
	}
	s.Add(ref, expiresAt)
	return nil
}

// Revoke mirrors web3session.Store.Revoke: it ends exactly the referenced
// session, is idempotent, and never touches a session the reference does not
// match in every field.
func (s *Sessions) Revoke(_ context.Context, ref auth.SessionRef, _, _ string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.down {
		return false, errors.New("authtest: session store down")
	}
	stored, ok := s.rows[ref.ID]
	if !ok || stored.ref != ref {
		return false, ErrNotOwned
	}
	already := stored.revoked
	stored.revoked = true
	s.rows[ref.ID] = stored
	return already, nil
}

// ErrNotOwned mirrors web3session.ErrNotOwned, including its marker method.
var ErrNotOwned error = notOwnedError{}

type notOwnedError struct{}

func (notOwnedError) Error() string  { return "authtest: session does not belong to this wallet" }
func (notOwnedError) NotOwned() bool { return true }

// SetDown makes every lookup fail like an unreachable store.
func (s *Sessions) SetDown(down bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.down = down
}

// Fixture is a verifier plus its session store.
type Fixture struct {
	Verifier *auth.Verifier
	Sessions *Sessions
	Policy   auth.Policy
}

// New builds a fixture; options may adjust the policy before validation.
func New(t testing.TB, options ...func(*auth.Policy)) *Fixture {
	t.Helper()
	f, err := build(options)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// MustNew builds a fixture outside a test, for a package-level fixture shared
// by the tests of one package. It panics on an invalid policy.
func MustNew(options ...func(*auth.Policy)) *Fixture {
	f, err := build(options)
	if err != nil {
		panic(err)
	}
	return f
}

func build(options []func(*auth.Policy)) (*Fixture, error) {
	sessions := &Sessions{}
	policy := auth.Policy{Issuer: Issuer, Audience: Audience, Current: auth.Key{ID: KeyID, Secret: Secret}, Epoch: Epoch, Leeway: 30 * time.Second, Sessions: sessions}
	for _, option := range options {
		option(&policy)
	}
	verifier, err := auth.NewVerifier(policy)
	if err != nil {
		return nil, err
	}
	return &Fixture{Verifier: verifier, Sessions: sessions, Policy: policy}, nil
}

// SessionID returns a random UUIDv4.
func SessionID(t testing.TB) string {
	t.Helper()
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatal(err)
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	v := hex.EncodeToString(raw[:])
	return v[:8] + "-" + v[8:12] + "-" + v[12:16] + "-" + v[16:20] + "-" + v[20:]
}

// SessionCreator persists sessions; both Sessions and web3session.Store
// implement it.
type SessionCreator interface {
	Create(ctx context.Context, ref auth.SessionRef, issuedAt, expiresAt time.Time) error
}

// persist stores a session in whatever store the policy checks against: the
// in-memory Sessions by default, or a real store a test configured (for
// example web3session.Store on a disposable database). A policy without a
// store still gets a genuine token, recorded only in the in-memory Sessions.
func (f *Fixture) persist(t testing.TB, ref auth.SessionRef, issued, expires time.Time) {
	t.Helper()
	var creator SessionCreator = f.Sessions
	if f.Policy.Sessions != nil {
		configured, ok := f.Policy.Sessions.(SessionCreator)
		if !ok {
			t.Fatal("authtest: the policy's session store cannot create sessions")
		}
		creator = configured
	}
	if err := creator.Create(context.Background(), ref, issued, expires); err != nil {
		t.Fatal(err)
	}
}

// Login persists a session for wallet and returns a token the verifier accepts.
func (f *Fixture) Login(t testing.TB, wallet string, chainID int) (token, sessionID string) {
	t.Helper()
	keyID, epoch := f.Verifier.SessionKey()
	ref := auth.SessionRef{ID: SessionID(t), Wallet: strings.ToLower(wallet), ChainID: chainID, Version: 1, Epoch: epoch, KeyID: keyID}
	issued := time.Now().Add(-time.Second)
	expires := issued.Add(10 * time.Minute)
	f.persist(t, ref, issued, expires)
	token, err := f.Verifier.Issue(ref, issued, expires)
	if err != nil {
		t.Fatal(err)
	}
	return token, ref.ID
}

// ValidClaims returns claims for a persisted session, for tests that mutate
// one property and sign the result with Sign.
func (f *Fixture) ValidClaims(t testing.TB, wallet string) auth.Claims {
	t.Helper()
	keyID, epoch := f.Verifier.SessionKey()
	ref := auth.SessionRef{ID: SessionID(t), Wallet: strings.ToLower(wallet), ChainID: 11155111, Version: 1, Epoch: epoch, KeyID: keyID}
	now := time.Now()
	f.persist(t, ref, now.Add(-time.Second), now.Add(10*time.Minute))
	return auth.Claims{Sub: ref.Wallet, ChainID: ref.ChainID, Type: "web3", SessionID: ref.ID, SessionVersion: 1, AuthEpoch: epoch,
		RegisteredClaims: jwt.RegisteredClaims{Issuer: Issuer, Audience: jwt.ClaimStrings{Audience}, IssuedAt: jwt.NewNumericDate(now.Add(-time.Second)), ExpiresAt: jwt.NewNumericDate(now.Add(10 * time.Minute))}}
}

// Sign signs claims with an arbitrary method, key id and secret.
func Sign(t testing.TB, method jwt.SigningMethod, kid string, secret []byte, claims auth.Claims) string {
	t.Helper()
	token := jwt.NewWithClaims(method, claims)
	if kid != "" {
		token.Header["kid"] = kid
	}
	signed, err := token.SignedString(secret)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}
