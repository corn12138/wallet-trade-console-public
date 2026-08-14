package indexer

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrCheckpointPoolUnavailable mirrors the sibling-package pattern: a
// nil-pool store can be constructed for tests / dry-run boots but every
// data-plane call surfaces this sentinel so callers can degrade.
var ErrCheckpointPoolUnavailable = errors.New("indexer checkpoint: database pool not configured")

// CheckpointStore is the persistence boundary for the indexer's
// per-(chain, contract) head pointer. Phase 6a.2 wires Get + Save; the
// log-parser / event-handler slices (6a.3, 6a.6) will call these around
// each block they process.
//
// The schema is web3_indexer_state from the NestJS Prisma model:
//
//	chain_id INT, contract_address VARCHAR(42), last_processed_block BIGINT,
//	last_safe_block BIGINT, confirmation_depth INT, updated_at TIMESTAMP
//	UNIQUE (chain_id, contract_address)
type CheckpointStore struct {
	pool *pgxpool.Pool
}

// NewCheckpointStore binds a store. A nil pool is tolerated so the
// worker can boot in dry-run mode and surface ErrCheckpointPoolUnavailable
// on every call.
func NewCheckpointStore(pool *pgxpool.Pool) *CheckpointStore {
	return &CheckpointStore{pool: pool}
}

// Get reads the highest safely-confirmed block we've already processed
// for (chainID, contractAddress). Mirrors getCheckpoint() in
// legacy NestJS indexer/indexer.service.ts: prefer last_safe_block,
// then last_processed_block, then fall back to deployBlock.
//
// No row found (pgx.ErrNoRows) is not an error — it just means the
// indexer has never processed this contract on this chain. The caller
// gets deployBlock back so the backfill loop can start from there.
func (s *CheckpointStore) Get(
	ctx context.Context,
	chainID int,
	contractAddress string,
	deployBlock uint64,
) (uint64, error) {
	if s == nil || s.pool == nil {
		return 0, ErrCheckpointPoolUnavailable
	}
	address := strings.ToLower(contractAddress)

	var lastProcessed, lastSafe int64
	err := s.pool.QueryRow(ctx, `
		SELECT last_processed_block, last_safe_block
		FROM web3_indexer_state
		WHERE chain_id = $1 AND contract_address = $2
	`, chainID, address).Scan(&lastProcessed, &lastSafe)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return deployBlock, nil
		}
		return 0, fmt.Errorf("query web3_indexer_state: %w", err)
	}

	return chooseCheckpoint(lastProcessed, lastSafe, deployBlock), nil
}

// Save persists (chainID, contractAddress) → blockNumber as a monotonic
// upsert. Mirrors saveCheckpoint() in the NestJS service: the
// `GREATEST(...)` clauses prevent races between the backfill writer
// (saves at batch boundaries) and the head-subscription writer (saves
// per-event) from rewinding the pointer if their writes arrive
// out-of-order.
//
// deployBlock is the floor used to compute last_safe_block when
// blockNumber is shallower than the confirmation depth.
func (s *CheckpointStore) Save(
	ctx context.Context,
	chainID int,
	contractAddress string,
	blockNumber uint64,
	confirmationDepth int,
	deployBlock uint64,
) error {
	if s == nil || s.pool == nil {
		return ErrCheckpointPoolUnavailable
	}
	if confirmationDepth < 0 {
		return fmt.Errorf("indexer checkpoint: negative confirmation depth %d", confirmationDepth)
	}
	address := strings.ToLower(contractAddress)
	lastSafe := ComputeLastSafeBlock(blockNumber, confirmationDepth, deployBlock)

	// Postgres BIGINT is signed int64; refuse to bind values that
	// overflow so we never accidentally write a negative checkpoint.
	if blockNumber > maxSignedInt64 || lastSafe > maxSignedInt64 || deployBlock > maxSignedInt64 {
		return fmt.Errorf("indexer checkpoint: block number %d exceeds bigint range", blockNumber)
	}

	_, err := s.pool.Exec(ctx, `
		INSERT INTO web3_indexer_state (
			chain_id,
			contract_address,
			last_processed_block,
			last_safe_block,
			confirmation_depth,
			updated_at
		)
		VALUES ($1, $2, $3, $4, $5, NOW())
		ON CONFLICT (chain_id, contract_address) DO UPDATE
		SET last_processed_block = GREATEST(
		      web3_indexer_state.last_processed_block,
		      EXCLUDED.last_processed_block
		    ),
		    last_safe_block = GREATEST(
		      web3_indexer_state.last_safe_block,
		      EXCLUDED.last_safe_block
		    ),
		    confirmation_depth = EXCLUDED.confirmation_depth,
		    updated_at = NOW()
	`, chainID, address, int64(blockNumber), int64(lastSafe), confirmationDepth)
	if err != nil {
		return fmt.Errorf("upsert web3_indexer_state: %w", err)
	}
	return nil
}

// ComputeLastSafeBlock rewinds blockNumber by confirmationDepth, clamped
// below by deployBlock. Mirrors getLastSafeBlock() in the NestJS service.
//
// When blockNumber <= confirmationDepth, the rewind would underflow, so
// we return deployBlock directly.
func ComputeLastSafeBlock(blockNumber uint64, confirmationDepth int, deployBlock uint64) uint64 {
	if confirmationDepth <= 0 {
		if blockNumber > deployBlock {
			return blockNumber
		}
		return deployBlock
	}
	depth := uint64(confirmationDepth)
	if blockNumber <= depth {
		return deployBlock
	}
	rewound := blockNumber - depth
	if rewound > deployBlock {
		return rewound
	}
	return deployBlock
}

const maxSignedInt64 = uint64(1<<63 - 1)

func chooseCheckpoint(lastProcessed, lastSafe int64, deployBlock uint64) uint64 {
	// Prefer last_safe_block — that's the depth we've crossed past the
	// reorg-risk window. Zero is a valid safe checkpoint, matching the
	// NestJS `lastSafeBlock ?? lastProcessedBlock` semantics.
	if lastSafe >= 0 {
		return uint64(lastSafe)
	}

	// Fall back to last_processed_block only for corrupted/pre-migration
	// rows where the safe value is negative or unavailable to the caller.
	if lastProcessed > 0 {
		return uint64(lastProcessed)
	}

	return deployBlock
}
