package abi

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
)

// Arg models one position in an encoded call. Static args carry their
// 32-byte head encoding (filled by Static). Dynamic args carry the
// tail-section bytes (length + items) and the encoder fills in the
// head slot with a computed offset.
//
// Only enough surface for the build-tx payloads ported from the
// NestJS services: uint256, address, address[]. Tuples and string/
// bytes dynamic types aren't wired yet — add them when a use lands.
type Arg struct {
	head    []byte
	tail    []byte
	dynamic bool
}

// Static wraps an already-encoded 32-byte head value.
func Static(headBytes []byte) Arg {
	return Arg{head: headBytes}
}

// EncodeAddressArrayArg encodes a Solidity `address[]` as a dynamic
// argument. Tail layout = length (uint256) + each address left-padded
// to 32 bytes, identical to viem's encodeFunctionData output for the
// same argument.
func EncodeAddressArrayArg(addrs []string) (Arg, error) {
	lenBytes, err := EncodeUint256(big.NewInt(int64(len(addrs))))
	if err != nil {
		return Arg{}, err
	}
	tail := make([]byte, 0, 32*(len(addrs)+1))
	tail = append(tail, lenBytes...)
	for _, a := range addrs {
		enc, err := EncodeAddress(a)
		if err != nil {
			return Arg{}, fmt.Errorf("address[] item: %w", err)
		}
		tail = append(tail, enc...)
	}
	return Arg{tail: tail, dynamic: true}, nil
}

// EncodeCallArgs encodes a function call with mixed static/dynamic
// args. Head section is 32 bytes per arg (static values directly,
// dynamic args' offset into the tail). Tail section is the dynamic
// args' contents concatenated in arg order.
//
// Returns the same 0x-prefixed hex string EncodeCall produces.
func EncodeCallArgs(signature string, args ...Arg) (string, error) {
	headSize := 32 * len(args)
	head := make([]byte, 0, headSize)
	tail := make([]byte, 0)
	tailOffset := int64(headSize)
	for i, a := range args {
		if a.dynamic {
			offsetBytes, err := EncodeUint256(big.NewInt(tailOffset))
			if err != nil {
				return "", fmt.Errorf("encode arg %d offset: %w", i, err)
			}
			head = append(head, offsetBytes...)
			tail = append(tail, a.tail...)
			tailOffset += int64(len(a.tail))
			continue
		}
		if len(a.head) != 32 {
			return "", errors.New("abi: static arg head must be 32 bytes")
		}
		head = append(head, a.head...)
	}
	flat := make([]byte, 0, 4+len(head)+len(tail))
	flat = append(flat, Selector(signature)...)
	flat = append(flat, head...)
	flat = append(flat, tail...)
	return "0x" + hex.EncodeToString(flat), nil
}
