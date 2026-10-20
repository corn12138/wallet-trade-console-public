package web3events

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// WorkbenchReader is the owner-scoped read surface. Owner and chain are
// arguments of every method because the HTTP layer may never widen them.
type WorkbenchReader interface {
	ListActions(ctx context.Context, q ActionListQuery) (ActionPage, error)
	GetAction(ctx context.Context, owner string, chainID int, actionID string) (ActionRow, bool, time.Time, error)
	ListAttempts(ctx context.Context, q AttemptListQuery) (AttemptPage, bool, error)
}

type ActionListQuery struct {
	Owner   string
	ChainID int
	Status  string // empty: no filter
	Limit   int
	Cursor  *ActionCursorState
}

type ActionPage struct {
	Rows       []ActionRow
	HasMore    bool
	HasUpdates bool
	AsOf       time.Time
}

type AttemptListQuery struct {
	Owner    string
	ChainID  int
	ActionID string
	Limit    int
	After    *AttemptKey
}

type AttemptPage struct {
	Rows    []AttemptRow
	HasMore bool
	AsOf    time.Time
}

// Stored timestamps are timestamp(3) without time zone in UTC; parameters are
// passed in the same form so keyset comparisons are exact.
const sqlTimeLayout = "2006-01-02 15:04:05.000"

func sqlTime(t time.Time) string { return t.UTC().Format(sqlTimeLayout) }

// readSnapshot runs fn in one read-only REPEATABLE READ transaction so a page,
// its asOf and its update hint describe the same database state.
func (r *Repository) readSnapshot(ctx context.Context, fn func(pgx.Tx) error) error {
	if r.pool == nil {
		return ErrPoolUnavailable
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("web3events: begin workbench read: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func snapshotTime(ctx context.Context, tx pgx.Tx) (time.Time, error) {
	var asOf time.Time
	if err := tx.QueryRow(ctx, `SELECT LOCALTIMESTAMP(3)`).Scan(&asOf); err != nil {
		return time.Time{}, fmt.Errorf("web3events: read snapshot time: %w", err)
	}
	return asOf.UTC(), nil
}

const actionColumns = `id, chain_id, client_action_id, action_type, status, attempt_count, created_at, updated_at`

func scanAction(row pgx.Row) (ActionRow, error) {
	var a ActionRow
	if err := row.Scan(&a.ID, &a.ChainID, &a.ClientActionID, &a.ActionType, &a.Status, &a.AttemptCount, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return ActionRow{}, err
	}
	a.CreatedAt, a.UpdatedAt = a.CreatedAt.UTC(), a.UpdatedAt.UTC()
	return a, nil
}

func (r *Repository) ListActions(ctx context.Context, q ActionListQuery) (ActionPage, error) {
	var page ActionPage
	err := r.readSnapshot(ctx, func(tx pgx.Tx) error {
		asOf, err := snapshotTime(ctx, tx)
		if err != nil {
			return err
		}
		page.AsOf = asOf
		var filter, afterAt, afterID *string
		if q.Status != "" {
			filter = &q.Status
		}
		if q.Cursor != nil {
			at := sqlTime(q.Cursor.After.At)
			afterAt, afterID = &at, &q.Cursor.After.ID
		}
		rows, err := tx.Query(ctx, `
			SELECT `+actionColumns+`
			FROM web3_transaction_actions
			WHERE owner_address = $1 AND chain_id = $2
			  AND ($3::text IS NULL OR status = $3)
			  AND ($4::timestamp IS NULL OR (created_at, id) < ($4::timestamp, $5::text))
			ORDER BY created_at DESC, id DESC
			LIMIT $6
		`, q.Owner, q.ChainID, filter, afterAt, afterID, q.Limit+1)
		if err != nil {
			return fmt.Errorf("web3events: list actions: %w", err)
		}
		defer rows.Close()
		page.Rows = make([]ActionRow, 0, q.Limit)
		for rows.Next() {
			a, err := scanAction(rows)
			if err != nil {
				return fmt.Errorf("web3events: scan action: %w", err)
			}
			page.Rows = append(page.Rows, a)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("web3events: iterate actions: %w", err)
		}
		if len(page.Rows) > q.Limit {
			page.Rows, page.HasMore = page.Rows[:q.Limit], true
		}
		if q.Cursor == nil {
			return nil
		}
		// Weak consistency: a row created above the first page's high-water
		// mark, or any row of this owner and chain changed after the first
		// page, means the client should reload the first page. The filter is
		// deliberately not applied, so a row moving into or out of it counts.
		return tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM web3_transaction_actions
				WHERE owner_address = $1 AND chain_id = $2
				  AND ((created_at, id) > ($3::timestamp, $4::text) OR updated_at > $5::timestamp)
			)
		`, q.Owner, q.ChainID, sqlTime(q.Cursor.HighWater.At), q.Cursor.HighWater.ID, sqlTime(q.Cursor.AsOf)).Scan(&page.HasUpdates)
	})
	return page, err
}

func getOwnedAction(ctx context.Context, tx pgx.Tx, owner string, chainID int, actionID string) (ActionRow, bool, error) {
	a, err := scanAction(tx.QueryRow(ctx, `
		SELECT `+actionColumns+`
		FROM web3_transaction_actions
		WHERE id = $1 AND owner_address = $2 AND chain_id = $3
	`, actionID, owner, chainID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ActionRow{}, false, nil
	}
	if err != nil {
		return ActionRow{}, false, fmt.Errorf("web3events: get action: %w", err)
	}
	return a, true, nil
}

func (r *Repository) GetAction(ctx context.Context, owner string, chainID int, actionID string) (ActionRow, bool, time.Time, error) {
	var (
		action ActionRow
		found  bool
		asOf   time.Time
	)
	err := r.readSnapshot(ctx, func(tx pgx.Tx) error {
		var err error
		if asOf, err = snapshotTime(ctx, tx); err != nil {
			return err
		}
		action, found, err = getOwnedAction(ctx, tx, owner, chainID, actionID)
		return err
	})
	return action, found, asOf, err
}

func (r *Repository) ListAttempts(ctx context.Context, q AttemptListQuery) (AttemptPage, bool, error) {
	var (
		page  AttemptPage
		found bool
	)
	err := r.readSnapshot(ctx, func(tx pgx.Tx) error {
		asOf, err := snapshotTime(ctx, tx)
		if err != nil {
			return err
		}
		page.AsOf = asOf
		if _, found, err = getOwnedAction(ctx, tx, q.Owner, q.ChainID, q.ActionID); err != nil || !found {
			return err
		}
		var afterNumber *int
		var afterID *int64
		if q.After != nil {
			afterNumber, afterID = &q.After.Number, &q.After.ID
		}
		// An attempt bound to the owner's action always has the owner as its
		// sender (TRANSACTION_OWNER_CONFLICT); the sender and chain predicates
		// keep that true for the read even if a row were ever mis-bound.
		contextColumns := `NULL::text, NULL::bigint, NULL::text`
		if r.contextColumns {
			contextColumns = `execution_context_state, execution_context_revision, execution_context_reason`
		}
		rows, err := tx.Query(ctx, `
			SELECT id, attempt_number, chain_id, tx_hash, from_address, to_address, contract_address, value,
			       sender_nonce, block_number, block_hash, confirmations, gas_used, gas_price, status,
			       replaced_by_hash, failure_code, submitted_at, confirmed_at, finalized_at, last_checked_at, created_at,
			       `+contextColumns+`
			FROM web3_transactions
			WHERE action_id = $1 AND chain_id = $2 AND LOWER(from_address) = $3
			  AND ($4::int IS NULL OR (attempt_number, id) > ($4::int, $5::int))
			ORDER BY attempt_number ASC, id ASC
			LIMIT $6
		`, q.ActionID, q.ChainID, q.Owner, afterNumber, afterID, q.Limit+1)
		if err != nil {
			return fmt.Errorf("web3events: list attempts: %w", err)
		}
		defer rows.Close()
		page.Rows = make([]AttemptRow, 0, q.Limit)
		for rows.Next() {
			var a AttemptRow
			if err := rows.Scan(&a.ID, &a.AttemptNumber, &a.ChainID, &a.TxHash, &a.FromAddress, &a.ToAddress,
				&a.ContractAddress, &a.Value, &a.SenderNonce, &a.BlockNumber, &a.BlockHash, &a.Confirmations,
				&a.GasUsed, &a.GasPrice, &a.Status, &a.ReplacedByHash, &a.FailureCode, &a.SubmittedAt,
				&a.ConfirmedAt, &a.FinalizedAt, &a.LastCheckedAt, &a.CreatedAt,
				&a.ContextState, &a.ContextRevision, &a.ContextReason); err != nil {
				return fmt.Errorf("web3events: scan attempt: %w", err)
			}
			page.Rows = append(page.Rows, a)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("web3events: iterate attempts: %w", err)
		}
		if len(page.Rows) > q.Limit {
			page.Rows, page.HasMore = page.Rows[:q.Limit], true
		}
		return nil
	})
	return page, found, err
}
