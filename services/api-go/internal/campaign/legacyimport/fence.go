package legacyimport

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFenced means the campaign has no active fence for the run, so its
// legacy rows may still change and cannot be verified for import.
var ErrNotFenced = errors.New("campaign has no active fence for this run")

// fenceLockSQL takes the campaign's exclusive fence lock. Every legacy writer
// takes the same key in shared mode before checking for a fence (see the
// migration), so holding it waits for writers already in flight and blocks new
// ones until the transaction ends.
const fenceLockSQL = `SELECT pg_advisory_xact_lock(hashtextextended('campaign-legacy-fence:' || $1, 0))`

func validCampaignIDs(ids []string) error {
	if len(ids) == 0 {
		return errors.New("at least one campaign is required")
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || len(id) > 160 || seen[id] {
			return fmt.Errorf("campaign IDs must be non-empty, at most 160 characters and distinct")
		}
		seen[id] = true
	}
	return nil
}

// Fence starts refusing every write to the campaigns' legacy rows. The database
// accepts a fence only for a campaign the captured plan may import, and at most
// one active fence per campaign. Fencing again under the same run is a no-op;
// a fence this run already lifted cannot be renewed, because writes may have
// happened meanwhile: capture a new run instead.
func Fence(ctx context.Context, pool *pgxpool.Pool, runID string, campaignIDs []string, operator string) error {
	if !operatorPattern.MatchString(operator) {
		return errors.New("operator must be 1-96 characters of letters, digits and ._@:-")
	}
	if err := validCampaignIDs(campaignIDs); err != nil {
		return err
	}
	return pgx.BeginTxFunc(ctx, pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		for _, id := range campaignIDs {
			var lifted *string
			err := tx.QueryRow(ctx, `SELECT lifted_by FROM campaign_legacy_write_fences WHERE run_id=$1 AND campaign_id=$2`, runID, id).Scan(&lifted)
			switch {
			case err == nil && lifted == nil:
				continue
			case err == nil:
				return fmt.Errorf("the fence of %s under this run was lifted; capture a new run", id)
			case !errors.Is(err, pgx.ErrNoRows):
				return err
			}
			if _, err = tx.Exec(ctx, `INSERT INTO campaign_legacy_write_fences(campaign_id,run_id,fenced_by) VALUES ($1,$2,$3)`, id, runID, operator); err != nil {
				return fmt.Errorf("fence %s: %w", id, describe(err))
			}
		}
		return nil
	})
}

// Lift ends the run's active fences on the campaigns, recording who lifted
// them. It reports the campaigns that had no active fence under this run.
func Lift(ctx context.Context, pool *pgxpool.Pool, runID string, campaignIDs []string, operator string) ([]string, error) {
	if !operatorPattern.MatchString(operator) {
		return nil, errors.New("operator must be 1-96 characters of letters, digits and ._@:-")
	}
	if err := validCampaignIDs(campaignIDs); err != nil {
		return nil, err
	}
	rows, err := pool.Query(ctx, `UPDATE campaign_legacy_write_fences SET lifted_by=$3, lifted_at=clock_timestamp()
		WHERE run_id=$1 AND campaign_id = ANY($2) AND lifted_at IS NULL RETURNING campaign_id`, runID, campaignIDs, operator)
	if err != nil {
		return nil, describe(err)
	}
	lifted, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, describe(err)
	}
	done := map[string]bool{}
	for _, id := range lifted {
		done[id] = true
	}
	var missing []string
	for _, id := range campaignIDs {
		if !done[id] {
			missing = append(missing, id)
		}
	}
	return missing, nil
}

// Abandon closes a run that will not be imported. Every fence of the run must
// be lifted first (the database refuses otherwise).
func Abandon(ctx context.Context, pool *pgxpool.Pool, runID, operator string) error {
	if !operatorPattern.MatchString(operator) {
		return errors.New("operator must be 1-96 characters of letters, digits and ._@:-")
	}
	tag, err := pool.Exec(ctx, `UPDATE campaign_legacy_import_runs SET status='abandoned', abandoned_by=$2, finished_at=coalesce(finished_at, clock_timestamp())
		WHERE id=$1 AND status IN ('capturing','captured','incomplete')`, runID, operator)
	if err != nil {
		return describe(err)
	}
	if tag.RowsAffected() != 1 {
		return errors.New("run not found or already abandoned")
	}
	return nil
}

// Drift is one difference between the archived and the current legacy rows.
// RowRef is a digest prefix: verification results are shared for review.
type Drift struct {
	Table  string `json:"table"`
	RowRef string `json:"rowRef"`
	Change string `json:"change"`
}

type VerifyResult struct {
	RunID      string  `json:"runId"`
	CampaignID string  `json:"campaignId"`
	Verified   bool    `json:"verified"`
	Rows       int     `json:"rows"`
	Drift      []Drift `json:"drift"`
}

// verifyHook runs after verification read the rows and before it commits,
// while the exclusive fence lock is held (tests only).
var verifyHook func()

// VerifyFenced re-reads the campaign's complete legacy set under the exclusive fence
// lock and compares it row by row with the run's archive. The lock waits for
// writers already in flight, so a row inserted before the fence took effect but
// committed after it is seen here as added; afterwards the fence refuses
// writers. A run that is not captured, or a campaign without an active fence,
// is refused.
func VerifyFenced(ctx context.Context, pool *pgxpool.Pool, runID, campaignID string) (VerifyResult, error) {
	var result VerifyResult
	err := pgx.BeginTxFunc(ctx, pool, pgx.TxOptions{IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var err error
		result, err = verifyInTx(ctx, tx, runID, campaignID)
		if err == nil && verifyHook != nil {
			verifyHook()
		}
		return err
	})
	return result, err
}

// VerifyInTx is VerifyFenced inside the caller's READ COMMITTED transaction.
// The exclusive fence lock it takes lasts until that transaction ends, so the
// activation verifies, writes and compares under one lock.
func VerifyInTx(ctx context.Context, tx pgx.Tx, runID, campaignID string) (VerifyResult, error) {
	return verifyInTx(ctx, tx, runID, campaignID)
}

// verifyInTx is the check activation repeats inside its own transaction. The
// transaction must be READ COMMITTED: every statement after the lock sees the
// writers the lock waited for.
func verifyInTx(ctx context.Context, tx pgx.Tx, runID, campaignID string) (VerifyResult, error) {
	result := VerifyResult{RunID: runID, CampaignID: campaignID, Drift: []Drift{}}
	var isolation string
	if err := tx.QueryRow(ctx, `SELECT current_setting('transaction_isolation')`).Scan(&isolation); err != nil {
		return result, err
	}
	if isolation != "read committed" {
		return result, fmt.Errorf("verification needs a READ COMMITTED transaction, not %s", isolation)
	}
	if _, err := tx.Exec(ctx, fenceLockSQL, campaignID); err != nil {
		return result, err
	}
	var fenced bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM campaign_legacy_write_fences f JOIN campaign_legacy_import_runs r ON r.id = f.run_id
		WHERE f.run_id=$1 AND f.campaign_id=$2 AND f.lifted_at IS NULL AND r.status='captured')`, runID, campaignID).Scan(&fenced); err != nil {
		return result, err
	}
	if !fenced {
		return result, ErrNotFenced
	}
	live := map[string]string{}
	for _, query := range []struct{ table, sql string }{
		{"launchpad_campaigns", `SELECT t.id, row_to_json(t)::text FROM launchpad_campaigns t WHERE t.id=$1`},
		{"campaign_participants", `SELECT t.id, row_to_json(t)::text FROM campaign_participants t WHERE t.campaign_id=$1`},
		{"campaign_reminders", `SELECT t.id, row_to_json(t)::text FROM campaign_reminders t WHERE t.campaign_id=$1`},
	} {
		rows, err := tx.Query(ctx, query.sql, campaignID)
		if err != nil {
			return result, err
		}
		for rows.Next() {
			var id, raw string
			if err = rows.Scan(&id, &raw); err != nil {
				rows.Close()
				return result, err
			}
			digest, err := canonicalRow([]byte(raw))
			if err != nil {
				rows.Close()
				return result, err
			}
			live[query.table+"\x00"+id] = digest
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return result, err
		}
	}
	archived := map[string]string{}
	rows, err := tx.Query(ctx, `SELECT source_table, row_id, row_sha256 FROM campaign_legacy_archive_rows WHERE run_id=$1 AND campaign_id=$2`, runID, campaignID)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var table, id, digest string
		if err = rows.Scan(&table, &id, &digest); err != nil {
			rows.Close()
			return result, err
		}
		archived[table+"\x00"+id] = digest
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return result, err
	}
	for key, digest := range live {
		table := key[:indexNUL(key)]
		switch before, ok := archived[key]; {
		case !ok:
			result.Drift = append(result.Drift, Drift{Table: table, RowRef: digest[:16], Change: "added"})
		case before != digest:
			result.Drift = append(result.Drift, Drift{Table: table, RowRef: before[:16] + ">" + digest[:16], Change: "changed"})
		}
	}
	for key, digest := range archived {
		if _, ok := live[key]; !ok {
			result.Drift = append(result.Drift, Drift{Table: key[:indexNUL(key)], RowRef: digest[:16], Change: "removed"})
		}
	}
	sort.Slice(result.Drift, func(i, j int) bool {
		a, b := result.Drift[i], result.Drift[j]
		return a.Table+a.Change+a.RowRef < b.Table+b.Change+b.RowRef
	})
	result.Rows = len(live)
	result.Verified = len(result.Drift) == 0 && len(archived) > 0
	return result, nil
}

func indexNUL(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == 0 {
			return i
		}
	}
	return len(s)
}

// describe keeps PostgreSQL's guard message and SQLSTATE and drops anything
// that could carry connection details.
func describe(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return fmt.Errorf("%s (SQLSTATE %s)", pgErr.Message, pgErr.Code)
	}
	return err
}
