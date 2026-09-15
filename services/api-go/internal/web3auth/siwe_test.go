package web3auth

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	secp256k1 "github.com/decred/dcrd/dcrec/secp256k1/v4"
	decredecdsa "github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
)

// signEthMessage signs `message` with `priv` using the EIP-191
// personal_sign hash, returning r||s||v hex (v ∈ {0,1}+27).
func signEthMessage(t *testing.T, priv *secp256k1.PrivateKey, message string) string {
	t.Helper()
	prefix := "\x19Ethereum Signed Message:\n"
	hash := keccak256([]byte(prefix + itoa(len(message)) + message))
	sig := decredecdsa.SignCompact(priv, hash, false)
	// decred SignCompact returns v(1)+27 || r(32) || s(32). Re-order to
	// Ethereum's r || s || v(0 or 1, +27).
	r := sig[1:33]
	s := sig[33:65]
	v := sig[0]
	out := append([]byte{}, r...)
	out = append(out, s...)
	out = append(out, v)
	return "0x" + hex.EncodeToString(out)
}

func itoa(n int) string {
	// avoids strconv import in test-only helpers
	if n == 0 {
		return "0"
	}
	buf := [20]byte{}
	idx := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		idx--
		buf[idx] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		idx--
		buf[idx] = '-'
	}
	return string(buf[idx:])
}

// addrFromPriv derives the lowercase 0x address from a private key.
func addrFromPriv(t *testing.T, priv *secp256k1.PrivateKey) string {
	t.Helper()
	pub := priv.PubKey()
	un := pub.SerializeUncompressed()
	addrHash := keccak256(un[1:])
	return "0x" + hex.EncodeToString(addrHash[12:])
}

func TestParseSiweMessage_Canonical(t *testing.T) {
	now := "2026-01-01T00:00:00Z"
	body := "example.com wants you to sign in with your Ethereum account:\n" +
		"0x0000000000000000000000000000000000000001\n" +
		"\n" +
		"Sign in to Example.\n" +
		"\n" +
		"URI: https://example.com\n" +
		"Version: 1\n" +
		"Chain ID: 11155111\n" +
		"Nonce: abcdef1234567890\n" +
		"Issued At: " + now

	m, err := ParseSiweMessage(body)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if m.Domain != "example.com" {
		t.Errorf("Domain = %q", m.Domain)
	}
	if m.Address != "0x0000000000000000000000000000000000000001" {
		t.Errorf("Address = %q", m.Address)
	}
	if m.Statement != "Sign in to Example." {
		t.Errorf("Statement = %q", m.Statement)
	}
	if m.ChainID != 11155111 {
		t.Errorf("ChainID = %d", m.ChainID)
	}
	if m.Nonce != "abcdef1234567890" {
		t.Errorf("Nonce = %q", m.Nonce)
	}
}

func TestParseSiweMessage_RejectsBadInput(t *testing.T) {
	for _, body := range []string{
		"",
		"no heading\n0x" + strings.Repeat("a", 40),
		"x.com wants you to sign in with your Ethereum account:\nnot-an-addr\n\nURI: x\nVersion: 1\nChain ID: 1\nNonce: n\nIssued At: t",
	} {
		if _, err := ParseSiweMessage(body); err == nil {
			t.Errorf("expected error for body=%q", body)
		}
	}
}

func TestRecoverAddress_KnownKey(t *testing.T) {
	priv, _ := secp256k1.GeneratePrivateKey()
	want := addrFromPriv(t, priv)
	message := "example.com wants you to sign in with your Ethereum account:\n" +
		want + "\n" +
		"\n" +
		"URI: https://example.com\n" +
		"Version: 1\n" +
		"Chain ID: 1\n" +
		"Nonce: abc123\n" +
		"Issued At: 2026-01-01T00:00:00Z"
	sig := signEthMessage(t, priv, message)
	got, err := RecoverAddress(message, sig)
	if err != nil {
		t.Fatalf("RecoverAddress err = %v", err)
	}
	if got != strings.ToLower(want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRecoverAddress_RejectsBadSig(t *testing.T) {
	cases := []string{
		"",
		"0xdeadbeef",
		"0x" + strings.Repeat("z", 130),
	}
	for _, sig := range cases {
		if _, err := RecoverAddress("msg", sig); err == nil {
			t.Errorf("expected err for sig=%q", sig)
		}
	}
}

func TestService_NonceLifecycle(t *testing.T) {
	svc := NewService("secret", 0, 0, nil)
	resp, err := svc.GenerateNonceFor(context.Background(), "")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if resp.Nonce == "" {
		t.Errorf("nonce empty")
	}
	if resp.Domain == "" || resp.URI == "" || resp.Statement == "" || resp.ExpirationTime == "" {
		t.Errorf("challenge envelope incomplete: %+v", resp)
	}
	if len(resp.AllowedChainIDs) == 0 {
		t.Errorf("allowedChainIds empty")
	}
}

func TestService_VerifySiwe_FullRoundtrip(t *testing.T) {
	svc := NewService("test-secret", 0, 0, []int{11155111})
	priv, _ := secp256k1.GeneratePrivateKey()
	addr := addrFromPriv(t, priv)

	resp, err := svc.GenerateNonceFor(context.Background(), addr)
	if err != nil {
		t.Fatalf("nonce err = %v", err)
	}

	message := buildMessage(addr, 11155111, resp)
	sig := signEthMessage(t, priv, message)

	out, err := svc.VerifySiwe(context.Background(), VerifyInput{Message: message, Signature: sig})
	if err != nil {
		t.Fatalf("verify err = %v", err)
	}
	if out.Token == "" {
		t.Errorf("token empty")
	}
	if out.SessionExpiresAt == "" {
		t.Errorf("sessionExpiresAt empty")
	}
	if out.Address != addr {
		t.Errorf("address = %q, want %q", out.Address, addr)
	}
	if out.ChainID != 11155111 {
		t.Errorf("chainId = %d", out.ChainID)
	}
}

func TestService_VerifySiwe_NonceReused(t *testing.T) {
	svc := NewService("secret", 0, 0, nil)
	priv, _ := secp256k1.GeneratePrivateKey()
	addr := addrFromPriv(t, priv)
	resp, _ := svc.GenerateNonceFor(context.Background(), addr)
	message := buildMessage(addr, 1, resp)
	sig := signEthMessage(t, priv, message)
	if _, err := svc.VerifySiwe(context.Background(), VerifyInput{Message: message, Signature: sig}); err != nil {
		t.Fatalf("first verify: %v", err)
	}
	if _, err := svc.VerifySiwe(context.Background(), VerifyInput{Message: message, Signature: sig}); err == nil {
		t.Errorf("second verify should fail (nonce already consumed)")
	}
}

func TestService_VerifySiwe_AddressMismatchRejected(t *testing.T) {
	svc := NewService("secret", 0, 0, nil)
	priv, _ := secp256k1.GeneratePrivateKey()
	otherAddr := "0x" + strings.Repeat("1", 40)
	resp, _ := svc.GenerateNonceFor(context.Background(), otherAddr)
	message := buildMessage(otherAddr, 1, resp)
	// Sign with a DIFFERENT key so the recovered address won't match.
	sig := signEthMessage(t, priv, message)
	if _, err := svc.VerifySiwe(context.Background(), VerifyInput{Message: message, Signature: sig}); err == nil {
		t.Errorf("expected mismatch error")
	}
}

func TestService_VerifySiwe_DomainMismatchRejected(t *testing.T) {
	svc := NewService("secret", 0, 0, nil)
	priv, _ := secp256k1.GeneratePrivateKey()
	addr := addrFromPriv(t, priv)
	resp, _ := svc.GenerateNonceFor(context.Background(), addr)
	message := "evil.example wants you to sign in with your Ethereum account:\n" +
		addr + "\n\n" +
		resp.Statement + "\n\n" +
		"URI: " + resp.URI + "\n" +
		"Version: 1\n" +
		"Chain ID: 1\n" +
		"Nonce: " + resp.Nonce + "\n" +
		"Issued At: " + resp.IssuedAt + "\n" +
		"Expiration Time: " + resp.ExpirationTime
	sig := signEthMessage(t, priv, message)
	if _, err := svc.VerifySiwe(context.Background(), VerifyInput{Message: message, Signature: sig}); err == nil {
		t.Errorf("expected domain mismatch error")
	}
}

func TestHandler_NonceGet(t *testing.T) {
	svc := NewService("s", 0, 0, nil)
	mux := Router(svc, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nonce", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d", rec.Code)
	}
}

func TestHandler_NoncePost_AcceptsAddress(t *testing.T) {
	svc := NewService("s", 0, 0, nil)
	mux := Router(svc, nil)
	body := `{"address":"0x0000000000000000000000000000000000000001"}`
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/nonce", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	var out NonceResponse
	_ = json.NewDecoder(rec.Body).Decode(&out)
	if out.Nonce == "" {
		t.Errorf("nonce empty")
	}
	if out.Domain == "" || out.URI == "" || out.Statement == "" || out.ExpirationTime == "" {
		t.Errorf("challenge envelope incomplete: %+v", out)
	}
}

func TestHandler_VerifyMissingBody_400(t *testing.T) {
	mux := Router(NewService("s", 0, 0, nil), nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/verify", strings.NewReader("{")))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d", rec.Code)
	}
}

func TestHandler_VerifyBadMessage_401(t *testing.T) {
	mux := Router(NewService("s", 0, 0, nil), nil)
	body := `{"message":"not a siwe message","signature":"0x" + "00"*65 + "00"}`
	_ = body
	body2 := `{"message":"not a siwe","signature":"0x` + strings.Repeat("00", 65) + `"}`
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/verify", strings.NewReader(body2)))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestHandler_Me_RequiresBearer(t *testing.T) {
	mux := Router(NewService("s", 0, 0, nil), nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/me", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d", rec.Code)
	}
}

// keepers — keep stdlib imports alive in case future tests use them.
var (
	_ = rand.Read
	_ = ecdsa.GenerateKey
)

func buildMessage(addr string, chainID int, challenge NonceResponse) string {
	return challenge.Domain + " wants you to sign in with your Ethereum account:\n" +
		addr + "\n\n" +
		challenge.Statement + "\n\n" +
		"URI: " + challenge.URI + "\n" +
		"Version: 1\n" +
		"Chain ID: " + itoa(chainID) + "\n" +
		"Nonce: " + challenge.Nonce + "\n" +
		"Issued At: " + challenge.IssuedAt + "\n" +
		"Expiration Time: " + challenge.ExpirationTime
}
