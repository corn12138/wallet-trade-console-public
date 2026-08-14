package auth

import (
	"errors"
	"testing"
	"time"
)

func TestUserVerifier_IssueVerifyRoundtrip(t *testing.T) {
	v := NewUserVerifier("access-secret", "refresh-secret", 0, 0)
	access, refresh, err := v.IssueTokens("u-1", "alice", "alice@example.com", []string{"user", "admin"})
	if err != nil {
		t.Fatalf("IssueTokens err = %v", err)
	}
	if access == "" || refresh == "" {
		t.Fatalf("tokens empty: access=%q refresh=%q", access, refresh)
	}

	claims, err := v.VerifyAccess(access)
	if err != nil {
		t.Fatalf("VerifyAccess err = %v", err)
	}
	if claims.Sub != "u-1" || claims.Username != "alice" || claims.Email != "alice@example.com" {
		t.Errorf("claims = %+v, want u-1/alice/email", claims)
	}
	if claims.Type != "user" {
		t.Errorf("Type = %q, want user", claims.Type)
	}

	rclaims, err := v.VerifyRefresh(refresh)
	if err != nil {
		t.Fatalf("VerifyRefresh err = %v", err)
	}
	if rclaims.Type != "refresh" {
		t.Errorf("Type = %q, want refresh", rclaims.Type)
	}
}

func TestUserVerifier_WrongType(t *testing.T) {
	v := NewUserVerifier("a", "b", 0, 0)
	access, refresh, _ := v.IssueTokens("u", "n", "e", nil)

	if _, err := v.VerifyRefresh(access); !errors.Is(err, ErrUserTokenWrongType) && !errors.Is(err, ErrUserTokenInvalid) {
		t.Errorf("VerifyRefresh(access) err = %v, want wrong-type or invalid (different secret)", err)
	}
	// VerifyAccess on a refresh signed with the same secret would fail
	// type check; the test setup uses different secrets so it surfaces
	// as ErrUserTokenInvalid first — either is acceptable.
	if _, err := v.VerifyAccess(refresh); err == nil {
		t.Errorf("VerifyAccess(refresh) should reject, got nil")
	}
}

func TestUserVerifier_Expired(t *testing.T) {
	v := NewUserVerifier("a", "b", 1*time.Nanosecond, 1*time.Nanosecond)
	access, _, _ := v.IssueTokens("u", "n", "e", nil)
	time.Sleep(10 * time.Millisecond)
	if _, err := v.VerifyAccess(access); err == nil {
		t.Errorf("VerifyAccess on expired token should fail")
	}
}

func TestPasswordHashAndCompare(t *testing.T) {
	hash, err := HashPassword("supersecret123")
	if err != nil {
		t.Fatalf("HashPassword err = %v", err)
	}
	if err := ComparePassword(hash, "supersecret123"); err != nil {
		t.Errorf("Compare match: err = %v", err)
	}
	if err := ComparePassword(hash, "wrong"); err == nil {
		t.Errorf("Compare wrong: want err, got nil")
	}
}

func TestCsrfSigner_RoundtripAndExpired(t *testing.T) {
	s := NewCsrfSigner("csrf-secret", 24*time.Hour)
	tok := s.GenerateToken("user-1")
	if tok == "" {
		t.Fatal("GenerateToken returned empty")
	}
	if !s.ValidateToken(tok, "user-1") {
		t.Errorf("ValidateToken(matching) returned false")
	}
	if s.ValidateToken(tok, "other-user") {
		t.Errorf("ValidateToken(wrong user) returned true")
	}

	// Expired
	expired := NewCsrfSigner("csrf-secret", 1*time.Nanosecond)
	tok2 := expired.GenerateToken("u")
	time.Sleep(5 * time.Millisecond)
	if expired.ValidateToken(tok2, "u") {
		t.Errorf("ValidateToken(expired) returned true")
	}

	// Missing secret
	none := NewCsrfSigner("", 0)
	if none.GenerateToken("u") != "" {
		t.Errorf("GenerateToken with empty secret should return empty")
	}
}

func TestCsrfSigner_TamperedSignatureRejected(t *testing.T) {
	s := NewCsrfSigner("secret", 24*time.Hour)
	tok := s.GenerateToken("u")
	// Flip a byte in the middle.
	tampered := []byte(tok)
	tampered[len(tampered)/2] = 'X'
	if s.ValidateToken(string(tampered), "u") {
		t.Errorf("tampered token validated")
	}
}

// TestHashPassword_NodeBcryptTruncationParity locks the 2026-07-02 fix: Go's
// x/crypto/bcrypt refuses inputs >72 bytes, which 500'd every classic
// login/register/refresh (issueAndPersist bcrypts the ~200-byte refresh JWT),
// while the deployed NestJS silently truncates (node bcrypt). Hash/Compare
// must accept JWT-length inputs and mirror node's [:72] semantics so hashes
// interoperate across both stacks during the cutover window.
func TestHashPassword_NodeBcryptTruncationParity(t *testing.T) {
	// A realistic refresh-JWT-sized input (>72 bytes, three dot segments).
	long := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9." +
		"eyJzdWIiOiJ1LTEiLCJ1c2VybmFtZSI6ImFsaWNlIiwidHlwZSI6InJlZnJlc2gifQ." +
		"c2lnbmF0dXJlLXNlZ21lbnQtcGFkZGluZy1wYWRkaW5nLXBhZGRpbmc"
	if len(long) <= 72 {
		t.Fatalf("test input must exceed 72 bytes, got %d", len(long))
	}

	hash, err := HashPassword(long)
	if err != nil {
		t.Fatalf("HashPassword(long) err = %v — JWT-length inputs must hash (node parity)", err)
	}
	if err := ComparePassword(hash, long); err != nil {
		t.Fatalf("ComparePassword(hash, long) err = %v — roundtrip must verify", err)
	}

	// Node-parity truncation semantics (documented, deliberate): only the
	// first 72 bytes participate, so a suffix change after byte 72 still
	// matches, while a prefix change does not.
	sameFirst72 := long[:72] + "-completely-different-suffix"
	if err := ComparePassword(hash, sameFirst72); err != nil {
		t.Fatalf("ComparePassword(hash, sameFirst72) err = %v — node bcrypt truncates at 72 bytes", err)
	}
	differentPrefix := "X" + long[1:]
	if err := ComparePassword(hash, differentPrefix); err == nil {
		t.Fatal("ComparePassword(hash, differentPrefix) = nil — prefix changes must NOT match")
	}
}
