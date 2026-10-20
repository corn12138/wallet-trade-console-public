package postgres

import (
	"context"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/application"
	"github.com/jackc/pgx/v5"
)

// Bound both wire size and per-snapshot schema/hash work. Large multibyte copy
// may end a page before its item limit; the cursor resumes without skipping it.
const queryPageBudget = 128 * 1024

var _ application.ReadStore = (*Store)(nil)

type querySnapshot struct {
	ctx       context.Context
	tx        pgx.Tx
	now       time.Time
	revisions map[string]readRevision
}

func (q *querySnapshot) timestamp() string { return q.now.UTC().Format(time.RFC3339Nano) }

// A single MVCC snapshot ties the publication, personal state, count and DB
// clock together. Reads never acquire the writer's aggregate row locks.
func (s *Store) readSnapshot(ctx context.Context, read func(*querySnapshot) error) error {
	if s == nil || s.pool == nil {
		return unavailable("campaign database unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return classify(err)
	}
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), time.Second)
		defer done()
		_ = tx.Rollback(cleanup)
	}()
	q := &querySnapshot{ctx: ctx, tx: tx}
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&q.now); err != nil {
		return classify(err)
	}
	if err = read(q); err != nil {
		return classify(err)
	}
	return classify(tx.Commit(ctx))
}
