package outboxrepair

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type rowReader interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

const snapshotSQL = `public.outbox_replay_snapshot(o.replay_generation,o.attempts,o.dead_lettered_at,o.error_code,o.published_at,o.available_at,o.lease_until,observed.at)`

type deliveryRow struct {
	snapshot  Snapshot
	raw       []byte
	errorCode *string
}

func decodeSnapshot(raw []byte, snapshot *Snapshot) error { return json.Unmarshal(raw, snapshot) }
func (e deliveryRow) matches(expected Expected) bool {
	return e.snapshot.Generation == expected.Generation && e.snapshot.DeadLetterAt != nil && e.snapshot.DeadLetterAt.Equal(expected.DeadLetterAt) && e.snapshot.Attempts == expected.Attempts && e.errorCode != nil && *e.errorCode == expected.ErrorCode && e.snapshot.PublishedAt == nil && !e.snapshot.LeaseActive
}
func lockEvent(ctx context.Context, tx pgx.Tx, id string) (deliveryRow, error) {
	var source string
	if err := tx.QueryRow(ctx, `SELECT source FROM public.durable_outbox WHERE id=$1 AND source='campaign' FOR UPDATE`, id).Scan(&source); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return deliveryRow{}, ErrNotFound
		}
		return deliveryRow{}, ErrUnavailable
	}
	return readEvent(ctx, tx, id)
}
func readEvent(ctx context.Context, tx rowReader, id string) (deliveryRow, error) {
	var row deliveryRow
	err := tx.QueryRow(ctx, `WITH observed AS (SELECT clock_timestamp() AS at) SELECT `+snapshotSQL+`,o.error_code FROM public.durable_outbox o CROSS JOIN observed WHERE o.id=$1 AND o.source='campaign'`, id).Scan(&row.raw, &row.errorCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return row, ErrNotFound
	}
	if err != nil || decodeSnapshot(row.raw, &row.snapshot) != nil {
		return row, ErrUnavailable
	}
	return row, nil
}
func loadState(ctx context.Context, q rowReader, id string) (State, error) {
	var state State
	i := &state.Intent
	err := q.QueryRow(ctx, `SELECT command_id::text,event_id::text,principal,authority_role,authority_version,expected_generation,expected_dead_lettered_at,expected_attempts,expected_error_code,fingerprint,reason,request_id,accepted_at
 FROM public.outbox_replay_intents WHERE command_id=$1`, id).Scan(&i.CommandID, &i.EventID, &i.Authority.Principal, &i.Authority.Role, &i.Authority.Version, &i.Expected.Generation, &i.Expected.DeadLetterAt, &i.Expected.Attempts, &i.Expected.ErrorCode, &i.Fingerprint, &i.Reason, &i.RequestID, &i.AcceptedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return state, ErrNotFound
	}
	if err != nil {
		return State{}, ErrUnavailable
	}
	var before, after []byte
	outcome := &Outcome{}
	err = q.QueryRow(ctx, `SELECT status,code,before_state,after_state,completed_at FROM public.outbox_replay_outcomes WHERE command_id=$1`, id).Scan(&outcome.Status, &outcome.Code, &before, &after, &outcome.CompletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return state, nil
	}
	if err != nil || decodeSnapshot(before, &outcome.Before) != nil || decodeSnapshot(after, &outcome.After) != nil {
		return State{}, ErrUnavailable
	}
	state.Outcome = outcome
	return state, nil
}
func (s *Postgres) Query(ctx context.Context, id, principal string) (State, error) {
	if !uuid.MatchString(id) || !identifier.MatchString(principal) {
		return State{}, ErrInvalidInput
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.begin(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return State{}, err
	}
	defer rollback(tx)
	state, err := loadState(ctx, tx, id)
	if err != nil {
		return State{}, err
	}
	if state.Intent.Authority.Principal != principal {
		return State{}, ErrNotFound
	}
	if tx.Commit(ctx) != nil {
		return State{}, ErrUnavailable
	}
	return state, nil
}
func (s *Postgres) ListDeadLetters(ctx context.Context, position *Position, limit int) (Page, error) {
	if limit < 1 || limit > 50 || (position != nil && (!uuid.MatchString(position.EventID) || !validTime(position.At))) {
		return Page{}, ErrInvalidInput
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.begin(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return Page{}, err
	}
	defer rollback(tx)
	page := Page{Entries: []Entry{}}
	if tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&page.ObservedAt) != nil {
		return Page{}, ErrUnavailable
	}
	var at *time.Time
	var id *string
	if position != nil {
		at = &position.At
		id = &position.EventID
	}
	rows, err := tx.Query(ctx, `SELECT event_id::text,topic,stream_version,stream_digest,occurred_at,replay_generation,attempts,dead_lettered_at,error_code,published_at,available_at,lease_active
 FROM public.campaign_outbox_dead_letters WHERE ($1::timestamptz IS NULL OR (dead_lettered_at,event_id)>($1,$2::uuid)) ORDER BY dead_lettered_at,event_id LIMIT $3`, at, id, limit+1)
	if err != nil {
		return Page{}, ErrUnavailable
	}
	for rows.Next() {
		var e Entry
		if err = rows.Scan(&e.EventID, &e.Topic, &e.StreamVersion, &e.StreamDigest, &e.OccurredAt, &e.Generation, &e.Attempts, &e.DeadLetterAt, &e.ErrorCode, &e.PublishedAt, &e.AvailableAt, &e.LeaseActive); err != nil {
			rows.Close()
			return Page{}, ErrUnavailable
		}
		page.Entries = append(page.Entries, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return Page{}, ErrUnavailable
	}
	if len(page.Entries) > limit {
		page.Entries = page.Entries[:limit]
		last := page.Entries[limit-1]
		page.Next = &Position{At: *last.DeadLetterAt, EventID: last.EventID}
	}
	if tx.Commit(ctx) != nil {
		return Page{}, ErrUnavailable
	}
	return page, nil
}
