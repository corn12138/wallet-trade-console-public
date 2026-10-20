package web3events

import (
	"context"
	"errors"
	"math"
	"testing"
)

func TestStoredNonceBounds(t *testing.T) {
	if got, err := storedNonce(nil); got != nil || err != nil {
		t.Fatalf("absent nonce: %v %v", got, err)
	}
	for _, value := range []uint64{0, math.MaxInt64, math.MaxInt64 + 1, math.MaxUint64} {
		got, err := storedNonce(&value)
		if value <= math.MaxInt64 {
			if err != nil || got == nil || uint64(*got) != value {
				t.Fatalf("nonce %d: %v %v", value, got, err)
			}
		} else if got != nil || !errors.Is(err, ErrRPCEvidenceMismatch) {
			t.Fatalf("overflow nonce %d accepted: %v %v", value, got, err)
		}
	}
}

func TestOversizedNonceDoesNotReachAttemptSQL(t *testing.T) {
	value := uint64(math.MaxInt64 + 1)
	// A nil SQL port makes an attempted write fail the test before any row
	// can be stored or a sibling incorrectly marked as replaced.
	if _, err := writeAttemptRow(context.Background(), nil, attemptWrite{senderNonce: &value}, "", 0); !errors.Is(err, ErrRPCEvidenceMismatch) {
		t.Fatal(err)
	}
	if _, err := markReplacedSiblings(context.Background(), nil, 1, "", "", &value); !errors.Is(err, ErrRPCEvidenceMismatch) {
		t.Fatal(err)
	}
}
