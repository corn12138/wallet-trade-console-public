package durableoutbox

import (
	"context"
	"regexp"
)

var consumerName = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,99}$`)

// Consume commits a local database effect and receipt atomically. The callback
// must use only this transaction and perform no external effects. Publication
// acknowledgement is independent: a message may arrive before the worker acks.
func (r *Repository) Consume(ctx context.Context, consumer, id string, supports func(Event) bool, apply func(context.Context, EffectTx, Event) error) (bool, error) {
	if r == nil || r.pool == nil || !consumerName.MatchString(consumer) || supports == nil || apply == nil {
		return false, ErrInvalidInput
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var event Event
	if err = scanEvent(tx.QueryRow(ctx, `SELECT `+eventColumns+` FROM durable_outbox o WHERE o.id=$1::uuid`, id), &event); err != nil {
		return false, err
	}
	// Lock by consumer and stream, not event: concurrent adjacent versions must
	// observe the predecessor receipt after its transaction commits.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, consumer+":"+event.Stream); err != nil {
		return false, err
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM consumer_receipts WHERE consumer_id=$1 AND event_id=$2::uuid)`, consumer, id).Scan(&exists); err != nil {
		return false, err
	}
	if exists {
		return false, tx.Commit(ctx)
	}
	if !supports(event) {
		return false, ErrUnsupported
	}
	// Receipts are inserted only through this ordered path. By induction, the
	// immediate predecessor proves the prefix without rescanning all history.
	// Operator backfills must preserve this invariant; out-of-band receipts
	// are not a supported way to skip a missing or unsupported event.
	if event.Version > 1 {
		var predecessor bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM durable_outbox o JOIN consumer_receipts r ON r.event_id=o.id AND r.consumer_id=$1
 WHERE o.aggregate_id=$2 AND o.aggregate_version=$3)`, consumer, event.Stream, event.Version-1).Scan(&predecessor); err != nil {
			return false, err
		}
		if !predecessor {
			return false, ErrSequenceGap
		}
	}
	if err = apply(ctx, effectTx{tx}, event); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO consumer_receipts(consumer_id,event_id,consumed_at) VALUES($1,$2::uuid,clock_timestamp())`, consumer, id); err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}
