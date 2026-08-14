// Package abi implements just enough Solidity ABI encoding to build the
// fixed-shape build-tx payloads the strangler-fig replaces from
// legacy NestJS swap, legacy NestJS earn, and
// legacy NestJS security.
//
// Surface:
//   - Selector(signature) = keccak256(canonical_sig)[:4]
//   - EncodeUint256(*big.Int), EncodeAddress(string) — static heads
//   - EncodeAddressArrayArg([]string)                — dynamic arg
//   - EncodeCall(sig, ...static)                     — all-static calls
//   - EncodeCallArgs(sig, ...Arg)                    — mixed static/dynamic
//   - ParseUnits(decimal, decimals) → *big.Int       — viem parseUnits
//
// String/bytes dynamic types and tuples aren't wired yet — extend when
// a use lands rather than pulling in go-ethereum's full ABI package.
package abi

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"golang.org/x/crypto/sha3"
)

// ErrInvalidAddress is returned by EncodeAddress when the input fails
// the basic 0x + 40-hex shape check.
var ErrInvalidAddress = errors.New("abi: invalid address")

var addressShape = mustHexCheck

// Selector returns the first 4 bytes of keccak256(signature). The
// signature MUST be the canonical Solidity form, e.g.
// "approve(address,uint256)" — no spaces, no parameter names.
func Selector(signature string) []byte {
	h := sha3.NewLegacyKeccak256()
	h.Write([]byte(signature))
	return h.Sum(nil)[:4]
}

// EventTopic0 returns the full 32-byte keccak256 of an event signature.
// Used by the indexer to identify events by their topics[0] hash.
func EventTopic0(signature string) [32]byte {
	h := sha3.NewLegacyKeccak256()
	h.Write([]byte(signature))
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// EncodeUint256 left-pads the big-endian representation of v to 32
// bytes. v must be non-negative; a negative value would need uint256
// two's-complement wrapping which none of the ported endpoints emit.
func EncodeUint256(v *big.Int) ([]byte, error) {
	if v == nil {
		return make([]byte, 32), nil
	}
	if v.Sign() < 0 {
		return nil, errors.New("abi: uint256 cannot encode negative")
	}
	buf := v.Bytes()
	if len(buf) > 32 {
		return nil, errors.New("abi: uint256 overflow (>32 bytes)")
	}
	out := make([]byte, 32)
	copy(out[32-len(buf):], buf)
	return out, nil
}

// EncodeAddress left-pads the 20-byte address to 32 bytes (12 zero
// bytes + 20 address bytes). Accepts both checksummed and lowercase
// hex; the regex check is the same one used elsewhere in the codebase.
func EncodeAddress(addr string) ([]byte, error) {
	if !addressShape(addr) {
		return nil, ErrInvalidAddress
	}
	raw, err := hex.DecodeString(strings.TrimPrefix(strings.ToLower(addr), "0x"))
	if err != nil {
		return nil, fmt.Errorf("abi: decode address hex: %w", err)
	}
	out := make([]byte, 32)
	copy(out[12:], raw)
	return out, nil
}

// mustHexCheck mirrors common/utils/web3-address.ts isValidAddress.
// Kept inline rather than importing a sibling package because the
// abi package is a leaf utility and shouldn't depend on package
// services.
func mustHexCheck(addr string) bool {
	if len(addr) != 42 {
		return false
	}
	if addr[0] != '0' || (addr[1] != 'x' && addr[1] != 'X') {
		return false
	}
	for _, c := range addr[2:] {
		switch {
		case c >= '0' && c <= '9':
		case c >= 'a' && c <= 'f':
		case c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

// EncodeCall concatenates the 4-byte selector and the encoded args
// into the hex-prefixed `data` payload an eth_sendTransaction (or
// FE wallet) expects.
func EncodeCall(signature string, args ...[]byte) string {
	parts := make([][]byte, 0, len(args)+1)
	parts = append(parts, Selector(signature))
	parts = append(parts, args...)
	total := 0
	for _, p := range parts {
		total += len(p)
	}
	flat := make([]byte, 0, total)
	for _, p := range parts {
		flat = append(flat, p...)
	}
	return "0x" + hex.EncodeToString(flat)
}
