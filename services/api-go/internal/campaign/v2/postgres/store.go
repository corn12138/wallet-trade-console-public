package postgres

import (
	"context"
	"errors"
	"time"

	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/application"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

var _ application.Store = (*Store)(nil)

type transaction struct {
	ctx        context.Context
	tx         pgx.Tx
	campaignID string
}

// The aggregate lock is also the serialization point for command, personal
// state and stream writes. No network capability lookup runs while holding it.
func (s *Store) Transact(ctx context.Context, scope application.LockScope, apply func(application.Transaction) error) error {
	if s.pool == nil {
		return unavailable("campaign database unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return classify(err)
	}
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), time.Second)
		defer done()
		_ = tx.Rollback(cleanup)
	}()
	adapter := &transaction{ctx: ctx, tx: tx}
	if scope.CreationKey != "" {
		// The server-generated campaign ID does not exist yet. Identical create
		// commands instead serialize on the fixed collection command identity.
		_, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, scope.CreationKey)
	} else if scope.CampaignID != "" {
		_, err = adapter.Publication(scope.CampaignID)
	} else {
		err = v2.InvalidInput("campaign transaction scope missing")
	}
	if err != nil {
		return classify(err)
	}
	if err = apply(adapter); err != nil {
		return classify(err)
	}
	return classify(tx.Commit(ctx))
}

func (t *transaction) Now() (time.Time, error) {
	var now time.Time
	err := t.tx.QueryRow(t.ctx, `SELECT clock_timestamp()`).Scan(&now)
	return now.UTC(), err
}

func unavailable(detail string) error {
	return &v2.DomainError{Code: "CAMPAIGN_DATA_UNAVAILABLE", Status: 503, Detail: detail}
}

func classify(err error) error {
	if err == nil {
		return nil
	}
	var domain *v2.DomainError
	if errors.As(err, &domain) {
		return err
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" && pg.ConstraintName == "campaign_definitions_slug_key" {
		return &v2.DomainError{Code: "CAMPAIGN_SLUG_CONFLICT", Status: 409, Detail: "campaign slug already exists"}
	}
	// Driver errors can contain command payloads or schema details. The HTTP
	// boundary gets a stable error; operational logging must remain redacted.
	return unavailable("campaign transaction failed")
}

func notFound() error {
	return &v2.DomainError{Code: "CAMPAIGN_NOT_FOUND", Status: 404, Detail: "campaign resource not found"}
}
