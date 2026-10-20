package campaign

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
)

// Querier runs the legacy reads: the pool, or a caller's transaction.
type Querier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type Repository struct {
	pool *pgxpool.Pool
	// tx and at serve reads inside another transaction at a fixed instant:
	// the legacy activation compares the legacy answer before and after its
	// writes (docs/campaign-legacy-activation-protocol-2026-10-09.md §1.5).
	tx Querier
	at time.Time
}

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// NewRepositoryAt reads through q at the fixed instant now instead of the
// statement time. It serves reads only.
func NewRepositoryAt(q Querier, now time.Time) *Repository { return &Repository{tx: q, at: now.UTC()} }

func (r *Repository) reads() Querier {
	switch {
	case r == nil:
		return nil
	case r.tx != nil:
		return r.tx
	case r.pool != nil:
		return r.pool
	}
	return nil
}

// clock is the read instant: the statement time, or the fixed instant passed
// as parameter n.
func (r *Repository) clock(n int) (string, []any) {
	if r.tx == nil {
		return "statement_timestamp()", nil
	}
	return fmt.Sprintf("$%d::timestamptz", n), []any{r.at}
}

// Legacy columns are TIMESTAMP without time zone holding UTC wall-clock time
// (Prisma convention). Comparing them with a timestamptz would reinterpret them
// in the session TimeZone, shifting the phase on any non-UTC database session,
// so the read instant is converted to UTC wall-clock time first.
func legacyPhase(clock string) string {
	return `CASE WHEN c.end_date<=c.start_date THEN 'unsupported'
 WHEN (` + clock + ` AT TIME ZONE 'UTC')<c.start_date THEN 'upcoming'
 WHEN (` + clock + ` AT TIME ZONE 'UTC')>=c.end_date THEN 'ended' ELSE 'active' END`
}
func campaignColumns(clock string) string {
	return `c.id, c.title, c.description, c.banner, c.start_date, c.end_date,
 ` + legacyPhase(clock) + `, c.reward, c.participants, c.metadata, c.created_at, c.updated_at`
}

// Definition ownership, not publication state, determines whether a row may
// appear in legacy reads. The formal campaign migration must precede this API.
const legacyOnly = `NOT EXISTS (SELECT 1 FROM campaign_definitions d WHERE d.id = c.id)`
const legacyPublic = legacyOnly + ` AND c.status IN ('upcoming','active','ended')`

func (r *Repository) ListAll(ctx context.Context, status string, limits ...int) ([]Campaign, error) {
	db := r.reads()
	if db == nil {
		return nil, ErrPoolUnavailable
	}
	if err := validStatus(status); err != nil {
		return nil, err
	}
	limit, err := boundedLimit(limits...)
	if err != nil {
		return nil, err
	}
	clock, args := r.clock(3)
	rows, err := db.Query(ctx, `SELECT `+campaignColumns(clock)+` FROM launchpad_campaigns c
 WHERE `+legacyPublic+` AND ($1 = '' OR `+legacyPhase(clock)+` = $1)
 ORDER BY c.start_date DESC, c.id ASC LIMIT $2`, append([]any{status, limit}, args...)...)
	if err != nil {
		return nil, fmt.Errorf("query legacy campaigns: %w", err)
	}
	defer rows.Close()
	return scanCampaigns(rows)
}

// The old time argument remains source-compatible; PostgreSQL supplies the
// authoritative clock for both filtering and the returned phase.
func (r *Repository) GetActive(ctx context.Context, _ time.Time, limits ...int) ([]Campaign, error) {
	return r.ListAll(ctx, "active", limits...)
}

func (r *Repository) FindByID(ctx context.Context, id string) (Campaign, error) {
	db := r.reads()
	if db == nil {
		return Campaign{}, ErrPoolUnavailable
	}
	var c Campaign
	var metadata []byte
	clock, args := r.clock(2)
	err := db.QueryRow(ctx, `SELECT `+campaignColumns(clock)+` FROM launchpad_campaigns c
 WHERE c.id=$1 AND `+legacyPublic, append([]any{id}, args...)...).Scan(&c.ID, &c.Title, &c.Description, &c.Banner,
		&c.StartDate, &c.EndDate, &c.Status, &c.Reward, &c.Participants, &metadata, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Campaign{}, ErrNotFound
	}
	if err != nil {
		return Campaign{}, fmt.Errorf("query legacy campaign: %w", err)
	}
	c.Metadata = decodeMetadata(metadata)
	return c, nil
}

func scanCampaigns(rows pgx.Rows) ([]Campaign, error) {
	out := make([]Campaign, 0)
	for rows.Next() {
		var c Campaign
		var metadata []byte
		if err := rows.Scan(&c.ID, &c.Title, &c.Description, &c.Banner, &c.StartDate, &c.EndDate, &c.Status, &c.Reward,
			&c.Participants, &metadata, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan launchpad_campaigns: %w", err)
		}
		c.Metadata = decodeMetadata(metadata)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate launchpad_campaigns: %w", err)
	}
	return out, nil
}

// decodeMetadata turns the JSONB column into a generic any. Bad JSON or
// SQL NULL surface as nil — matches Prisma's `Json?` behavior.
func decodeMetadata(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil
	}
	return v
}
