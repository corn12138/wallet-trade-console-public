package bridge

import (
	"fmt"
	"math/big"
)

const (
	maxBridgeInt64AsUint = uint64(1<<63 - 1)
	maxBridgeDBIntAsUint = uint64(1<<31 - 1)
)

// bridgeChainID converts an ABI uint256 only after proving it fits the Go int
// and database fields used by routing. This keeps a malformed provider or
// unexpected event from silently wrapping into a different chain.
func bridgeChainID(label string, value *big.Int) (int, error) {
	if value == nil || value.Sign() < 0 || value.BitLen() > 31 {
		return 0, fmt.Errorf("bridge relayer: %s is outside database integer range", label)
	}
	return int(value.Uint64()), nil
}

func bridgeLogIndex(value uint64) (int, error) {
	if value > maxBridgeDBIntAsUint {
		return 0, fmt.Errorf("bridge relayer: log index %d exceeds database integer range", value)
	}
	return int(value), nil
}

func bridgeBlockNumber(value uint64) (int64, error) {
	if value > maxBridgeInt64AsUint {
		return 0, fmt.Errorf("bridge relayer: block number %d exceeds signed database range", value)
	}
	return int64(value), nil
}
