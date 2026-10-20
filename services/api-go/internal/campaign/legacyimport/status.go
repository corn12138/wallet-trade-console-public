package legacyimport

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// FenceState is one fence of a run.
type FenceState struct {
	CampaignID string `json:"campaignId"`
	FencedBy   string `json:"fencedBy"`
	FencedAt   string `json:"fencedAt"`
	LiftedBy   string `json:"liftedBy,omitempty"`
	LiftedAt   string `json:"liftedAt,omitempty"`
}

// RunStatus is the shareable state of a run: no raw values, no wallets.
type RunStatus struct {
	RunID        string         `json:"runId"`
	Status       string         `json:"status"`
	Operator     string         `json:"operator"`
	Algorithm    string         `json:"algorithm"`
	LimitHit     string         `json:"limitHit,omitempty"`
	SourceSHA256 string         `json:"sourceSha256,omitempty"`
	CapturedAt   string         `json:"capturedAt,omitempty"`
	Rows         int64          `json:"rows"`
	Bytes        int64          `json:"bytes"`
	RetainUntil  string         `json:"retainUntil"`
	AbandonedBy  string         `json:"abandonedBy,omitempty"`
	Decisions    map[string]int `json:"decisions"`
	Fences       []FenceState   `json:"fences"`
}

func Status(ctx context.Context, pool *pgxpool.Pool, runID string) (RunStatus, error) {
	status := RunStatus{RunID: runID, Decisions: map[string]int{}, Fences: []FenceState{}}
	err := pgx.BeginTxFunc(ctx, pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var limitHit, source, abandonedBy *string
		var capturedAt *time.Time
		var rows, bytes *int64
		var retain time.Time
		err := tx.QueryRow(ctx, `SELECT status, operator, algorithm, limit_hit, source_sha256, captured_at, row_count, byte_count, retain_until, abandoned_by
			FROM campaign_legacy_import_runs WHERE id=$1`, runID).Scan(&status.Status, &status.Operator, &status.Algorithm, &limitHit, &source, &capturedAt, &rows, &bytes, &retain, &abandonedBy)
		if errors.Is(err, pgx.ErrNoRows) {
			return errors.New("run not found")
		}
		if err != nil {
			return err
		}
		status.LimitHit, status.SourceSHA256, status.AbandonedBy = deref(limitHit), deref(source), deref(abandonedBy)
		if capturedAt != nil {
			status.CapturedAt = capturedAt.UTC().Format(time.RFC3339Nano)
		}
		if rows != nil {
			status.Rows, status.Bytes = *rows, *bytes
		}
		status.RetainUntil = retain.UTC().Format(time.RFC3339Nano)
		decisions, err := tx.Query(ctx, `SELECT decision, count(*) FROM campaign_legacy_import_items WHERE run_id=$1 GROUP BY decision`, runID)
		if err != nil {
			return err
		}
		for decisions.Next() {
			var decision string
			var count int
			if err = decisions.Scan(&decision, &count); err != nil {
				decisions.Close()
				return err
			}
			status.Decisions[decision] = count
		}
		decisions.Close()
		if err = decisions.Err(); err != nil {
			return err
		}
		fences, err := tx.Query(ctx, `SELECT campaign_id, fenced_by, fenced_at, lifted_by, lifted_at FROM campaign_legacy_write_fences WHERE run_id=$1 ORDER BY campaign_id`, runID)
		if err != nil {
			return err
		}
		defer fences.Close()
		for fences.Next() {
			var fence FenceState
			var fencedAt time.Time
			var liftedBy *string
			var liftedAt *time.Time
			if err = fences.Scan(&fence.CampaignID, &fence.FencedBy, &fencedAt, &liftedBy, &liftedAt); err != nil {
				return err
			}
			fence.FencedAt, fence.LiftedBy = fencedAt.UTC().Format(time.RFC3339Nano), deref(liftedBy)
			if liftedAt != nil {
				fence.LiftedAt = liftedAt.UTC().Format(time.RFC3339Nano)
			}
			status.Fences = append(status.Fences, fence)
		}
		return fences.Err()
	})
	return status, err
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
