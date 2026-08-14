// Package web3auth ports legacy NestJS web3-auth/ — SIWE
// (EIP-4361) nonce/verify/me endpoints.
//
// The crypto path is hand-rolled to keep the binary independent of
// go-ethereum: we use decred's secp256k1 for ECDSA recovery and
// x/crypto/sha3 for keccak256. The SIWE message parser is intentionally
// strict — it accepts the canonical EIP-4361 format the spruceid siwe
// JS library emits, which is what the frontend signs.
package web3auth

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	secp256k1 "github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"golang.org/x/crypto/sha3"
)

// Errors returned by the SIWE module. Handlers map these to HTTP codes.
var (
	ErrBadMessage      = errors.New("invalid SIWE message")
	ErrBadSignature    = errors.New("invalid signature")
	ErrAddressMismatch = errors.New("recovered address does not match SIWE message address")
	ErrNonceUnknown    = errors.New("nonce not recognized (already consumed or expired)")
	ErrNonceExpired    = errors.New("nonce expired")
	ErrNonceMismatch   = errors.New("nonce does not match challenge")
	ErrDomainMismatch  = errors.New("SIWE domain not allowed")
	ErrChainNotAllowed = errors.New("SIWE chain id not allowed")
)

// SiweMessage holds the canonical EIP-4361 fields the verifier needs.
// Optional fields (expirationTime, notBefore, requestId, resources)
// are accepted but only expirationTime is checked.
type SiweMessage struct {
	Domain         string
	Address        string
	Statement      string
	URI            string
	Version        string
	ChainID        int
	Nonce          string
	IssuedAt       string
	ExpirationTime string
	NotBefore      string
	Raw            string // the message bytes that were signed
}

var (
	addressRE = regexp.MustCompile(`^0x[a-fA-F0-9]{40}$`)
	hexSigRE  = regexp.MustCompile(`^0x[a-fA-F0-9]{130}$`)
	headingRE = regexp.MustCompile(`^(?P<domain>[^ ]+) wants you to sign in with your Ethereum account:$`)
	intLineRE = regexp.MustCompile(`^Chain ID:\s*(\d+)$`)
)

// ParseSiweMessage parses the canonical EIP-4361 format. Returns
// ErrBadMessage on any structural error.
func ParseSiweMessage(message string) (SiweMessage, error) {
	if message == "" {
		return SiweMessage{}, fmt.Errorf("%w: empty", ErrBadMessage)
	}
	lines := strings.Split(message, "\n")
	if len(lines) < 8 {
		return SiweMessage{}, fmt.Errorf("%w: too few lines", ErrBadMessage)
	}

	m := SiweMessage{Raw: message}

	// Line 0: "<domain> wants you to sign in with your Ethereum account:"
	heading := headingRE.FindStringSubmatch(lines[0])
	if heading == nil {
		return SiweMessage{}, fmt.Errorf("%w: bad heading", ErrBadMessage)
	}
	m.Domain = heading[1]

	// Line 1: address
	addr := strings.TrimSpace(lines[1])
	if !addressRE.MatchString(addr) {
		return SiweMessage{}, fmt.Errorf("%w: bad address", ErrBadMessage)
	}
	m.Address = addr

	// Line 2: blank. Line 3: statement (or blank if no statement,
	// in which case the URI block starts directly).
	idx := 2
	if lines[idx] != "" {
		return SiweMessage{}, fmt.Errorf("%w: expected blank after address", ErrBadMessage)
	}
	idx++

	// Optional statement (single line) then blank, OR directly key:value block.
	if idx < len(lines) && !strings.HasPrefix(lines[idx], "URI:") {
		m.Statement = lines[idx]
		idx++
		if idx < len(lines) && lines[idx] == "" {
			idx++
		}
	}

	for ; idx < len(lines); idx++ {
		line := lines[idx]
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "URI: "):
			m.URI = strings.TrimPrefix(line, "URI: ")
		case strings.HasPrefix(line, "Version: "):
			m.Version = strings.TrimPrefix(line, "Version: ")
		case strings.HasPrefix(line, "Chain ID: "):
			match := intLineRE.FindStringSubmatch(line)
			if match == nil {
				return SiweMessage{}, fmt.Errorf("%w: bad chainId", ErrBadMessage)
			}
			n, _ := strconv.Atoi(match[1])
			m.ChainID = n
		case strings.HasPrefix(line, "Nonce: "):
			m.Nonce = strings.TrimPrefix(line, "Nonce: ")
		case strings.HasPrefix(line, "Issued At: "):
			m.IssuedAt = strings.TrimPrefix(line, "Issued At: ")
		case strings.HasPrefix(line, "Expiration Time: "):
			m.ExpirationTime = strings.TrimPrefix(line, "Expiration Time: ")
		case strings.HasPrefix(line, "Not Before: "):
			m.NotBefore = strings.TrimPrefix(line, "Not Before: ")
		case strings.HasPrefix(line, "Resources:"), strings.HasPrefix(line, "- "), strings.HasPrefix(line, "Request ID:"):
			// optional fields we don't check
		default:
			return SiweMessage{}, fmt.Errorf("%w: unknown line %q", ErrBadMessage, line)
		}
	}

	if m.URI == "" || m.Version == "" || m.Nonce == "" || m.IssuedAt == "" || m.ChainID == 0 {
		return SiweMessage{}, fmt.Errorf("%w: missing required field", ErrBadMessage)
	}
	return m, nil
}

// RecoverAddress runs the SIWE signature recovery: hashes the message
// with EIP-191 personal_sign prefix, recovers the secp256k1 public
// key, and derives the Ethereum address. Returns the lowercased
// 0x-prefixed address.
func RecoverAddress(message, signatureHex string) (string, error) {
	if !hexSigRE.MatchString(signatureHex) {
		return "", fmt.Errorf("%w: signature format", ErrBadSignature)
	}
	sigBytes, err := hex.DecodeString(strings.TrimPrefix(signatureHex, "0x"))
	if err != nil || len(sigBytes) != 65 {
		return "", fmt.Errorf("%w: hex decode", ErrBadSignature)
	}

	// Ethereum signatures: r (32) || s (32) || v (1, 27 or 28 or 0/1).
	v := sigBytes[64]
	if v == 27 || v == 28 {
		v -= 27
	}
	if v != 0 && v != 1 {
		return "", fmt.Errorf("%w: bad recovery id %d", ErrBadSignature, v)
	}

	// EIP-191 personal_sign hash.
	prefix := fmt.Sprintf("\x19Ethereum Signed Message:\n%d", len(message))
	hash := keccak256([]byte(prefix + message))

	// Decred's RecoverCompact expects [v(1)+27 || r(32) || s(32)] but we
	// can call the lower-level signature reconstruction.
	r := new(secp256k1.ModNScalar)
	if overflow := r.SetByteSlice(sigBytes[:32]); overflow {
		return "", fmt.Errorf("%w: r overflows curve order", ErrBadSignature)
	}
	s := new(secp256k1.ModNScalar)
	if overflow := s.SetByteSlice(sigBytes[32:64]); overflow {
		return "", fmt.Errorf("%w: s overflows curve order", ErrBadSignature)
	}

	// Decred's RecoverCompact uses compact format: v(1)+27 || r || s.
	compact := make([]byte, 65)
	compact[0] = v + 27
	copy(compact[1:33], sigBytes[:32])
	copy(compact[33:65], sigBytes[32:64])

	pub, _, err := ecdsa.RecoverCompact(compact, hash)
	if err != nil {
		return "", fmt.Errorf("%w: recover compact: %v", ErrBadSignature, err)
	}

	// Ethereum address = last 20 bytes of keccak256(uncompressedPubKey[1:]).
	uncompressed := pub.SerializeUncompressed() // 65 bytes, leading 0x04
	addrHash := keccak256(uncompressed[1:])
	addr := "0x" + strings.ToLower(hex.EncodeToString(addrHash[12:]))
	return addr, nil
}

func keccak256(data []byte) []byte {
	h := sha3.NewLegacyKeccak256()
	h.Write(data)
	return h.Sum(nil)
}

// GenerateNonce returns a 16-byte random alphanumeric nonce matching
// the spruceid siwe-js generateNonce() output shape (base32-ish but
// we use base64-without-padding for brevity — verifiers only check
// it as an opaque string).
func GenerateNonce() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "deadbeefdeadbeef" // last-resort fallback; should never happen
	}
	s := base64.RawURLEncoding.EncodeToString(b[:])
	// Strip non-alphanumerics so siwe-js's regex-validated nonce field
	// accepts the response.
	return strings.NewReplacer("-", "", "_", "").Replace(s)
}

// NormalizeAddress returns the lowercase 0x form (case-insensitive
// equality for SIWE messages).
func NormalizeAddress(addr string) string {
	return strings.ToLower(strings.TrimSpace(addr))
}

// Now is overridable for tests.
var Now = func() time.Time { return time.Now() }
