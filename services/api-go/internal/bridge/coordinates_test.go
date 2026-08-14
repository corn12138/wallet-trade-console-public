package bridge

import (
	"math/big"
	"testing"
)

func TestBridgeChainIDRejectsUnsignedOverflow(t *testing.T) {
	t.Parallel()

	if got, err := bridgeChainID("chain ID", big.NewInt(11155111)); err != nil || got != 11155111 {
		t.Fatalf("bridgeChainID = %d, %v", got, err)
	}
	if _, err := bridgeChainID("chain ID", new(big.Int).SetUint64(maxBridgeDBIntAsUint+1)); err == nil {
		t.Fatal("expected uint256 outside database range to be rejected")
	}
}

func TestBridgeCoordinatesRejectDatabaseOverflow(t *testing.T) {
	t.Parallel()

	if _, err := bridgeBlockNumber(maxBridgeInt64AsUint + 1); err == nil {
		t.Fatal("expected overflowing block number to be rejected")
	}
	if _, err := bridgeLogIndex(maxBridgeDBIntAsUint + 1); err == nil {
		t.Fatal("expected overflowing log index to be rejected")
	}
}
