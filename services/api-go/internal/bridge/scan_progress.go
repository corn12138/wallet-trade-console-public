package bridge

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/rpc"
	"github.com/jackc/pgx/v5"
)

// scanProgressStore is separated from transfer persistence so recovery rules
// can be failure-injected without a live chain or database.
type scanProgressStore interface {
	LoadScanProgress(ctx context.Context, chainID int) (nextBlock uint64, found bool, err error)
	SaveScanProgress(ctx context.Context, chainID int, nextBlock uint64) error
	RecordLogFailure(ctx context.Context, chainID int, lg rpc.Log, failure string) error
	ResolveLogFailure(ctx context.Context, chainID int, lg rpc.Log) error
	HasUnresolvedLogFailure(ctx context.Context, chainID int, fromBlock, toBlock uint64) (bool, error)
}

var ErrUnresolvedLogFailure = errors.New("bridge: unresolved relayer log failure blocks progress")

const scanProgressLockNamespace int32 = 0x42524745

// LoadScanProgress reads the first block that has not been durably completed.
func (s *Store) LoadScanProgress(ctx context.Context, chainID int) (uint64, bool, error) {
	if !s.Available() {
		return 0, false, ErrStoreUnavailable
	}
	var nextBlock int64
	err := s.pool.QueryRow(ctx,
		`SELECT next_block FROM bridge_relayer_scan_progress WHERE chain_id = $1`, chainID,
	).Scan(&nextBlock)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("bridge: load relayer scan progress: %w", err)
	}
	if nextBlock < 0 {
		return 0, false, fmt.Errorf("bridge: negative relayer scan progress for chain %d", chainID)
	}
	return uint64(nextBlock), true, nil
}

// SaveScanProgress advances the first incomplete block monotonically. A stale
// concurrent relayer may replay work, but it can never rewind durable progress.
func (s *Store) SaveScanProgress(ctx context.Context, chainID int, nextBlock uint64) error {
	if !s.Available() {
		return ErrStoreUnavailable
	}
	if nextBlock > math.MaxInt64 {
		return fmt.Errorf("bridge: next scan block %d exceeds database range", nextBlock)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("bridge: begin relayer progress update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockScanProgress(ctx, tx, chainID); err != nil {
		return err
	}
	var unresolved bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM bridge_relayer_log_failures
			WHERE chain_id = $1 AND resolved_at IS NULL AND block_number < $2
		)
	`, chainID, int64(nextBlock)).Scan(&unresolved); err != nil {
		return fmt.Errorf("bridge: check relayer progress boundary: %w", err)
	}
	if unresolved {
		return ErrUnresolvedLogFailure
	}
	var saved int64
	err = tx.QueryRow(ctx, `
		INSERT INTO bridge_relayer_scan_progress (chain_id, next_block, created_at, updated_at)
		VALUES ($1, $2, NOW(), NOW())
		ON CONFLICT (chain_id) DO UPDATE SET
			next_block = GREATEST(bridge_relayer_scan_progress.next_block, EXCLUDED.next_block),
			updated_at = NOW()
		RETURNING next_block
	`, chainID, int64(nextBlock)).Scan(&saved)
	if err != nil {
		return fmt.Errorf("bridge: save relayer scan progress: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("bridge: commit relayer progress update: %w", err)
	}
	return nil
}

// RecordLogFailure keeps the failed coordinate durable while its block remains
// the next recovery unit. Repeated cycles increment attempts instead of losing
// the original failure behind transient logs.
func (s *Store) RecordLogFailure(ctx context.Context, chainID int, lg rpc.Log, failure string) error {
	if !s.Available() {
		return ErrStoreUnavailable
	}
	blockNumber, logIndex, err := scanLogCoordinates(lg)
	if err != nil {
		return err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("bridge: begin relayer failure journal: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockScanProgress(ctx, tx, chainID); err != nil {
		return err
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO bridge_relayer_log_failures (
			chain_id, block_number, log_index, tx_hash, last_error,
			attempts, first_failed_at, last_failed_at, resolved_at
		) VALUES ($1, $2, $3, $4, $5, 1, NOW(), NOW(), NULL)
		ON CONFLICT (chain_id, block_number, log_index, tx_hash) DO UPDATE SET
			last_error = EXCLUDED.last_error,
			attempts = bridge_relayer_log_failures.attempts + 1,
			last_failed_at = NOW(),
			resolved_at = NULL
	`, chainID, blockNumber, logIndex, strings.ToLower(strings.TrimSpace(lg.TxHash)), truncate(failure, 1000))
	if err != nil {
		return fmt.Errorf("bridge: record relayer log failure: %w", err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO bridge_relayer_scan_progress (chain_id, next_block, created_at, updated_at)
		VALUES ($1, $2, NOW(), NOW())
		ON CONFLICT (chain_id) DO UPDATE SET
			next_block = LEAST(bridge_relayer_scan_progress.next_block, EXCLUDED.next_block),
			updated_at = NOW()
	`, chainID, blockNumber)
	if err != nil {
		return fmt.Errorf("bridge: pin relayer scan progress to failed block: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("bridge: commit relayer failure journal: %w", err)
	}
	return nil
}

// ResolveLogFailure closes a prior failure journal entry only after the same
// log has projected successfully during replay.
func (s *Store) ResolveLogFailure(ctx context.Context, chainID int, lg rpc.Log) error {
	if !s.Available() {
		return ErrStoreUnavailable
	}
	blockNumber, logIndex, err := scanLogCoordinates(lg)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		UPDATE bridge_relayer_log_failures
		   SET resolved_at = NOW()
		 WHERE chain_id = $1 AND block_number = $2 AND log_index = $3
		   AND tx_hash = $4 AND resolved_at IS NULL
	`, chainID, blockNumber, logIndex, strings.ToLower(strings.TrimSpace(lg.TxHash)))
	if err != nil {
		return fmt.Errorf("bridge: resolve relayer log failure: %w", err)
	}
	return nil
}

func (s *Store) HasUnresolvedLogFailure(ctx context.Context, chainID int, fromBlock, toBlock uint64) (bool, error) {
	if !s.Available() {
		return false, ErrStoreUnavailable
	}
	if fromBlock > toBlock {
		return false, nil
	}
	if fromBlock > math.MaxInt64 || toBlock > math.MaxInt64 {
		return false, fmt.Errorf("bridge: failure scan range [%d,%d] exceeds database range", fromBlock, toBlock)
	}
	var unresolved bool
	if err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM bridge_relayer_log_failures
			WHERE chain_id = $1 AND resolved_at IS NULL
			  AND block_number BETWEEN $2 AND $3
		)
	`, chainID, int64(fromBlock), int64(toBlock)).Scan(&unresolved); err != nil {
		return false, fmt.Errorf("bridge: query unresolved relayer failures: %w", err)
	}
	return unresolved, nil
}

func scanLogCoordinates(lg rpc.Log) (int64, int, error) {
	blockNumber, err := bridgeBlockNumber(lg.BlockNumber)
	if err != nil {
		return 0, 0, err
	}
	logIndex, err := bridgeLogIndex(lg.LogIndex)
	if err != nil {
		return 0, 0, err
	}
	return blockNumber, logIndex, nil
}

func lockScanProgress(ctx context.Context, tx pgx.Tx, chainID int) error {
	if chainID < 0 || int64(chainID) > math.MaxInt32 {
		return fmt.Errorf("bridge: chain id %d exceeds database range", chainID)
	}
	// The lock is acquired before reading failures so a waiting cursor update
	// cannot reuse a snapshot taken before another relayer journals a failure.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1, $2)`, scanProgressLockNamespace, int32(chainID)); err != nil {
		return fmt.Errorf("bridge: lock relayer scan progress: %w", err)
	}
	return nil
}
