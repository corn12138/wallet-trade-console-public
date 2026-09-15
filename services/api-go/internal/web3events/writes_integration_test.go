package web3events

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestRepositoryTransactionOwnerIsImmutable pins the property its original
// version pinned: a chain transaction keeps its first verified owner.
//
// It no longer hand-writes the table. The previous fixture declared
// BIGSERIAL/TIMESTAMPTZ/VARCHAR(20) where the migrated table has
// integer/timestamp(3)/text, so it could pass against a shape production does
// not have — and it silently lost coverage the moment the schema grew columns.
// It now runs against a database migrated by the real migration chain.
func TestRepositoryTransactionOwnerIsImmutable(t *testing.T) {
	pool := requireMigratedDatabase(t)
	chainID := isolatedChain(t, pool)
	repo := NewRepository(pool)
	ctx := context.Background()
	hash := "0x" + strings.Repeat("a", 64)

	if _, err := repo.UpsertSubmittedTransaction(ctx, SubmitTxInput{
		ChainID: chainID, TxHash: hash, FromAddress: ownerA,
	}); err != nil {
		t.Fatalf("initial submit: %v", err)
	}
	if _, err := repo.UpsertSubmittedTransaction(ctx, SubmitTxInput{
		ChainID: chainID, TxHash: hash, FromAddress: ownerB,
	}); !errors.Is(err, ErrTransactionOwnerConflict) {
		t.Fatalf("conflicting submit err=%v, want ErrTransactionOwnerConflict", err)
	}
	if _, err := repo.UpsertTransactionReceipt(ctx, ReceiptTxInput{
		ChainID: chainID, TxHash: hash, FromAddress: ownerB,
		Status: StatusConfirmed, BlockNumber: 123,
	}); !errors.Is(err, ErrTransactionOwnerConflict) {
		t.Fatalf("conflicting receipt err=%v, want ErrTransactionOwnerConflict", err)
	}

	var persisted string
	if err := pool.QueryRow(ctx,
		`SELECT from_address FROM web3_transactions WHERE chain_id=$1 AND tx_hash=$2`,
		chainID, hash).Scan(&persisted); err != nil {
		t.Fatalf("read persisted owner: %v", err)
	}
	if persisted != ownerA {
		t.Fatalf("persisted owner=%s, want %s", persisted, ownerA)
	}
}
