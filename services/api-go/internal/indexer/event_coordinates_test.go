package indexer

import (
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/rpc"
)

func TestToParsedEventNormalizesCoordinatesOnce(t *testing.T) {
	t.Parallel()

	event, err := toParsedEvent(11155111, "0xFallback", rpc.Log{
		Address:     "0xABCDEF",
		BlockNumber: 123,
		LogIndex:    7,
		TxHash:      "0xhash",
	})
	if err != nil {
		t.Fatalf("toParsedEvent: %v", err)
	}
	if event.ContractAddress != "0xabcdef" || event.BlockNumber != 123 || event.LogIndex != 7 {
		t.Fatalf("event coordinates = %+v", event)
	}
}

func TestToParsedEventRejectsCoordinatesOutsidePersistenceRange(t *testing.T) {
	t.Parallel()

	if _, err := toParsedEvent(1, "0x1", rpc.Log{BlockNumber: maxSignedInt64AsUint + 1}); err == nil {
		t.Fatal("expected overflowing block number to be rejected")
	}

	if _, err := toParsedEvent(1, "0x1", rpc.Log{LogIndex: maxDatabaseIntAsUint + 1}); err == nil {
		t.Fatal("expected overflowing log index to be rejected")
	}
}
