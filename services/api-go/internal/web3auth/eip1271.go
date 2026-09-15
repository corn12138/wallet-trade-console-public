package web3auth

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/rpc"
)

// EIP-1271: bytes4(keccak256("isValidSignature(bytes32,bytes)")). The same four
// bytes are both the function selector and the success magic value.
const eip1271MagicValue = "1626ba7e"

var (
	// ErrContractVerificationUnavailable means no verifier is configured for
	// the message's chain. It is a failure, not a soft pass: a smart-wallet
	// login on a chain the API cannot read must be refused, never assumed
	// valid.
	ErrContractVerificationUnavailable = errors.New("contract wallet verification unavailable for this chain")

	// ErrContractVerificationUnreachable means the node did not answer. It is
	// deliberately distinct from a rejection: reporting an RPC outage as
	// "invalid signature" tells the user something false about their wallet.
	ErrContractVerificationUnreachable = errors.New("contract wallet verification unavailable: chain node did not answer")
)

// EthCaller is the read-only slice of the RPC client this package needs.
// *rpc.Client satisfies it.
type EthCaller interface {
	EthCall(ctx context.Context, to, dataHex, block string) ([]byte, error)
}

// ContractSignatureVerifier answers whether `address` on `chainID` accepts
// `signature` over `digest` under EIP-1271.
type ContractSignatureVerifier interface {
	IsValidSignature(ctx context.Context, chainID int, address string, digest, signature []byte) (bool, error)
	// SupportsChain reports whether a verification attempt is even possible.
	SupportsChain(chainID int) bool
}

// RPCContractVerifier verifies through eth_call against one RPC per chain.
// A chain absent from the map is unsupported, which fails closed.
type RPCContractVerifier struct {
	callers map[int]EthCaller
}

func NewRPCContractVerifier(callers map[int]EthCaller) *RPCContractVerifier {
	copied := make(map[int]EthCaller, len(callers))
	for chainID, caller := range callers {
		if caller != nil {
			copied[chainID] = caller
		}
	}
	return &RPCContractVerifier{callers: copied}
}

func (v *RPCContractVerifier) SupportsChain(chainID int) bool {
	if v == nil {
		return false
	}
	_, ok := v.callers[chainID]
	return ok
}

// IsValidSignature staticcalls isValidSignature(bytes32,bytes) and accepts only
// an exact magic-value return.
//
// Every other outcome is a rejection, not an error to be retried or excused:
//   - an EOA or undeployed address returns empty data;
//   - a wallet that refuses the signature reverts, which surfaces as an RPC
//     error carrying no return data;
//   - a contract answering some other word is not an EIP-1271 wallet.
//
// A transport failure is reported as an error so the caller fails the login
// rather than treating an unreachable node as a rejection it could retry past.
func (v *RPCContractVerifier) IsValidSignature(ctx context.Context, chainID int, address string, digest, signature []byte) (bool, error) {
	caller, ok := v.callers[chainID]
	if !ok {
		return false, ErrContractVerificationUnavailable
	}
	if len(digest) != 32 {
		return false, fmt.Errorf("eip1271: digest must be 32 bytes, got %d", len(digest))
	}
	if !addressRE.MatchString(address) {
		return false, fmt.Errorf("%w: address", ErrBadMessage)
	}

	out, err := caller.EthCall(ctx, address, encodeIsValidSignature(digest, signature), "latest")
	if err != nil {
		if nodeAnswered(err) {
			// "0x" is what an EOA or an undeployed address returns; a JSON-RPC
			// error frame is the wallet reverting. Both are the chain saying
			// no, so they are rejections rather than errors.
			return false, nil
		}
		return false, fmt.Errorf("%w: %v", ErrContractVerificationUnreachable, err)
	}
	return isMagicValue(out), nil
}

// nodeAnswered separates "the chain replied and the answer was no" from "the
// node could not be reached". Only the first is a signature rejection.
func nodeAnswered(err error) bool {
	if errors.Is(err, rpc.ErrEmpty) {
		return true
	}
	var frame *rpc.RPCError
	return errors.As(err, &frame)
}

// isMagicValue accepts only a 32-byte word whose leading 4 bytes are the magic
// value. Shorter returns (including empty, which is what an EOA gives) fail.
func isMagicValue(out []byte) bool {
	if len(out) != 32 {
		return false
	}
	if hex.EncodeToString(out[:4]) != eip1271MagicValue {
		return false
	}
	// The remaining 28 bytes of the word must be the ABI zero padding.
	for _, b := range out[4:] {
		if b != 0 {
			return false
		}
	}
	return true
}

// encodeIsValidSignature ABI-encodes isValidSignature(bytes32,bytes):
//
//	selector | digest | offset(0x40) | len(signature) | signature (right-padded)
//
// The signature is dynamic, so no assumption is made about its length —
// contract-wallet signatures are routinely not 65 bytes.
func encodeIsValidSignature(digest, signature []byte) string {
	var b strings.Builder
	b.Grow(2 + 8 + 2*(32*3+len(signature)+31))
	b.WriteString("0x")
	b.WriteString(eip1271MagicValue)
	b.WriteString(hex.EncodeToString(digest))
	b.WriteString(word(0x40))
	b.WriteString(word(uint64(len(signature))))
	b.WriteString(hex.EncodeToString(signature))
	if pad := (32 - len(signature)%32) % 32; pad > 0 {
		b.WriteString(strings.Repeat("00", pad))
	}
	return b.String()
}

// word renders a uint64 as a left-padded 32-byte ABI word.
func word(n uint64) string {
	buf := make([]byte, 32)
	for i := 31; i >= 0 && n > 0; i-- {
		buf[i] = byte(n)
		n >>= 8
	}
	return hex.EncodeToString(buf)
}

// PersonalSignDigest returns the EIP-191 personal_sign hash of message. Both the
// EOA recovery path and the EIP-1271 path authenticate this exact digest.
func PersonalSignDigest(message string) []byte {
	return keccak256([]byte(fmt.Sprintf("\x19Ethereum Signed Message:\n%d%s", len(message), message)))
}
