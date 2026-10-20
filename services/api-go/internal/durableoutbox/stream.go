package durableoutbox

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var eventIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type StreamPage struct {
	Head       int64
	Events     []Event
	ObservedAt time.Time
}

// ReadStream is an internal read port, not an authorization boundary. The domain
// adapter must derive the exact stream from trusted identity, validate payload
// compatibility and expose only its public DTO. Reads never create receipts.
// A nil cursor anchors the head before the caller refreshes an authoritative
// snapshot, so changes concurrent with that refresh remain in the next page.
func (r *Repository) ReadStream(ctx context.Context, stream string, after *int64, limit int) (StreamPage, error) {
	page := StreamPage{Events: []Event{}}
	if r == nil || r.pool == nil || len(stream) == 0 || len(stream) > 500 || strings.ContainsRune(stream, 0) ||
		limit < 1 || limit > 100 || (after != nil && *after < 0) {
		return page, ErrInvalidInput
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return page, err
	}
	defer tx.Rollback(ctx)
	if err = tx.QueryRow(ctx, `SELECT COALESCE((SELECT last_stream_version FROM outbox_streams WHERE stream_key=$1),0),clock_timestamp()`, stream).Scan(&page.Head, &page.ObservedAt); err != nil {
		return page, err
	}
	if after != nil && *after < page.Head {
		// Publication is an advisory transport outcome. Committed events remain
		// readable through a notification outage; business consumption is separate.
		rows, err := tx.Query(ctx, `SELECT `+eventColumns+` FROM durable_outbox o
 WHERE o.aggregate_id=$1 AND o.aggregate_version>$2 AND o.aggregate_version<=$3
 ORDER BY o.aggregate_version LIMIT $4`, stream, *after, page.Head, limit)
		if err != nil {
			return page, err
		}
		for rows.Next() {
			var event Event
			if err = scanEvent(rows, &event); err != nil {
				rows.Close()
				return page, err
			}
			page.Events = append(page.Events, event)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return page, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return page, err
	}
	return page, nil
}

// EventStream maps an advisory wakeup's event ID to its source and stream so a
// listener can narrow durable re-reads. It never authorizes or acknowledges;
// found=false is a normal answer for an unknown or foreign ID.
func (r *Repository) EventStream(ctx context.Context, id string) (source, stream string, found bool, err error) {
	if r == nil || r.pool == nil || !eventIDPattern.MatchString(id) {
		return "", "", false, ErrInvalidInput
	}
	err = r.pool.QueryRow(ctx, `SELECT source,aggregate_id FROM durable_outbox WHERE id=$1::uuid`, id).Scan(&source, &stream)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	return source, stream, true, nil
}
