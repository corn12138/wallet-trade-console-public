package bridge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/rpc"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestStoreFailureJournalPinsScanProgress(t *testing.T) {
	dsn := os.Getenv("BRIDGE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("BRIDGE_TEST_DATABASE_URL is not set")
	}

	ctx := context.Background()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse integration database URL: %v", err)
	}
	delete(config.ConnConfig.RuntimeParams, "schema")
	admin, err := pgx.ConnectConfig(ctx, config.ConnConfig.Copy())
	if err != nil {
		t.Fatalf("open integration database: %v", err)
	}
	defer admin.Close(ctx)

	schema := fmt.Sprintf("bridge_progress_%d", time.Now().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatalf("create integration schema: %v", err)
	}
	defer func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+quotedSchema+" CASCADE")
	}()

	config.ConnConfig.RuntimeParams["default_transaction_isolation"] = "repeatable read"
	config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET search_path TO "+quotedSchema)
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("open isolated integration pool: %v", err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `
		CREATE TABLE bridge_relayer_scan_progress (
			chain_id INTEGER PRIMARY KEY,
			next_block BIGINT NOT NULL CHECK (next_block >= 0),
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL
		);
		CREATE TABLE bridge_relayer_log_failures (
			chain_id INTEGER NOT NULL,
			block_number BIGINT NOT NULL,
			log_index INTEGER NOT NULL,
			tx_hash VARCHAR(66) NOT NULL,
			last_error TEXT NOT NULL,
			attempts INTEGER NOT NULL DEFAULT 1,
			first_failed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			last_failed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			resolved_at TIMESTAMPTZ,
			PRIMARY KEY (chain_id, block_number, log_index, tx_hash)
		);
		CREATE TABLE bridge_transfers (
			id TEXT PRIMARY KEY,
			transfer_id VARCHAR(66) NOT NULL,
			src_chain_id INTEGER NOT NULL,
			dst_chain_id INTEGER NOT NULL,
			src_gateway VARCHAR(42) NOT NULL,
			sender VARCHAR(42) NOT NULL,
			recipient VARCHAR(42) NOT NULL,
			src_token VARCHAR(42) NOT NULL,
			dst_token VARCHAR(42),
			amount NUMERIC(78,0) NOT NULL,
			status VARCHAR(20) NOT NULL,
			deposit_tx_hash VARCHAR(66) NOT NULL,
			deposit_log_index INTEGER NOT NULL,
			deposit_block BIGINT NOT NULL,
			deposited_at TIMESTAMPTZ NOT NULL,
			fulfill_tx_hash VARCHAR(66),
			fulfill_block BIGINT,
			fulfilled_at TIMESTAMPTZ,
			refund_tx_hash VARCHAR(66),
			refunded_at TIMESTAMPTZ,
			last_error TEXT,
			attempts INTEGER NOT NULL DEFAULT 0,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			UNIQUE (src_chain_id, transfer_id)
		)
	`); err != nil {
		t.Fatalf("create relayer progress tables: %v", err)
	}

	store := NewStore(pool)
	const chainID = 11155111
	if err := store.SaveScanProgress(ctx, chainID, 30); err != nil {
		t.Fatalf("seed scan progress: %v", err)
	}
	failed := rpc.Log{BlockNumber: 22, LogIndex: 1, TxHash: "0xfailed"}
	if err := store.RecordLogFailure(ctx, chainID, failed, "injected projection failure"); err != nil {
		t.Fatalf("record log failure: %v", err)
	}
	if err := store.SaveScanProgress(ctx, chainID, 31); !errors.Is(err, ErrUnresolvedLogFailure) {
		t.Fatalf("advance with unresolved failure err=%v, want ErrUnresolvedLogFailure", err)
	}
	next, found, err := store.LoadScanProgress(ctx, chainID)
	if err != nil || !found || next != 22 {
		t.Fatalf("pinned progress next=%d found=%v err=%v, want 22,true,nil", next, found, err)
	}
	unresolved, err := store.HasUnresolvedLogFailure(ctx, chainID, 20, 30)
	if err != nil || !unresolved {
		t.Fatalf("unresolved=%v err=%v, want true,nil", unresolved, err)
	}
	if err := store.ResolveLogFailure(ctx, chainID, failed); err != nil {
		t.Fatalf("resolve log failure: %v", err)
	}
	if err := store.SaveScanProgress(ctx, chainID, 31); err != nil {
		t.Fatalf("advance after resolution: %v", err)
	}

	transferID := "0xtransfer"
	if _, err := store.MarkFulfilled(ctx, chainID, transferID, "0xfulfill", 24, time.Now().UTC(), ""); !errors.Is(err, ErrTransferNotFound) {
		t.Fatalf("fulfil before deposit err=%v, want ErrTransferNotFound", err)
	}
	inserted, err := store.RecordInitiated(ctx, Transfer{
		TransferID: transferID, SrcChainID: chainID, DstChainID: 84532,
		SrcGateway: "0x0000000000000000000000000000000000000001",
		Sender:     "0x0000000000000000000000000000000000000002",
		Recipient:  "0x0000000000000000000000000000000000000003",
		SrcToken:   "0x0000000000000000000000000000000000000004",
		Amount:     "1", DepositTxHash: "0xdeposit", DepositLogIndex: 0,
		DepositBlock: 20, DepositedAt: time.Now().UTC(),
	})
	if err != nil || !inserted {
		t.Fatalf("record initiated inserted=%v err=%v", inserted, err)
	}
	updated, err := store.MarkFulfilled(ctx, chainID, transferID, "0xfulfill", 24, time.Now().UTC(), "")
	if err != nil || !updated {
		t.Fatalf("mark fulfilled updated=%v err=%v", updated, err)
	}
	updated, err = store.MarkFulfilled(ctx, chainID, transferID, "0xfulfill", 24, time.Now().UTC(), "")
	if err != nil || updated {
		t.Fatalf("idempotent fulfil updated=%v err=%v", updated, err)
	}

	reconciledID := "0xreconciled"
	inserted, err = store.RecordInitiated(ctx, Transfer{
		TransferID: reconciledID, SrcChainID: chainID, DstChainID: 84532,
		SrcGateway: "0x0000000000000000000000000000000000000001",
		Sender:     "0x0000000000000000000000000000000000000002",
		Recipient:  "0x0000000000000000000000000000000000000003",
		SrcToken:   "0x0000000000000000000000000000000000000004",
		Amount:     "2", DepositTxHash: "0xdeposit2", DepositLogIndex: 1,
		DepositBlock: 21, DepositedAt: time.Now().UTC(),
	})
	if err != nil || !inserted {
		t.Fatalf("record reconciled transfer inserted=%v err=%v", inserted, err)
	}
	updated, err = store.MarkFulfilled(ctx, chainID, reconciledID, "", 0, time.Now().UTC(), "")
	if err != nil || !updated {
		t.Fatalf("reconcile fulfillment updated=%v err=%v", updated, err)
	}
	updated, err = store.MarkFulfilled(ctx, chainID, reconciledID, "0xcanonical", 25, time.Now().UTC(), "")
	if err != nil || !updated {
		t.Fatalf("canonical log after reconciliation updated=%v err=%v", updated, err)
	}
	updated, err = store.MarkFulfilled(ctx, chainID, reconciledID, "0xcanonical", 25, time.Now().UTC(), "")
	if err != nil || updated {
		t.Fatalf("canonical fulfillment replay updated=%v err=%v", updated, err)
	}
	if _, err := store.MarkFulfilled(ctx, chainID, reconciledID, "0xdifferent", 26, time.Now().UTC(), ""); !errors.Is(err, ErrTransferStateConflict) {
		t.Fatalf("conflicting fulfillment err=%v, want ErrTransferStateConflict", err)
	}
}

func TestStoreConcurrentFailureCannotBeSkippedByProgressSnapshot(t *testing.T) {
	dsn := os.Getenv("BRIDGE_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("BRIDGE_TEST_DATABASE_URL is not set")
	}

	ctx := context.Background()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse integration database URL: %v", err)
	}
	delete(config.ConnConfig.RuntimeParams, "schema")
	admin, err := pgx.ConnectConfig(ctx, config.ConnConfig.Copy())
	if err != nil {
		t.Fatalf("open integration database: %v", err)
	}
	defer admin.Close(ctx)

	schema := fmt.Sprintf("bridge_progress_race_%d", time.Now().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatalf("create integration schema: %v", err)
	}
	defer func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+quotedSchema+" CASCADE")
	}()

	config.ConnConfig.RuntimeParams["default_transaction_isolation"] = "repeatable read"
	config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET search_path TO "+quotedSchema)
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("open isolated integration pool: %v", err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `
		CREATE TABLE bridge_relayer_scan_progress (
			chain_id INTEGER PRIMARY KEY,
			next_block BIGINT NOT NULL CHECK (next_block >= 0),
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL
		);
		CREATE TABLE bridge_relayer_log_failures (
			chain_id INTEGER NOT NULL,
			block_number BIGINT NOT NULL,
			log_index INTEGER NOT NULL,
			tx_hash VARCHAR(66) NOT NULL,
			last_error TEXT NOT NULL,
			attempts INTEGER NOT NULL DEFAULT 1,
			first_failed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			last_failed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			resolved_at TIMESTAMPTZ,
			PRIMARY KEY (chain_id, block_number, log_index, tx_hash)
		)
	`); err != nil {
		t.Fatalf("create relayer progress tables: %v", err)
	}

	store := NewStore(pool)
	const chainID = 421614
	if err := store.SaveScanProgress(ctx, chainID, 30); err != nil {
		t.Fatalf("seed scan progress: %v", err)
	}
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin lock holder: %v", err)
	}
	defer func() { _ = blocker.Rollback(context.Background()) }()
	if err := lockScanProgress(ctx, blocker, chainID); err != nil {
		t.Fatalf("hold progress lock: %v", err)
	}

	advanceResult := make(chan error, 1)
	go func() {
		advanceResult <- store.SaveScanProgress(ctx, chainID, 31)
	}()
	select {
	case err := <-advanceResult:
		t.Fatalf("progress update did not serialize behind the chain lock: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO bridge_relayer_log_failures (
			chain_id, block_number, log_index, tx_hash, last_error,
			attempts, first_failed_at, last_failed_at, resolved_at
		) VALUES ($1, 25, 0, '0xconcurrent', 'injected concurrent failure', 1, NOW(), NOW(), NULL)
	`, chainID); err != nil {
		t.Fatalf("insert concurrent failure: %v", err)
	}
	if err := blocker.Commit(ctx); err != nil {
		t.Fatalf("release progress lock: %v", err)
	}
	select {
	case err := <-advanceResult:
		if !errors.Is(err, ErrUnresolvedLogFailure) {
			t.Fatalf("concurrent advance err=%v, want ErrUnresolvedLogFailure", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for serialized progress update")
	}
	next, found, err := store.LoadScanProgress(ctx, chainID)
	if err != nil || !found || next != 30 {
		t.Fatalf("progress after concurrent failure next=%d found=%v err=%v, want 30,true,nil", next, found, err)
	}
}
