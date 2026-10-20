package web3events

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
)

func (r *Repository) recordAttempt(ctx context.Context, in attemptWrite) (TransactionRow, error) {
	return retryAbortedAttempt(ctx, func() (TransactionRow, error) {
		return r.recordAttemptOnce(ctx, in)
	})
}

// Row-first existing writers and action-first initial misses can deadlock,
// including when a mined receipt updates same-nonce siblings. PostgreSQL's
// 40P01 guarantees the victim transaction aborted, so identical input is safe
// to retry after its rollback. Connection errors and unknown commit outcomes
// cannot make that guarantee. Statement diagnostics may precede a rollback.
func retryAbortedAttempt(ctx context.Context, write func() (TransactionRow, error)) (TransactionRow, error) {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if err := ctx.Err(); err != nil {
			return TransactionRow{}, err
		}
		var row TransactionRow
		row, err = write()
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "40P01" {
			return row, err
		}
	}
	return TransactionRow{}, err
}

func (r *Repository) recordAttemptOnce(ctx context.Context, in attemptWrite) (TransactionRow, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return TransactionRow{}, fmt.Errorf("web3events: begin attempt write: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	row, err := recordAttemptTx(ctx, tx, in)
	if err != nil {
		return TransactionRow{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return TransactionRow{}, fmt.Errorf("web3events: commit attempt write: %w", err)
	}
	return row, nil
}
