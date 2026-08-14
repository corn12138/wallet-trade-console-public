package indexer

import (
	"fmt"
	"strings"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/rpc"
)

const (
	maxSignedInt64AsUint = uint64(1<<63 - 1)
	maxDatabaseIntAsUint = uint64(1<<31 - 1)
)

// toParsedEvent is the single boundary where unsigned JSON-RPC coordinates
// become the signed database types used by every projection. An impossible
// coordinate is rejected before any raw or derived row can be persisted.
func toParsedEvent(chainID int, contractAddress string, raw rpc.Log) (ParsedEvent, error) {
	if raw.BlockNumber > maxSignedInt64AsUint {
		return ParsedEvent{}, fmt.Errorf("block number %d exceeds signed database range", raw.BlockNumber)
	}
	if raw.LogIndex > maxDatabaseIntAsUint {
		return ParsedEvent{}, fmt.Errorf("log index %d exceeds database integer range", raw.LogIndex)
	}

	emitter := raw.Address
	if emitter == "" {
		emitter = contractAddress
	}
	return ParsedEvent{
		ChainID:         chainID,
		ContractAddress: strings.ToLower(emitter),
		BlockNumber:     int64(raw.BlockNumber),
		TxHash:          raw.TxHash,
		LogIndex:        int(raw.LogIndex),
		Parsed:          ParseIndexedLog(Log{Address: raw.Address, Topics: raw.Topics, Data: raw.Data}),
	}, nil
}
