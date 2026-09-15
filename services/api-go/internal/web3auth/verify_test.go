package web3auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	secp256k1 "github.com/decred/dcrd/dcrec/secp256k1/v4"
	decredecdsa2 "github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
)

// decredSignCompact is aliased so the helper below reads without the long
// package qualifier.
var decredSignCompact = func(priv *secp256k1.PrivateKey, hash []byte) []byte {
	return decredecdsa2.SignCompact(priv, hash, false)
}

// withFrozenClock pins Now for the duration of a test so time-field assertions
// are not flaky, and restores it afterwards.
func withFrozenClock(t *testing.T, at time.Time) {
	t.Helper()
	prev := Now
	Now = func() time.Time { return at }
	t.Cleanup(func() { Now = prev })
}

func messageWithLines(addr string, chainID int, challenge NonceResponse, extra ...string) string {
	base := buildMessage(addr, chainID, challenge)
	for _, line := range extra {
		base += "\n" + line
	}
	return base
}

func TestVerifySiwe_RejectsIssuedAtInTheFuture(t *testing.T) {
	svc := NewService("secret", 0, 0, []int{11155111})
	priv, _ := secp256k1.GeneratePrivateKey()
	addr := addrFromPriv(t, priv)
	challenge, err := svc.GenerateNonceFor(context.Background(), addr)
	if err != nil {
		t.Fatalf("nonce: %v", err)
	}
	// Beyond DefaultMaxClockSkew, so it cannot be a wallet clock a few
	// seconds ahead.
	challenge.IssuedAt = Now().Add(30 * time.Minute).UTC().Format(time.RFC3339)
	message := buildMessage(addr, 11155111, challenge)

	_, err = svc.VerifySiwe(context.Background(), VerifyInput{
		Message: message, Signature: signEthMessage(t, priv, message),
	})
	if !errors.Is(err, ErrBadMessage) {
		t.Fatalf("err = %v, want ErrBadMessage", err)
	}
	if !strings.Contains(err.Error(), "issuedAt is in the future") {
		t.Errorf("error should name the field, got %q", err)
	}
}

func TestVerifySiwe_AcceptsIssuedAtWithinClockSkew(t *testing.T) {
	svc := NewService("secret", 0, 0, []int{11155111})
	priv, _ := secp256k1.GeneratePrivateKey()
	addr := addrFromPriv(t, priv)
	challenge, _ := svc.GenerateNonceFor(context.Background(), addr)
	challenge.IssuedAt = Now().Add(30 * time.Second).UTC().Format(time.RFC3339)
	message := buildMessage(addr, 11155111, challenge)

	if _, err := svc.VerifySiwe(context.Background(), VerifyInput{
		Message: message, Signature: signEthMessage(t, priv, message),
	}); err != nil {
		t.Fatalf("a wallet clock 30s fast must still log in: %v", err)
	}
}

func TestVerifySiwe_RejectsNotBeforeInTheFuture(t *testing.T) {
	svc := NewService("secret", 0, 0, []int{11155111})
	priv, _ := secp256k1.GeneratePrivateKey()
	addr := addrFromPriv(t, priv)
	challenge, _ := svc.GenerateNonceFor(context.Background(), addr)
	notBefore := Now().Add(time.Hour).UTC().Format(time.RFC3339)
	message := messageWithLines(addr, 11155111, challenge, "Not Before: "+notBefore)

	_, err := svc.VerifySiwe(context.Background(), VerifyInput{
		Message: message, Signature: signEthMessage(t, priv, message),
	})
	if !errors.Is(err, ErrBadMessage) || !strings.Contains(err.Error(), "notBefore") {
		t.Fatalf("err = %v, want a notBefore rejection", err)
	}
}

func TestVerifySiwe_AcceptsNotBeforeAlreadyPassed(t *testing.T) {
	svc := NewService("secret", 0, 0, []int{11155111})
	priv, _ := secp256k1.GeneratePrivateKey()
	addr := addrFromPriv(t, priv)
	challenge, _ := svc.GenerateNonceFor(context.Background(), addr)
	notBefore := Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	message := messageWithLines(addr, 11155111, challenge, "Not Before: "+notBefore)

	if _, err := svc.VerifySiwe(context.Background(), VerifyInput{
		Message: message, Signature: signEthMessage(t, priv, message),
	}); err != nil {
		t.Fatalf("a past notBefore must be accepted: %v", err)
	}
}

func TestVerifySiwe_RejectsExpiredChallenge(t *testing.T) {
	base := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	withFrozenClock(t, base)
	svc := NewService("secret", 0, time.Minute, []int{11155111})
	priv, _ := secp256k1.GeneratePrivateKey()
	addr := addrFromPriv(t, priv)
	challenge, _ := svc.GenerateNonceFor(context.Background(), addr)
	message := buildMessage(addr, 11155111, challenge)
	sig := signEthMessage(t, priv, message)

	Now = func() time.Time { return base.Add(2 * time.Minute) }
	if _, err := svc.VerifySiwe(context.Background(), VerifyInput{Message: message, Signature: sig}); !errors.Is(err, ErrNonceExpired) {
		t.Fatalf("err = %v, want ErrNonceExpired", err)
	}
}

func TestVerifySiwe_RejectsChainOutsideTheIssuedChallenge(t *testing.T) {
	svc := NewService("secret", 0, 0, []int{11155111})
	priv, _ := secp256k1.GeneratePrivateKey()
	addr := addrFromPriv(t, priv)
	challenge, _ := svc.GenerateNonceFor(context.Background(), addr)
	message := buildMessage(addr, 1, challenge) // mainnet, not in the allowed set

	_, err := svc.VerifySiwe(context.Background(), VerifyInput{
		Message: message, Signature: signEthMessage(t, priv, message),
	})
	if !errors.Is(err, ErrChainNotAllowed) {
		t.Fatalf("err = %v, want ErrChainNotAllowed", err)
	}
}

func TestVerifySiwe_RejectsMismatchedRequestOrigin(t *testing.T) {
	svc := NewService("secret", 0, 0, []int{11155111})
	priv, _ := secp256k1.GeneratePrivateKey()
	addr := addrFromPriv(t, priv)
	challenge, _ := svc.GenerateNonceFor(context.Background(), addr)
	message := buildMessage(addr, 11155111, challenge)

	_, err := svc.VerifySiwe(context.Background(), VerifyInput{
		Message: message, Signature: signEthMessage(t, priv, message),
		Origin: "https://phishing.example",
	})
	if !errors.Is(err, ErrDomainMismatch) {
		t.Fatalf("err = %v, want ErrDomainMismatch", err)
	}
}

func TestVerifySiwe_AcceptsMatchingAndAbsentOrigin(t *testing.T) {
	for _, origin := range []string{"", "null"} {
		svc := NewService("secret", 0, 0, []int{11155111})
		priv, _ := secp256k1.GeneratePrivateKey()
		addr := addrFromPriv(t, priv)
		challenge, _ := svc.GenerateNonceFor(context.Background(), addr)
		message := buildMessage(addr, 11155111, challenge)
		if _, err := svc.VerifySiwe(context.Background(), VerifyInput{
			Message: message, Signature: signEthMessage(t, priv, message), Origin: origin,
		}); err != nil {
			t.Fatalf("origin %q must be accepted (non-browser client): %v", origin, err)
		}
	}

	svc := NewService("secret", 0, 0, []int{11155111})
	priv, _ := secp256k1.GeneratePrivateKey()
	addr := addrFromPriv(t, priv)
	challenge, _ := svc.GenerateNonceFor(context.Background(), addr)
	message := buildMessage(addr, 11155111, challenge)
	if _, err := svc.VerifySiwe(context.Background(), VerifyInput{
		Message: message, Signature: signEthMessage(t, priv, message), Origin: challenge.URI,
	}); err != nil {
		t.Fatalf("the challenge's own origin must be accepted: %v", err)
	}
}

// The body must not be able to choose the origin it is checked against.
func TestHandler_VerifyOriginComesFromTheHeaderNotTheBody(t *testing.T) {
	svc := NewService("secret", 0, 0, []int{11155111})
	priv, _ := secp256k1.GeneratePrivateKey()
	addr := addrFromPriv(t, priv)
	challenge, _ := svc.GenerateNonceFor(context.Background(), addr)
	message := buildMessage(addr, 11155111, challenge)
	sig := signEthMessage(t, priv, message)

	body := `{"message":` + quoteJSON(message) + `,"signature":"` + sig + `","origin":"` + challenge.URI + `"}`
	req := httptest.NewRequest(http.MethodPost, "/verify", strings.NewReader(body))
	req.Header.Set("Origin", "https://phishing.example")
	rec := httptest.NewRecorder()
	Router(svc, nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 — a body-supplied origin must not win", rec.Code)
	}
}

// One issued challenge funds exactly one attempt, so a wrong signature cannot
// be retried against it.
func TestVerifySiwe_FailedAttemptBurnsTheChallenge(t *testing.T) {
	svc := NewService("secret", 0, 0, []int{11155111})
	priv, _ := secp256k1.GeneratePrivateKey()
	other, _ := secp256k1.GeneratePrivateKey()
	addr := addrFromPriv(t, priv)
	challenge, _ := svc.GenerateNonceFor(context.Background(), addr)
	message := buildMessage(addr, 11155111, challenge)

	if _, err := svc.VerifySiwe(context.Background(), VerifyInput{
		Message: message, Signature: signEthMessage(t, other, message),
	}); err == nil {
		t.Fatal("a signature from the wrong key must not verify")
	}
	if _, err := svc.VerifySiwe(context.Background(), VerifyInput{
		Message: message, Signature: signEthMessage(t, priv, message),
	}); !errors.Is(err, ErrNonceUnknown) {
		t.Fatalf("retry err = %v, want ErrNonceUnknown", err)
	}
}

// --- EIP-1271 through the full verify path ---

// contractOwnerVerifier answers for one address on one chain, checking the
// signature the way a Safe-style account would.
type contractOwnerVerifier struct {
	chainID int
	address string
	priv    *secp256k1.PrivateKey
	t       *testing.T
	calls   int
}

func (c *contractOwnerVerifier) SupportsChain(chainID int) bool { return chainID == c.chainID }

func (c *contractOwnerVerifier) IsValidSignature(_ context.Context, chainID int, address string, digest, signature []byte) (bool, error) {
	c.calls++
	if chainID != c.chainID || !strings.EqualFold(address, c.address) {
		return false, nil
	}
	if len(digest) != 32 {
		c.t.Fatalf("verifier received a %d-byte digest", len(digest))
	}
	want := signCompactOverDigest(c.t, c.priv, digest)
	return string(signature) == string(want), nil
}

func TestVerifySiwe_ContractWalletAuthenticatesWhenTheAccountAccepts(t *testing.T) {
	ownerKey, _ := secp256k1.GeneratePrivateKey()
	walletAddress := "0x" + strings.Repeat("cd", 20)
	verifier := &contractOwnerVerifier{chainID: 11155111, address: walletAddress, priv: ownerKey, t: t}
	svc := NewServiceWithConfig("secret", Config{
		AllowedChainIDs:  []int{11155111},
		ContractVerifier: verifier,
	})

	challenge, _ := svc.GenerateNonceFor(context.Background(), walletAddress)
	message := buildMessage(walletAddress, 11155111, challenge)
	// The wallet's owner signs the same EIP-191 digest the EOA path recovers.
	sig := "0x" + toHex(signCompactOverDigest(t, ownerKey, PersonalSignDigest(message)))

	out, err := svc.VerifySiwe(context.Background(), VerifyInput{Message: message, Signature: sig})
	if err != nil {
		t.Fatalf("contract wallet login: %v", err)
	}
	if out.Address != walletAddress {
		t.Errorf("session address = %q, want the contract account %q", out.Address, walletAddress)
	}
	if verifier.calls != 1 {
		t.Errorf("verifier calls = %d, want 1", verifier.calls)
	}
}

func TestVerifySiwe_ContractWalletRejectionFails(t *testing.T) {
	ownerKey, _ := secp256k1.GeneratePrivateKey()
	strangerKey, _ := secp256k1.GeneratePrivateKey()
	walletAddress := "0x" + strings.Repeat("cd", 20)
	svc := NewServiceWithConfig("secret", Config{
		AllowedChainIDs: []int{11155111},
		ContractVerifier: &contractOwnerVerifier{
			chainID: 11155111, address: walletAddress, priv: ownerKey, t: t,
		},
	})

	challenge, _ := svc.GenerateNonceFor(context.Background(), walletAddress)
	message := buildMessage(walletAddress, 11155111, challenge)
	sig := "0x" + toHex(signCompactOverDigest(t, strangerKey, PersonalSignDigest(message)))

	if _, err := svc.VerifySiwe(context.Background(), VerifyInput{Message: message, Signature: sig}); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("err = %v, want ErrBadSignature", err)
	}
}

// An EOA signature must never be routed through the contract verifier: the
// fallback is a fallback, not a second chance to be asked about a key that
// already answered.
func TestVerifySiwe_EOAPathDoesNotConsultTheContractVerifier(t *testing.T) {
	ownerKey, _ := secp256k1.GeneratePrivateKey()
	verifier := &contractOwnerVerifier{chainID: 11155111, address: "0xdead", priv: ownerKey, t: t}
	svc := NewServiceWithConfig("secret", Config{
		AllowedChainIDs:  []int{11155111},
		ContractVerifier: verifier,
	})
	priv, _ := secp256k1.GeneratePrivateKey()
	addr := addrFromPriv(t, priv)
	challenge, _ := svc.GenerateNonceFor(context.Background(), addr)
	message := buildMessage(addr, 11155111, challenge)

	if _, err := svc.VerifySiwe(context.Background(), VerifyInput{
		Message: message, Signature: signEthMessage(t, priv, message),
	}); err != nil {
		t.Fatalf("EOA login: %v", err)
	}
	if verifier.calls != 0 {
		t.Errorf("contract verifier consulted %d times for an EOA login", verifier.calls)
	}
}

// With no verifier configured, a contract wallet is refused — never assumed
// valid because the API happens not to be able to check.
func TestVerifySiwe_NoVerifierRefusesNonRecoverableSignature(t *testing.T) {
	svc := NewService("secret", 0, 0, []int{11155111})
	walletAddress := "0x" + strings.Repeat("cd", 20)
	challenge, _ := svc.GenerateNonceFor(context.Background(), walletAddress)
	message := buildMessage(walletAddress, 11155111, challenge)

	// A contract signature is not 65 bytes, so recovery cannot even parse it.
	if _, err := svc.VerifySiwe(context.Background(), VerifyInput{
		Message: message, Signature: "0x" + strings.Repeat("ab", 96),
	}); err == nil {
		t.Fatal("a contract signature must not authenticate without a verifier")
	}
}

func TestVerifySiwe_UnsupportedChainForContractWalletFailsClosed(t *testing.T) {
	ownerKey, _ := secp256k1.GeneratePrivateKey()
	walletAddress := "0x" + strings.Repeat("cd", 20)
	svc := NewServiceWithConfig("secret", Config{
		AllowedChainIDs: []int{11155111},
		// Verifier only knows Anvil, but the challenge allows Sepolia.
		ContractVerifier: &contractOwnerVerifier{
			chainID: 31337, address: walletAddress, priv: ownerKey, t: t,
		},
	})
	challenge, _ := svc.GenerateNonceFor(context.Background(), walletAddress)
	message := buildMessage(walletAddress, 11155111, challenge)
	sig := "0x" + toHex(signCompactOverDigest(t, ownerKey, PersonalSignDigest(message)))

	if _, err := svc.VerifySiwe(context.Background(), VerifyInput{
		Message: message, Signature: sig,
	}); !errors.Is(err, ErrContractVerificationUnavailable) {
		t.Fatalf("err = %v, want ErrContractVerificationUnavailable", err)
	}
}

// --- shared test helpers ---

// signCompactOverDigest signs an arbitrary 32-byte digest and returns the
// Ethereum r||s||v layout, so a test can build a signature over the same
// EIP-191 digest the contract path passes to isValidSignature.
func signCompactOverDigest(t *testing.T, priv *secp256k1.PrivateKey, digest []byte) []byte {
	t.Helper()
	sig := decredSignCompact(priv, digest)
	out := append([]byte{}, sig[1:33]...) // r
	out = append(out, sig[33:65]...)      // s
	return append(out, sig[0])            // v (already +27)
}

func toHex(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&0x0f])
	}
	return string(out)
}

// quoteJSON escapes a SIWE message for embedding in a JSON body literal.
func quoteJSON(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// acceptanceKey returns a throwaway keypair and its address. The key exists
// only inside the test process and is never written anywhere.
func acceptanceKey(t *testing.T) (*secp256k1.PrivateKey, string) {
	t.Helper()
	priv, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return priv, addrFromPriv(t, priv)
}

// A deployment that serves several origins must keep working from all of them:
// the challenge carries only the first, so checking against it alone would
// lock out every other configured origin.
func TestVerifySiwe_AcceptsAnySecondaryConfiguredOrigin(t *testing.T) {
	svc := NewServiceWithConfig("secret", Config{
		AllowedChainIDs: []int{11155111},
		AllowedDomains:  []string{"app.example.com"},
		AllowedURIs:     []string{"https://app.example.com", "https://preview.example.com"},
	})
	priv, addr := acceptanceKey(t)
	challenge, _ := svc.GenerateNonceFor(context.Background(), addr)
	if challenge.URI != "https://app.example.com" {
		t.Fatalf("challenge uri = %q, expected the first configured uri", challenge.URI)
	}
	message := buildMessage(addr, 11155111, challenge)

	if _, err := svc.VerifySiwe(context.Background(), VerifyInput{
		Message: message, Signature: signEthMessage(t, priv, message),
		Origin: "https://preview.example.com",
	}); err != nil {
		t.Fatalf("a second configured origin must be accepted: %v", err)
	}
}

func TestVerifySiwe_RejectsAnUnconfiguredOrigin(t *testing.T) {
	svc := NewServiceWithConfig("secret", Config{
		AllowedChainIDs: []int{11155111},
		AllowedDomains:  []string{"app.example.com"},
		AllowedURIs:     []string{"https://app.example.com", "https://preview.example.com"},
	})
	priv, addr := acceptanceKey(t)
	challenge, _ := svc.GenerateNonceFor(context.Background(), addr)
	message := buildMessage(addr, 11155111, challenge)

	if _, err := svc.VerifySiwe(context.Background(), VerifyInput{
		Message: message, Signature: signEthMessage(t, priv, message),
		Origin: "https://app.example.com.evil.test",
	}); !errors.Is(err, ErrDomainMismatch) {
		t.Fatalf("err = %v, want ErrDomainMismatch — a suffix must not match", err)
	}
}
