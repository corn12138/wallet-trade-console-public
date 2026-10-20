package legacyimport

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Limits bound one capture. A capture that would exceed any of them archives
// nothing and is recorded as incomplete: an incomplete run can never be
// fenced, verified or imported, so it cannot become approval material.
type Limits struct {
	MaxRows     int64
	MaxBytes    int64
	MaxDuration time.Duration
}

// DefaultLimits are deliberately modest; an operator raises them explicitly.
var DefaultLimits = Limits{MaxRows: 1_000_000, MaxBytes: 512 << 20, MaxDuration: 10 * time.Minute}

// DefaultRetention is how long the raw archive is kept before it may be purged.
const DefaultRetention = 180 * 24 * time.Hour

type CaptureOptions struct {
	Operator  string
	Limits    Limits
	Retention time.Duration
}

// CaptureResult is the shareable summary of a capture. Raw values stay in the
// archive table; Report carries digests and wallet hashes only.
type CaptureResult struct {
	RunID        string         `json:"runId"`
	Status       string         `json:"status"`
	LimitHit     string         `json:"limitHit,omitempty"`
	SourceSHA256 string         `json:"sourceSha256,omitempty"`
	Snapshot     string         `json:"snapshot,omitempty"`
	CapturedAt   string         `json:"capturedAt,omitempty"`
	Rows         int64          `json:"rows"`
	Bytes        int64          `json:"bytes"`
	Decisions    map[string]int `json:"decisions"`
	Report       *Report        `json:"report,omitempty"`
}

// ErrIncomplete means a limit was reached: nothing was archived and the run is
// recorded as incomplete.
var ErrIncomplete = errors.New("legacy capture reached a limit; nothing was archived and the run is incomplete")

var operatorPattern = regexp.MustCompile(`^[A-Za-z0-9._@:-]{1,96}$`)

// Plan decisions. 'import-unpublished' exists only as the outcome of a recorded
// review decision (LG-02b); a capture never assigns it.
const (
	DecisionImportPublic     = "import-public"
	DecisionReviewRequired   = "review-required"
	DecisionBlocked          = "blocked"
	DecisionAlreadyVersioned = "already-versioned"
)

// publicStatuses are the legacy states the old API listed publicly.
var publicStatuses = map[string]bool{"upcoming": true, "active": true, "ended": true}

// decide maps the inventory classification to a plan decision. Only a
// campaign the old API showed publicly, with no blocker and nothing to review,
// may become public; anything else needs a human decision or stays out.
func decide(c Campaign) string {
	switch c.Classification {
	case "already-versioned":
		return DecisionAlreadyVersioned
	case "blocked":
		return DecisionBlocked
	case "importable":
		if publicStatuses[c.Status] {
			return DecisionImportPublic
		}
	}
	return DecisionReviewRequired
}

// Capture archives every legacy row and builds the import plan in one
// REPEATABLE READ snapshot, together with the read-only inventory of that same
// snapshot. The run is recorded as 'capturing' first, so an interrupted capture
// leaves evidence; it can then only be abandoned.
func Capture(ctx context.Context, pool *pgxpool.Pool, options CaptureOptions) (CaptureResult, error) {
	if pool == nil {
		return CaptureResult{}, errors.New("database pool required")
	}
	if !operatorPattern.MatchString(options.Operator) {
		return CaptureResult{}, errors.New("operator must be 1-96 characters of letters, digits and ._@:-")
	}
	limits := options.Limits
	if limits == (Limits{}) {
		limits = DefaultLimits
	}
	if limits.MaxRows <= 0 || limits.MaxBytes <= 0 || limits.MaxDuration <= 0 {
		return CaptureResult{}, errors.New("every capture limit must be positive")
	}
	retention := options.Retention
	if retention <= 0 {
		retention = DefaultRetention
	}
	runID, err := newRunID()
	if err != nil {
		return CaptureResult{}, err
	}
	limitsJSON, _ := json.Marshal(map[string]int64{"maxRows": limits.MaxRows, "maxBytes": limits.MaxBytes, "maxDurationMs": limits.MaxDuration.Milliseconds()})
	if _, err = pool.Exec(ctx, `INSERT INTO campaign_legacy_import_runs(id,algorithm,operator,limits,retain_until)
		VALUES ($1,$2,$3,$4,clock_timestamp() + make_interval(secs => $5))`, runID, DigestAlgorithm, options.Operator, limitsJSON, retention.Seconds()); err != nil {
		return CaptureResult{}, err
	}
	captureCtx, cancel := context.WithTimeout(ctx, limits.MaxDuration)
	defer cancel()
	result, hit, err := capture(captureCtx, pool, runID, limits)
	if hit == "" && err != nil && captureCtx.Err() != nil && ctx.Err() == nil {
		hit = "maxDuration"
	}
	markCtx, markCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer markCancel()
	if hit != "" {
		tag, markErr := pool.Exec(markCtx, `UPDATE campaign_legacy_import_runs SET status='incomplete', limit_hit=$2, finished_at=clock_timestamp()
			WHERE id=$1 AND status='capturing'`, runID, hit)
		switch {
		case markErr != nil:
			return CaptureResult{RunID: runID, Status: runStatus(markCtx, pool, runID), Decisions: map[string]int{}}, errors.Join(ErrIncomplete, markErr)
		case tag.RowsAffected() != 1:
			status := runStatus(markCtx, pool, runID)
			return CaptureResult{RunID: runID, Status: status, Decisions: map[string]int{}},
				fmt.Errorf("the capture reached %s, but the run is %s and was not marked incomplete", hit, status)
		}
		return CaptureResult{RunID: runID, Status: "incomplete", LimitHit: hit, Decisions: map[string]int{}}, ErrIncomplete
	}
	if err != nil {
		// Normally the run stays 'capturing': evidence of an interrupted attempt.
		return CaptureResult{RunID: runID, Status: runStatus(markCtx, pool, runID), Decisions: map[string]int{}}, err
	}
	return result, nil
}

// runStatus reports a run's recorded status for an error result, or "unknown".
func runStatus(ctx context.Context, pool *pgxpool.Pool, runID string) string {
	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM campaign_legacy_import_runs WHERE id=$1`, runID).Scan(&status); err != nil {
		return "unknown"
	}
	return status
}

type budget struct {
	limits      Limits
	rows, bytes int64
}

func (b *budget) add(raw string) string {
	b.rows++
	b.bytes += int64(len(raw))
	switch {
	case b.rows > b.limits.MaxRows:
		return "maxRows"
	case b.bytes > b.limits.MaxBytes:
		return "maxBytes"
	}
	return ""
}

var archiveColumns = []string{"run_id", "source_table", "row_id", "campaign_id", "row_json", "row_sha256"}

func capture(ctx context.Context, pool *pgxpool.Pool, runID string, limits Limits) (CaptureResult, string, error) {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return CaptureResult{}, "", err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	// Lock the run first. An abandon racing the capture waits for it; a run that
	// is no longer capturing is refused here, even when there is nothing to
	// archive for the archive guard to refuse.
	var status string
	if err = tx.QueryRow(ctx, `SELECT status FROM campaign_legacy_import_runs WHERE id=$1 FOR NO KEY UPDATE`, runID).Scan(&status); err != nil {
		return CaptureResult{}, "", err
	}
	if status != "capturing" {
		return CaptureResult{}, "", fmt.Errorf("run is %s, not capturing", status)
	}
	if captureHook != nil {
		captureHook()
	}
	report := Report{SchemaVersion: 2, DigestAlgorithm: DigestAlgorithm, Campaigns: []Campaign{}, Findings: []Finding{},
		TimeBasis: "legacy columns are TIMESTAMP(3) without time zone; read as UTC per the Prisma writer convention, which the database cannot prove"}
	var captured time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp(), pg_current_snapshot()::text, current_setting('server_version_num')`).Scan(&captured, &report.Snapshot, &report.ServerVersion); err != nil {
		return CaptureResult{}, "", err
	}
	report.CapturedAt = captured.UTC().Format(time.RFC3339Nano)
	inventorySource := sha256.New()
	if err = inventoryCampaigns(ctx, tx, captured, &report, inventorySource); err != nil {
		return CaptureResult{}, "", err
	}
	for _, table := range []string{"campaign_participants", "campaign_reminders"} {
		if err = inventoryInteractions(ctx, tx, table, &report, inventorySource); err != nil {
			return CaptureResult{}, "", err
		}
	}
	classify(&report)
	report.SourceSHA256 = hex.EncodeToString(inventorySource.Sum(nil))

	archiveSource := sha256.New()
	interactions := interactionDigests{}
	spent := &budget{limits: limits}
	hit := ""
	for _, table := range sourceTables {
		err = scanSource(ctx, tx, table, func(page []sourceRow) error {
			batch := make([][]any, 0, len(page))
			for _, row := range page {
				if hit = spent.add(row.raw); hit != "" {
					return errLimit
				}
				_, _ = archiveSource.Write([]byte(row.line()))
				if table != "launchpad_campaigns" {
					interactions.add(row)
				}
				batch = append(batch, []any{runID, row.table, row.id, row.campaignID, row.raw, row.digest})
			}
			_, err := tx.CopyFrom(ctx, pgx.Identifier{"campaign_legacy_archive_rows"}, archiveColumns, pgx.CopyFromRows(batch))
			return err
		})
		if errors.Is(err, errLimit) {
			return CaptureResult{}, hit, nil
		}
		if err != nil {
			return CaptureResult{}, "", err
		}
	}
	// Both views read the same snapshot in the same order; a difference means a
	// bug in one of them, never a reason to proceed.
	if got := hex.EncodeToString(archiveSource.Sum(nil)); got != report.SourceSHA256 {
		return CaptureResult{}, "", fmt.Errorf("archive digest %s differs from the inventory digest %s of the same snapshot", got[:16], report.SourceSHA256[:16])
	}

	findings := map[string][]Finding{}
	for _, finding := range report.Findings {
		findings[finding.CampaignID] = append(findings[finding.CampaignID], finding)
	}
	decisions := map[string]int{}
	items := make([][]any, 0, len(report.Campaigns))
	for _, c := range report.Campaigns {
		if c.ParticipantRows > math.MaxInt32 || c.ReminderRows > math.MaxInt32 {
			return CaptureResult{}, "", fmt.Errorf("campaign %s has more interaction rows than a plan item can record", c.ID)
		}
		decision := decide(c)
		decisions[decision]++
		own := findings[c.ID]
		if own == nil {
			own = []Finding{}
		}
		raw, err := json.Marshal(own)
		if err != nil {
			return CaptureResult{}, "", err
		}
		items = append(items, []any{runID, c.ID, c.LegacyRowSHA256, interactions.sum(c.ID), int32(c.ParticipantRows), int32(c.ReminderRows), decision, raw})
	}
	if _, err = tx.CopyFrom(ctx, pgx.Identifier{"campaign_legacy_import_items"},
		[]string{"run_id", "campaign_id", "legacy_row_sha256", "interactions_sha256", "participant_rows", "reminder_rows", "decision", "findings"},
		pgx.CopyFromRows(items)); err != nil {
		return CaptureResult{}, "", err
	}
	if _, err = tx.Exec(ctx, `UPDATE campaign_legacy_import_runs SET status='captured', snapshot=$2, captured_at=$3, source_sha256=$4,
		row_count=$5, byte_count=$6, finished_at=clock_timestamp() WHERE id=$1 AND status='capturing'`,
		runID, report.Snapshot, captured, report.SourceSHA256, spent.rows, spent.bytes); err != nil {
		return CaptureResult{}, "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return CaptureResult{}, "", err
	}
	return CaptureResult{RunID: runID, Status: "captured", SourceSHA256: report.SourceSHA256, Snapshot: report.Snapshot, CapturedAt: report.CapturedAt,
		Rows: spent.rows, Bytes: spent.bytes, Decisions: decisions, Report: &report}, "", nil
}

var errLimit = errors.New("capture limit reached")

// captureHook runs inside the capture transaction once the run is locked
// (tests only).
var captureHook func()

func newRunID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	v := hex.EncodeToString(raw[:])
	return v[:8] + "-" + v[8:12] + "-" + v[12:16] + "-" + v[16:20] + "-" + v[20:], nil
}
