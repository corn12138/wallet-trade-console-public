package durableoutbox

import (
	"context"
	"crypto/rand"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct{ pool *pgxpool.Pool }

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

const eventColumns = `o.id::text,o.source,o.aggregate_id,o.aggregate_version,o.topic,o.owner_scope,o.payload_version,o.payload,o.occurred_at`

func scanEvent(row pgx.Row, e *Event, extra ...any) error {
	fields := []any{&e.ID, &e.Source, &e.Stream, &e.Version, &e.Topic, &e.OwnerScope, &e.PayloadVersion, &e.Payload, &e.OccurredAt}
	return row.Scan(append(fields, extra...)...)
}
func leaseToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// Claim takes at most one event per stream. The predecessor predicate includes
// dead letters and leased rows, so SKIP LOCKED cannot skip a stream's order.
func (r *Repository) Claim(ctx context.Context, o Options) ([]Delivery, error) {
	if err := o.validate(); err != nil {
		return nil, err
	}
	if r == nil || r.pool == nil {
		return nil, ErrInvalidInput
	}
	token, err := leaseToken()
	if err != nil {
		return nil, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	// A crash on the final attempt must become observable rather than leave an
	// expired row that can neither be reclaimed nor identified as dead-lettered.
	_, err = tx.Exec(ctx, `WITH exhausted AS (SELECT id FROM durable_outbox
 WHERE source=$1 AND published_at IS NULL AND dead_lettered_at IS NULL AND attempts >= $2
 AND (lease_until IS NULL OR lease_until <= clock_timestamp()) ORDER BY available_at,id FOR UPDATE SKIP LOCKED LIMIT $3)
 UPDATE durable_outbox o SET dead_lettered_at=clock_timestamp(),error_code='leaseExpired',lease_owner=NULL,lease_until=NULL
 FROM exhausted e WHERE o.id=e.id`, o.Source, o.MaxAttempts, o.Batch)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `WITH candidates AS (
 SELECT o.id FROM durable_outbox o WHERE o.source=$1 AND o.published_at IS NULL AND o.dead_lettered_at IS NULL
 AND o.available_at <= clock_timestamp() AND (o.lease_until IS NULL OR o.lease_until <= clock_timestamp()) AND o.attempts < $2
 AND NOT EXISTS (SELECT 1 FROM durable_outbox prior WHERE prior.aggregate_id=o.aggregate_id AND prior.aggregate_version<o.aggregate_version AND prior.published_at IS NULL)
 ORDER BY o.available_at,o.id FOR UPDATE OF o SKIP LOCKED LIMIT $3)
 UPDATE durable_outbox o SET lease_owner=$4::uuid,lease_until=clock_timestamp()+($5::bigint * interval '1 millisecond'),attempts=o.attempts+1
 FROM candidates c WHERE o.id=c.id RETURNING `+eventColumns+`,o.lease_owner::text,o.attempts`, o.Source, o.MaxAttempts, o.Batch, token, o.Lease.Milliseconds())
	if err != nil {
		return nil, err
	}
	result := []Delivery{}
	for rows.Next() {
		var d Delivery
		if err = scanEvent(rows, &d.Event, &d.Token, &d.Attempts); err != nil {
			rows.Close()
			return nil, err
		}
		result = append(result, d)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}

func (r *Repository) Ack(ctx context.Context, d Delivery) error {
	if r == nil || r.pool == nil {
		return ErrInvalidInput
	}
	tag, err := r.pool.Exec(ctx, `UPDATE durable_outbox SET published_at=clock_timestamp(),lease_owner=NULL,lease_until=NULL,error_code=NULL
 WHERE id=$1::uuid AND lease_owner=$2::uuid AND lease_until>clock_timestamp() AND published_at IS NULL AND dead_lettered_at IS NULL`, d.ID, d.Token)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrLeaseLost
	}
	return nil
}

// Reject stores only closed diagnostics, never an error or event payload.
func (r *Repository) Reject(ctx context.Context, d Delivery, o Options, unsupported bool) error {
	if r == nil || r.pool == nil {
		return ErrInvalidInput
	}
	if err := o.validate(); err != nil {
		return err
	}
	code := "publisherUnavailable"
	if unsupported {
		code = "unsupportedSchema"
	}
	tag, err := r.pool.Exec(ctx, `UPDATE durable_outbox SET lease_owner=NULL,lease_until=NULL,error_code=$3,
 available_at=clock_timestamp()+($4::bigint * interval '1 millisecond'),
 dead_lettered_at=CASE WHEN $5 OR attempts >= $6 THEN clock_timestamp() ELSE NULL END
 WHERE id=$1::uuid AND lease_owner=$2::uuid AND lease_until>clock_timestamp() AND published_at IS NULL AND dead_lettered_at IS NULL`, d.ID, d.Token, code, o.retryDelay(d.Attempts).Milliseconds(), unsupported, o.MaxAttempts)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrLeaseLost
	}
	return nil
}
