// Package ethtx signs legacy (EIP-155) Ethereum transactions.
//
// The repo deliberately does not depend on go-ethereum (see internal/web3auth),
// so this is a minimal, self-contained implementation of exactly what the
// bridge relayer needs: RLP encoding, keccak256, and a secp256k1 signature with
// the EIP-155 replay-protected `v`.
//
// Legacy (type 0) rather than EIP-1559 on purpose: it is the smaller surface to
// get right by hand, and it is accepted by every chain this product targets
// including a local Anvil. Nothing here broadcasts — callers pass the signed
// bytes to rpc.SendRawTransaction.
package ethtx

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"

	secp256k1 "github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"golang.org/x/crypto/sha3"
)

// Tx is an unsigned legacy transaction.
type Tx struct {
	Nonce    uint64
	GasPrice *big.Int
	Gas      uint64
	To       string // 20-byte hex address; empty means contract creation
	Value    *big.Int
	Data     []byte
	ChainID  *big.Int
}

// Signer holds a secp256k1 private key.
type Signer struct {
	priv    *secp256k1.PrivateKey
	address string
}

// NewSigner parses a hex private key (with or without 0x).
func NewSigner(hexKey string) (*Signer, error) {
	clean := strings.TrimPrefix(strings.TrimSpace(hexKey), "0x")
	if len(clean) != 64 {
		return nil, errors.New("ethtx: private key must be 32 bytes of hex")
	}
	raw, err := hex.DecodeString(clean)
	if err != nil {
		return nil, fmt.Errorf("ethtx: private key is not hex: %w", err)
	}
	priv := secp256k1.PrivKeyFromBytes(raw)

	// Ethereum address = last 20 bytes of keccak256(uncompressed pubkey[1:]).
	uncompressed := priv.PubKey().SerializeUncompressed()
	hash := keccak256(uncompressed[1:])
	return &Signer{priv: priv, address: "0x" + hex.EncodeToString(hash[12:])}, nil
}

// Address returns the signer's lower-case 0x address.
func (s *Signer) Address() string { return s.address }

// SignTx returns the RLP-encoded signed transaction, ready for
// eth_sendRawTransaction.
func (s *Signer) SignTx(tx Tx) (string, error) {
	if tx.ChainID == nil || tx.ChainID.Sign() <= 0 {
		// An EIP-155 signature is bound to a chain id. Signing without one
		// would produce a transaction replayable on every other chain.
		return "", errors.New("ethtx: chain id is required (EIP-155 replay protection)")
	}
	if tx.GasPrice == nil || tx.Gas == 0 {
		return "", errors.New("ethtx: gas price and gas limit are required")
	}

	value := tx.Value
	if value == nil {
		value = big.NewInt(0)
	}
	to, err := decodeAddress(tx.To)
	if err != nil {
		return "", err
	}

	// EIP-155 signing payload: rlp([nonce, gasPrice, gas, to, value, data,
	// chainId, 0, 0]).
	sigPayload := rlpList([][]byte{
		rlpUint(tx.Nonce),
		rlpBig(tx.GasPrice),
		rlpUint(tx.Gas),
		rlpBytes(to),
		rlpBig(value),
		rlpBytes(tx.Data),
		rlpBig(tx.ChainID),
		rlpBig(big.NewInt(0)),
		rlpBig(big.NewInt(0)),
	})

	hash := keccak256(sigPayload)
	sig := ecdsa.SignCompact(s.priv, hash, false)
	if len(sig) != 65 {
		return "", fmt.Errorf("ethtx: unexpected signature length %d", len(sig))
	}

	// SignCompact returns [recoveryCode || R || S] where recoveryCode is
	// 27+recid for uncompressed keys. Ethereum wants R, S separately and a
	// recovery id of 0/1 folded into v.
	recid := int64(sig[0]) - 27
	if recid < 0 || recid > 1 {
		return "", fmt.Errorf("ethtx: unexpected recovery id %d", recid)
	}
	r := new(big.Int).SetBytes(sig[1:33])
	sVal := new(big.Int).SetBytes(sig[33:65])

	// EIP-155: v = recid + chainId*2 + 35
	v := new(big.Int).Mul(tx.ChainID, big.NewInt(2))
	v.Add(v, big.NewInt(35+recid))

	signed := rlpList([][]byte{
		rlpUint(tx.Nonce),
		rlpBig(tx.GasPrice),
		rlpUint(tx.Gas),
		rlpBytes(to),
		rlpBig(value),
		rlpBytes(tx.Data),
		rlpBig(v),
		rlpBig(r),
		rlpBig(sVal),
	})
	return "0x" + hex.EncodeToString(signed), nil
}

// Hash returns the transaction hash of an already-signed raw transaction.
func Hash(rawHex string) string {
	raw, err := hex.DecodeString(strings.TrimPrefix(rawHex, "0x"))
	if err != nil {
		return ""
	}
	return "0x" + hex.EncodeToString(keccak256(raw))
}

func decodeAddress(addr string) ([]byte, error) {
	clean := strings.TrimPrefix(strings.TrimSpace(addr), "0x")
	if clean == "" {
		return nil, nil // contract creation
	}
	if len(clean) != 40 {
		return nil, fmt.Errorf("ethtx: `to` must be a 20-byte address, got %q", addr)
	}
	return hex.DecodeString(clean)
}

func keccak256(data []byte) []byte {
	h := sha3.NewLegacyKeccak256()
	h.Write(data)
	return h.Sum(nil)
}

// ─── Minimal RLP ─────────────────────────────────────────────────────────────
//
// Only the two forms a transaction needs: byte strings and one list.

// rlpBytes encodes a byte string.
func rlpBytes(b []byte) []byte {
	// A single byte below 0x80 is its own encoding.
	if len(b) == 1 && b[0] < 0x80 {
		return b
	}
	return append(rlpLengthPrefix(len(b), 0x80), b...)
}

// rlpList encodes a list of already-encoded items.
func rlpList(items [][]byte) []byte {
	var payload []byte
	for _, item := range items {
		payload = append(payload, item...)
	}
	return append(rlpLengthPrefix(len(payload), 0xc0), payload...)
}

// rlpLengthPrefix builds the short-form or long-form prefix.
func rlpLengthPrefix(length int, offset byte) []byte {
	if length < 56 {
		return []byte{offset + byte(length)}
	}
	lenBytes := big.NewInt(int64(length)).Bytes()
	return append([]byte{offset + 55 + byte(len(lenBytes))}, lenBytes...)
}

// rlpBig encodes an integer as a minimal big-endian byte string. Zero encodes
// as the EMPTY string (0x80), not as 0x00 — getting this wrong changes the
// signing hash and produces a transaction the network rejects.
func rlpBig(v *big.Int) []byte {
	if v == nil || v.Sign() == 0 {
		return []byte{0x80}
	}
	return rlpBytes(v.Bytes())
}

func rlpUint(v uint64) []byte {
	return rlpBig(new(big.Int).SetUint64(v))
}
