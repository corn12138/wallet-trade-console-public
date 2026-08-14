package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Transfer statuses. A transfer is only ever advanced by an OBSERVED on-chain
// event — the relayer never writes an optimistic status, because a status the
// chain has not confirmed is exactly the fake progress this package replaced.
const (
	StatusInitiated = "INITIATED"
	StatusFulfilled = "FULFILLED"
	StatusRefunded  = "REFUNDED"
)

// ErrStoreUnavailable is returned when no database is configured.
var ErrStoreUnavailable = errors.New("bridge: transfer store unavailable")

// ErrTransferNotFound is returned when no row matches.
var ErrTransferNotFound = errors.New("bridge: transfer not found")

// Transfer is one durable cross-chain transfer.
type Transfer struct {
	TransferID      string
	SrcChainID      int
	DstChainID      int
	SrcGateway      string
	Sender          string
	Recipient       string
	SrcToken        string
	DstToken        string
	Amount          string // base units, exact
	Status          string
	DepositTxHash   string
	DepositLogIndex int
	DepositBlock    int64
	DepositedAt     time.Time
	FulfillTxHash   string
	FulfillBlock    *int64
	FulfilledAt     *time.Time
	RefundTxHash    string
	RefundedAt      *time.Time
	LastError       string
	Attempts        int
	UpdatedAt       time.Time
}

// Store persists transfers.
type Store struct{ pool *pgxpool.Pool }

// NewStore builds a store. A nil pool makes every method return
// ErrStoreUnavailable, which the API reports honestly rather than as "no
// transfers exist".
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Available reports whether a database is wired.
func (s *Store) Available() bool { return s != nil && s.pool != nil }

// RecordInitiated inserts a transfer observed from a BridgeInitiated log.
// Idempotent on (src_chain_id, transfer_id): re-scanning the same block range
// updates nothing and re-reports the existing row.
func (s *Store) RecordInitiated(ctx context.Context, t Transfer) (bool, error) {
	if !s.Available() {
		return false, ErrStoreUnavailable
	}
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO bridge_transfers (
			id, transfer_id, src_chain_id, dst_chain_id, src_gateway,
			sender, recipient, src_token, dst_token, amount, status,
			deposit_tx_hash, deposit_log_index, deposit_block, deposited_at,
			created_at, updated_at
		) VALUES (
			gen_random_uuid()::text, $1, $2, $3, $4,
			$5, $6, $7, $8, $9::numeric, $10,
			$11, $12, $13, $14,
			NOW(), NOW()
		)
		ON CONFLICT (src_chain_id, transfer_id) DO NOTHING
	`,
		strings.ToLower(t.TransferID), t.SrcChainID, t.DstChainID, strings.ToLower(t.SrcGateway),
		strings.ToLower(t.Sender), strings.ToLower(t.Recipient), strings.ToLower(t.SrcToken),
		nullableLower(t.DstToken), t.Amount, StatusInitiated,
		strings.ToLower(t.DepositTxHash), t.DepositLogIndex, t.DepositBlock, t.DepositedAt,
	)
	if err != nil {
		return false, fmt.Errorf("bridge: record initiated: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// MarkFulfilled advances a transfer from an observed BridgeFulfilled log.
//
// The WHERE clause pins status to INITIATED so a replayed log cannot resurrect
// a refunded transfer or overwrite an existing fulfillment.
func (s *Store) MarkFulfilled(ctx context.Context, srcChainID int, transferID, txHash string, block int64, at time.Time) (bool, error) {
	if !s.Available() {
		return false, ErrStoreUnavailable
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE bridge_transfers
		   SET status = $1, fulfill_tx_hash = $2, fulfill_block = $3,
		       fulfilled_at = $4, last_error = NULL, updated_at = NOW()
		 WHERE src_chain_id = $5 AND transfer_id = $6 AND status = $7
	`, StatusFulfilled, strings.ToLower(txHash), block, at, srcChainID, strings.ToLower(transferID), StatusInitiated)
	if err != nil {
		return false, fmt.Errorf("bridge: mark fulfilled: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// MarkRefunded advances a transfer from an observed BridgeRefunded log.
func (s *Store) MarkRefunded(ctx context.Context, srcChainID int, transferID, txHash string, at time.Time) (bool, error) {
	if !s.Available() {
		return false, ErrStoreUnavailable
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE bridge_transfers
		   SET status = $1, refund_tx_hash = $2, refunded_at = $3, updated_at = NOW()
		 WHERE src_chain_id = $4 AND transfer_id = $5 AND status = $6
	`, StatusRefunded, strings.ToLower(txHash), at, srcChainID, strings.ToLower(transferID), StatusInitiated)
	if err != nil {
		return false, fmt.Errorf("bridge: mark refunded: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// RecordAttempt notes a relayer failure against a transfer so a stuck transfer
// explains itself instead of sitting on a generic "pending".
func (s *Store) RecordAttempt(ctx context.Context, srcChainID int, transferID, failure string) error {
	if !s.Available() {
		return ErrStoreUnavailable
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE bridge_transfers
		   SET attempts = attempts + 1, last_error = $1, updated_at = NOW()
		 WHERE src_chain_id = $2 AND transfer_id = $3
	`, truncate(failure, 500), srcChainID, strings.ToLower(transferID))
	if err != nil {
		return fmt.Errorf("bridge: record attempt: %w", err)
	}
	return nil
}

const transferColumns = `
	transfer_id, src_chain_id, dst_chain_id, src_gateway, sender, recipient,
	src_token, COALESCE(dst_token, ''), amount::text, status,
	deposit_tx_hash, deposit_log_index, deposit_block, deposited_at,
	COALESCE(fulfill_tx_hash, ''), fulfill_block, fulfilled_at,
	COALESCE(refund_tx_hash, ''), refunded_at, COALESCE(last_error, ''),
	attempts, updated_at`

func scanTransfer(row pgx.Row) (Transfer, error) {
	var t Transfer
	err := row.Scan(
		&t.TransferID, &t.SrcChainID, &t.DstChainID, &t.SrcGateway, &t.Sender, &t.Recipient,
		&t.SrcToken, &t.DstToken, &t.Amount, &t.Status,
		&t.DepositTxHash, &t.DepositLogIndex, &t.DepositBlock, &t.DepositedAt,
		&t.FulfillTxHash, &t.FulfillBlock, &t.FulfilledAt,
		&t.RefundTxHash, &t.RefundedAt, &t.LastError,
		&t.Attempts, &t.UpdatedAt,
	)
	return t, err
}

// FindByTransferID looks a transfer up by its canonical id. The source chain is
// optional (0 = any) because the id is globally unique by construction.
func (s *Store) FindByTransferID(ctx context.Context, srcChainID int, transferID string) (Transfer, error) {
	if !s.Available() {
		return Transfer{}, ErrStoreUnavailable
	}
	q := `SELECT ` + transferColumns + ` FROM bridge_transfers WHERE transfer_id = $1`
	args := []any{strings.ToLower(transferID)}
	if srcChainID > 0 {
		q += ` AND src_chain_id = $2`
		args = append(args, srcChainID)
	}
	q += ` LIMIT 1`

	t, err := scanTransfer(s.pool.QueryRow(ctx, q, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return Transfer{}, ErrTransferNotFound
	}
	if err != nil {
		return Transfer{}, fmt.Errorf("bridge: find transfer: %w", err)
	}
	return t, nil
}

// ListPending returns transfers still awaiting delivery, oldest first — the
// relayer's work queue.
func (s *Store) ListPending(ctx context.Context, limit int) ([]Transfer, error) {
	if !s.Available() {
		return nil, ErrStoreUnavailable
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+transferColumns+`
		   FROM bridge_transfers
		  WHERE status = $1
		  ORDER BY deposited_at ASC
		  LIMIT $2`, StatusInitiated, limit)
	if err != nil {
		return nil, fmt.Errorf("bridge: list pending: %w", err)
	}
	defer rows.Close()

	out := []Transfer{}
	for rows.Next() {
		t, scanErr := scanTransfer(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("bridge: scan pending: %w", scanErr)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ListByAddress returns transfers a wallet sent or received, newest first.
func (s *Store) ListByAddress(ctx context.Context, address string, limit int) ([]Transfer, error) {
	if !s.Available() {
		return nil, ErrStoreUnavailable
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	addr := strings.ToLower(strings.TrimSpace(address))
	rows, err := s.pool.Query(ctx,
		`SELECT `+transferColumns+`
		   FROM bridge_transfers
		  WHERE sender = $1 OR recipient = $1
		  ORDER BY deposited_at DESC
		  LIMIT $2`, addr, limit)
	if err != nil {
		return nil, fmt.Errorf("bridge: list by address: %w", err)
	}
	defer rows.Close()

	out := []Transfer{}
	for rows.Next() {
		t, scanErr := scanTransfer(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("bridge: scan transfer: %w", scanErr)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// HighestScannedBlock returns the newest deposit block recorded for a chain, so
// the relayer can resume without rescanning from genesis. Zero when empty.
func (s *Store) HighestScannedBlock(ctx context.Context, srcChainID int) (int64, error) {
	if !s.Available() {
		return 0, ErrStoreUnavailable
	}
	var block *int64
	err := s.pool.QueryRow(ctx,
		`SELECT MAX(deposit_block) FROM bridge_transfers WHERE src_chain_id = $1`, srcChainID).Scan(&block)
	if err != nil {
		return 0, fmt.Errorf("bridge: highest scanned block: %w", err)
	}
	if block == nil {
		return 0, nil
	}
	return *block, nil
}

// Counts summarizes stored transfers by status, for the status endpoint.
func (s *Store) Counts(ctx context.Context) (map[string]int64, error) {
	if !s.Available() {
		return nil, ErrStoreUnavailable
	}
	rows, err := s.pool.Query(ctx, `SELECT status, COUNT(*) FROM bridge_transfers GROUP BY status`)
	if err != nil {
		return nil, fmt.Errorf("bridge: counts: %w", err)
	}
	defer rows.Close()

	out := map[string]int64{}
	for rows.Next() {
		var status string
		var n int64
		if err := rows.Scan(&status, &n); err != nil {
			return nil, fmt.Errorf("bridge: scan counts: %w", err)
		}
		out[status] = n
	}
	return out, rows.Err()
}

func nullableLower(v string) any {
	trimmed := strings.ToLower(strings.TrimSpace(v))
	if trimmed == "" {
		return nil
	}
	return trimmed
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// ParseAmount validates a base-unit integer string.
func ParseAmount(raw string) (*big.Int, bool) {
	v, ok := new(big.Int).SetString(strings.TrimSpace(raw), 10)
	if !ok || v.Sign() <= 0 {
		return nil, false
	}
	return v, true
}

// RelayerHeartbeat is one relayer's liveness row.
type RelayerHeartbeat struct {
	RelayerAddress string
	LastSeenAt     time.Time
	Chains         []int
	// GasBalances is chainID -> wei as a decimal string. Exact: a uint256 never
	// goes through a float, here or anywhere else in this package.
	GasBalances    map[string]string
	CanDeliver     bool
	LastCycleError string
}

// WriteHeartbeat records that the relayer completed a cycle. Best-effort: a
// heartbeat failure must never stop delivery, so callers log and continue.
func (s *Store) WriteHeartbeat(ctx context.Context, hb RelayerHeartbeat) error {
	if !s.Available() {
		return ErrStoreUnavailable
	}
	addr := strings.ToLower(strings.TrimSpace(hb.RelayerAddress))
	if addr == "" {
		// Observe-only deployments have no signer address to key on. There is
		// nothing dishonest about that — they genuinely cannot deliver — so it
		// is not an error, just nothing to record.
		return nil
	}
	balances := []byte("{}")
	if len(hb.GasBalances) > 0 {
		if b, err := json.Marshal(hb.GasBalances); err == nil {
			balances = b
		}
	}
	var lastErr *string
	if hb.LastCycleError != "" {
		msg := hb.LastCycleError
		if len(msg) > 500 {
			msg = msg[:500]
		}
		lastErr = &msg
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO bridge_relayer_heartbeat
			(relayer_address, last_seen_at, chains, gas_balances, can_deliver, last_cycle_error, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, now(), now())
		ON CONFLICT (relayer_address) DO UPDATE SET
			last_seen_at = EXCLUDED.last_seen_at,
			chains = EXCLUDED.chains,
			gas_balances = EXCLUDED.gas_balances,
			can_deliver = EXCLUDED.can_deliver,
			last_cycle_error = EXCLUDED.last_cycle_error,
			updated_at = now()`,
		addr, hb.LastSeenAt.UTC(), hb.Chains, balances, hb.CanDeliver, lastErr)
	if err != nil {
		return fmt.Errorf("bridge: write heartbeat: %w", err)
	}
	return nil
}
