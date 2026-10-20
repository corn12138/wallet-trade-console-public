package durableoutbox

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// EffectTx exposes database operations but not Commit/Rollback. The receipt
// owner controls transaction lifetime. Callbacks must not issue transaction-
// control SQL or external effects; they are trusted application adapters.
type EffectTx interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type effectTx struct{ tx pgx.Tx }

func (e effectTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return e.tx.Exec(ctx, sql, args...)
}
func (e effectTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return e.tx.Query(ctx, sql, args...)
}
func (e effectTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return e.tx.QueryRow(ctx, sql, args...)
}
