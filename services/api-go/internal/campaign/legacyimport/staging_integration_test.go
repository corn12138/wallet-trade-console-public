//go:build integration

package legacyimport_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/legacyimport"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const testOperator = "lg02a-test"

type fixture struct {
	pool                           *pgxpool.Pool
	prefix                         string
	public, review, blocked, other string
	wallet                         string
}

// seedStaging writes four legacy campaigns and orphan rows under a random
// prefix. Cleanup lifts any fence still active on them before deleting the
// rows: legacy tables are shared by every legacy test.
func seedStaging(t *testing.T) fixture {
	t.Helper()
	pool := legacyPool(t)
	random := make([]byte, 6)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	p := "staging-" + hex.EncodeToString(random)
	f := fixture{pool: pool, prefix: p, public: p + "-public", review: p + "-review", blocked: p + "-blocked", other: p + "-other",
		wallet: "0xabcdef0123456789abcdef0123456789abcdef01"}
	t.Cleanup(func() {
		ctx := context.Background()
		if _, err := pool.Exec(ctx, `UPDATE campaign_legacy_write_fences SET lifted_by='test-cleanup', lifted_at=clock_timestamp()
			WHERE campaign_id LIKE $1 AND lifted_at IS NULL`, p+"-%"); err != nil {
			t.Errorf("cleanup fences: %v", err)
		}
		for _, table := range []string{"campaign_participants", "campaign_reminders", "launchpad_campaigns"} {
			if _, err := pool.Exec(ctx, `DELETE FROM `+table+` WHERE id LIKE $1`, p+"-%"); err != nil {
				t.Errorf("cleanup %s: %v", table, err)
			}
		}
	})
	exec(t, pool, `INSERT INTO launchpad_campaigns(id,title,start_date,end_date,status,participants,updated_at) VALUES
		($1,'Public','2020-01-01','2999-01-01','active',1,now()),
		($2,'Review',  '2020-01-01','2020-02-01','ended',0,now()),
		($3,'Blocked', '2020-03-01','2020-02-01','ended',0,now()),
		($4,'Other',   '2020-01-01','2999-01-01','active',0,now())`, f.public, f.review, f.blocked, f.other)
	exec(t, pool, `UPDATE launchpad_campaigns SET reward='100 USDC pool' WHERE id=$1`, f.review)
	exec(t, pool, `INSERT INTO campaign_participants(id,campaign_id,wallet_address,joined_at) VALUES ($1,$2,$3,'2020-01-15')`, p+"-p1", f.public, f.wallet)
	exec(t, pool, `INSERT INTO campaign_reminders(id,campaign_id,wallet_address) VALUES ($1,$2,$3)`, p+"-r1", f.public, f.wallet)
	exec(t, pool, `INSERT INTO campaign_participants(id,campaign_id,wallet_address) VALUES ($1,$2,$3)`, p+"-p2", p+"-missing", f.wallet)
	return f
}

func exec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

func sqlState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

func capture(t *testing.T, pool *pgxpool.Pool) legacyimport.CaptureResult {
	t.Helper()
	result, err := legacyimport.Capture(context.Background(), pool, legacyimport.CaptureOptions{Operator: testOperator})
	if err != nil || result.Status != "captured" {
		t.Fatalf("capture: %+v %v", result, err)
	}
	return result
}

func decisionOf(t *testing.T, pool *pgxpool.Pool, runID, campaignID string) string {
	t.Helper()
	var decision string
	if err := pool.QueryRow(context.Background(), `SELECT decision FROM campaign_legacy_import_items WHERE run_id=$1 AND campaign_id=$2`, runID, campaignID).Scan(&decision); err != nil {
		return "absent"
	}
	return decision
}

// 1 and 7: the archive is the inventory's snapshot, verbatim; plan decisions;
// recapture digests follow the data.
func TestCaptureArchivesTheInventorySnapshotVerbatim(t *testing.T) {
	f := seedStaging(t)
	ctx := context.Background()
	result := capture(t, f.pool)
	inventory, err := legacyimport.Run(ctx, f.pool)
	if err != nil {
		t.Fatal(err)
	}
	if result.SourceSHA256 != inventory.SourceSHA256 || result.SourceSHA256 != result.Report.SourceSHA256 {
		t.Fatal("the archive must cover exactly the inventory's source")
	}
	var archived, live string
	if err = f.pool.QueryRow(ctx, `SELECT row_json FROM campaign_legacy_archive_rows WHERE run_id=$1 AND source_table='launchpad_campaigns' AND row_id=$2`, result.RunID, f.public).Scan(&archived); err != nil {
		t.Fatal(err)
	}
	if err = f.pool.QueryRow(ctx, `SELECT row_to_json(t)::text FROM launchpad_campaigns t WHERE id=$1`, f.public).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if archived != live {
		t.Fatal("the archive must keep the row exactly as PostgreSQL renders it")
	}
	var rows int
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM campaign_legacy_archive_rows WHERE run_id=$1 AND (row_id LIKE $2 OR campaign_id LIKE $2)`, result.RunID, f.prefix+"-%").Scan(&rows); err != nil || rows != 7 {
		t.Fatalf("seeded rows archived: %d %v", rows, err)
	}
	for campaign, want := range map[string]string{f.public: "import-public", f.review: "review-required", f.blocked: "blocked", f.other: "import-public", f.prefix + "-missing": "absent"} {
		if got := decisionOf(t, f.pool, result.RunID, campaign); got != want {
			t.Fatalf("%s decision %s, want %s", campaign, got, want)
		}
	}
	again := capture(t, f.pool)
	if again.RunID == result.RunID || again.SourceSHA256 != result.SourceSHA256 {
		t.Fatal("an unchanged source must recapture to the same digest under a new run")
	}
	exec(t, f.pool, `UPDATE campaign_reminders SET created_at=created_at+interval '1 millisecond' WHERE id=$1`, f.prefix+"-r1")
	if changed := capture(t, f.pool); changed.SourceSHA256 == result.SourceSHA256 {
		t.Fatal("a changed interaction row must change the source digest")
	}
}

// 2: every limit leaves an incomplete run with nothing archived, and nothing
// can be built on it.
func TestCaptureLimitsLeaveAnIncompleteRunWithNothingArchived(t *testing.T) {
	f := seedStaging(t)
	ctx := context.Background()
	for name, limits := range map[string]legacyimport.Limits{
		"maxRows":     {MaxRows: 1, MaxBytes: 1 << 30, MaxDuration: time.Minute},
		"maxBytes":    {MaxRows: 1 << 30, MaxBytes: 16, MaxDuration: time.Minute},
		"maxDuration": {MaxRows: 1 << 30, MaxBytes: 1 << 30, MaxDuration: time.Nanosecond},
	} {
		result, err := legacyimport.Capture(ctx, f.pool, legacyimport.CaptureOptions{Operator: testOperator, Limits: limits})
		if !errors.Is(err, legacyimport.ErrIncomplete) || result.Status != "incomplete" || result.LimitHit != name {
			t.Fatalf("%s: %+v %v", name, result, err)
		}
		var archived, items int
		if err = f.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM campaign_legacy_archive_rows WHERE run_id=$1), (SELECT count(*) FROM campaign_legacy_import_items WHERE run_id=$1)`, result.RunID).Scan(&archived, &items); err != nil || archived+items != 0 {
			t.Fatalf("%s: incomplete run kept %d archive rows and %d items", name, archived, items)
		}
		if err = legacyimport.Fence(ctx, f.pool, result.RunID, []string{f.public}, testOperator); err == nil {
			t.Fatalf("%s: an incomplete run must not fence", name)
		}
		if _, err = legacyimport.RestoreCheck(ctx, f.pool, result.RunID); err == nil {
			t.Fatalf("%s: an incomplete run has no archive to check", name)
		}
		status, err := legacyimport.Status(ctx, f.pool, result.RunID)
		if err != nil || status.Status != "incomplete" || status.LimitHit != name {
			t.Fatalf("%s status: %+v %v", name, status, err)
		}
		if err = legacyimport.Abandon(ctx, f.pool, result.RunID, testOperator); err != nil {
			t.Fatalf("%s abandon: %v", name, err)
		}
	}
}

// 3: the archive cannot be edited, restores exactly, and live drift is reported
// without failing the archive.
func TestArchiveIsImmutableAndRestoresExactly(t *testing.T) {
	f := seedStaging(t)
	ctx := context.Background()
	result := capture(t, f.pool)
	report, err := legacyimport.RestoreCheck(ctx, f.pool, result.RunID)
	if err != nil || !report.Restorable() || report.LiveChanged+report.LiveRemoved+report.LiveAdded != 0 || report.Rows != result.Rows {
		t.Fatalf("restore check: %+v %v", report, err)
	}
	for name, sql := range map[string]string{
		"update":   `UPDATE campaign_legacy_archive_rows SET row_json='{}' WHERE run_id=$1`,
		"delete":   `DELETE FROM campaign_legacy_archive_rows WHERE run_id=$1`,
		"insert":   `INSERT INTO campaign_legacy_archive_rows(run_id,source_table,row_id,campaign_id,row_json,row_sha256) VALUES ($1,'campaign_reminders','x','x','{}',repeat('a',64))`,
		"items":    `UPDATE campaign_legacy_import_items SET decision='blocked' WHERE run_id=$1`,
		"run":      `UPDATE campaign_legacy_import_runs SET operator='someone-else' WHERE id=$1`,
		"run gone": `DELETE FROM campaign_legacy_import_runs WHERE id=$1`,
	} {
		if _, err = f.pool.Exec(ctx, sql, result.RunID); sqlState(err) != "23514" {
			t.Fatalf("%s must be refused by a guard, got %v", name, err)
		}
	}
	exec(t, f.pool, `UPDATE launchpad_campaigns SET title='Other edited' WHERE id=$1`, f.other)
	drifted, err := legacyimport.RestoreCheck(ctx, f.pool, result.RunID)
	if err != nil || !drifted.Restorable() || drifted.LiveChanged != 1 {
		t.Fatalf("live drift: %+v %v", drifted, err)
	}
}

// 4: a fence refuses every write path to the campaign's legacy rows, leaves
// other campaigns writable, admits one active fence, and is lifted one way.
func TestFenceRefusesEveryLegacyWrite(t *testing.T) {
	f := seedStaging(t)
	ctx := context.Background()
	run := capture(t, f.pool)
	// One call is all or nothing: a blocked item rolls back the valid one too.
	if err := legacyimport.Fence(ctx, f.pool, run.RunID, []string{f.other, f.blocked}, testOperator); err == nil {
		t.Fatal("a blocked plan item must not be fenced")
	}
	exec(t, f.pool, `UPDATE launchpad_campaigns SET title='Other still writable' WHERE id=$1`, f.other)
	if err := legacyimport.Fence(ctx, f.pool, run.RunID, []string{f.public, f.review}, testOperator); err != nil {
		t.Fatal(err)
	}
	if err := legacyimport.Fence(ctx, f.pool, run.RunID, []string{f.public}, testOperator); err != nil {
		t.Fatalf("fencing again under the same run is a no-op: %v", err)
	}
	upper := strings.ToUpper(f.wallet[2:])
	for name, sql := range map[string]string{
		"insert participant (case variant)": `INSERT INTO campaign_participants(id,campaign_id,wallet_address) VALUES ('` + f.prefix + `-p9','` + f.public + `','0x` + upper + `')`,
		"update participant":                `UPDATE campaign_participants SET wallet_address=lower(wallet_address) WHERE id='` + f.prefix + `-p1'`,
		"delete participant":                `DELETE FROM campaign_participants WHERE id='` + f.prefix + `-p1'`,
		"insert reminder":                   `INSERT INTO campaign_reminders(id,campaign_id,wallet_address) VALUES ('` + f.prefix + `-r9','` + f.public + `','` + f.wallet + `')`,
		"delete reminder":                   `DELETE FROM campaign_reminders WHERE id='` + f.prefix + `-r1'`,
		"update campaign":                   `UPDATE launchpad_campaigns SET title='Changed' WHERE id='` + f.public + `'`,
		"delete campaign":                   `DELETE FROM launchpad_campaigns WHERE id='` + f.review + `'`,
		"move a row into the fence":         `UPDATE campaign_participants SET campaign_id='` + f.public + `' WHERE id='` + f.prefix + `-p2'`,
	} {
		if _, err := f.pool.Exec(ctx, sql); sqlState(err) != "55006" {
			t.Fatalf("%s must be refused by the fence, got %v", name, err)
		}
	}
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(ctx, `TRUNCATE campaign_reminders`)
	_ = tx.Rollback(ctx)
	if sqlState(err) != "55006" {
		t.Fatalf("truncate must be refused while a fence is active, got %v", err)
	}
	exec(t, f.pool, `INSERT INTO campaign_participants(id,campaign_id,wallet_address) VALUES ($1,$2,$3)`, f.prefix+"-p8", f.other, f.wallet)
	// The compatibility row a v2 create writes under a new ID is not fenced.
	exec(t, f.pool, `INSERT INTO launchpad_campaigns(id,title,start_date,end_date,status,updated_at) VALUES ($1,'New v2 campaign','2020-01-01','2999-01-01','unpublished',now())`, f.prefix+"-v2new")
	second := capture(t, f.pool)
	if err = legacyimport.Fence(ctx, f.pool, second.RunID, []string{f.public}, testOperator); err == nil || !strings.Contains(err.Error(), "23505") {
		t.Fatalf("a campaign admits one active fence: %v", err)
	}
	if err = legacyimport.Abandon(ctx, f.pool, run.RunID, testOperator); err == nil {
		t.Fatal("a run with active fences must not be abandoned")
	}
	if missing, err := legacyimport.Lift(ctx, f.pool, run.RunID, []string{f.public, f.review}, testOperator); err != nil || len(missing) != 0 {
		t.Fatalf("lift: %v %v", missing, err)
	}
	exec(t, f.pool, `UPDATE launchpad_campaigns SET title='Public again' WHERE id=$1`, f.public)
	if err = legacyimport.Fence(ctx, f.pool, run.RunID, []string{f.public}, testOperator); err == nil {
		t.Fatal("a lifted fence cannot be renewed under the same run")
	}
	if missing, _ := legacyimport.Lift(ctx, f.pool, run.RunID, []string{f.public}, testOperator); len(missing) != 1 {
		t.Fatal("lifting twice reports the campaign as not fenced")
	}
	if _, err = f.pool.Exec(ctx, `UPDATE campaign_legacy_write_fences SET lifted_by=NULL, lifted_at=NULL WHERE run_id=$1`, run.RunID); sqlState(err) != "23514" {
		t.Fatalf("a lift is one-way, got %v", err)
	}
	if err = legacyimport.Abandon(ctx, f.pool, run.RunID, testOperator); err != nil {
		t.Fatal(err)
	}
}

// 5: a writer in flight when the fence is requested is waited for; its row is
// then reported as drift. A rolled-back writer leaves the plan valid.
func TestFenceWaitsForWritersInFlightAndVerifySeesThem(t *testing.T) {
	for _, commit := range []bool{true, false} {
		f := seedStaging(t)
		ctx := context.Background()
		run := capture(t, f.pool)
		writer, err := f.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		// A failed check must not leave the writer holding its connection.
		defer func() { _ = writer.Rollback(ctx) }()
		if _, err = writer.Exec(ctx, `INSERT INTO campaign_participants(id,campaign_id,wallet_address) VALUES ($1,$2,$3)`, f.prefix+"-late", f.public, "0x"+strings.Repeat("b", 40)); err != nil {
			t.Fatal(err)
		}
		fenced := make(chan error, 1)
		go func() { fenced <- legacyimport.Fence(ctx, f.pool, run.RunID, []string{f.public}, testOperator) }()
		waitForAdvisoryWaiter(t, f.pool)
		select {
		case err = <-fenced:
			t.Fatalf("the fence must wait for the writer in flight, returned %v", err)
		default:
		}
		if commit {
			err = writer.Commit(ctx)
		} else {
			err = writer.Rollback(ctx)
		}
		if err != nil {
			t.Fatal(err)
		}
		if err = <-fenced; err != nil {
			t.Fatal(err)
		}
		result, err := legacyimport.VerifyFenced(ctx, f.pool, run.RunID, f.public)
		if err != nil {
			t.Fatal(err)
		}
		if commit && (result.Verified || len(result.Drift) != 1 || result.Drift[0].Change != "added" || result.Drift[0].Table != "campaign_participants") {
			t.Fatalf("a committed late writer must be reported: %+v", result)
		}
		if !commit && !result.Verified {
			t.Fatalf("a rolled-back writer leaves the plan valid: %+v", result)
		}
	}
}

// 6: while verification holds the lock, a new writer waits; once verification
// commits the fence refuses it, so the verified set cannot change.
func TestVerifyBlocksNewWritersUntilItCommits(t *testing.T) {
	f := seedStaging(t)
	ctx := context.Background()
	run := capture(t, f.pool)
	if err := legacyimport.Fence(ctx, f.pool, run.RunID, []string{f.public}, testOperator); err != nil {
		t.Fatal(err)
	}
	written := make(chan error, 1)
	restore := legacyimport.SetVerifyHookForTest(func() {
		go func() {
			_, err := f.pool.Exec(ctx, `INSERT INTO campaign_participants(id,campaign_id,wallet_address) VALUES ($1,$2,$3)`, f.prefix+"-racer", f.public, "0x"+strings.Repeat("c", 40))
			written <- err
		}()
		waitForAdvisoryWaiter(t, f.pool)
	})
	defer restore()
	result, err := legacyimport.VerifyFenced(ctx, f.pool, run.RunID, f.public)
	if err != nil || !result.Verified || result.Rows != 3 {
		t.Fatalf("verify: %+v %v", result, err)
	}
	if err = <-written; sqlState(err) != "55006" {
		t.Fatalf("the waiting writer must be refused by the fence, got %v", err)
	}
}

// 4b: a transaction whose snapshot spans the transaction (REPEATABLE READ,
// SERIALIZABLE) cannot act on a fence state its snapshot does not show. Once a
// fence commits after that snapshot, writing a fenced row, truncating a legacy
// table and abandoning the run all fail with a serialization error; a writer
// that meets a fence still being set waits for it, then fails the same way.
func TestFenceHoldsForTransactionsWithAnOlderSnapshot(t *testing.T) {
	for _, isolation := range []pgx.TxIsoLevel{pgx.RepeatableRead, pgx.Serializable} {
		t.Run(string(isolation), func(t *testing.T) {
			f := seedStaging(t)
			ctx := context.Background()
			run := capture(t, f.pool)
			begin := func() pgx.Tx {
				tx, err := f.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: isolation})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = tx.Rollback(ctx) })
				// The first query fixes the transaction's snapshot.
				var fences int
				if err = tx.QueryRow(ctx, `SELECT count(*) FROM campaign_legacy_write_fences`).Scan(&fences); err != nil {
					t.Fatal(err)
				}
				return tx
			}
			stale := map[string]pgx.Tx{"insert": begin(), "truncate": begin(), "abandon": begin()}
			if err := legacyimport.Fence(ctx, f.pool, run.RunID, []string{f.public}, testOperator); err != nil {
				t.Fatal(err)
			}
			for name, sql := range map[string]string{
				"insert":   `INSERT INTO campaign_participants(id,campaign_id,wallet_address) VALUES ('` + f.prefix + `-stale','` + f.public + `','0x` + strings.Repeat("d", 40) + `')`,
				"truncate": `TRUNCATE campaign_reminders`,
				"abandon":  `UPDATE campaign_legacy_import_runs SET status='abandoned', abandoned_by='` + testOperator + `', finished_at=finished_at WHERE id='` + run.RunID + `'`,
			} {
				_, err := stale[name].Exec(ctx, sql)
				_ = stale[name].Rollback(ctx)
				if sqlState(err) != "40001" {
					t.Errorf("%s with a snapshot older than the fence must fail with a serialization error, got %v", name, err)
				}
			}

			writer := begin()
			setting, err := f.pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = setting.Rollback(ctx) }()
			if _, err = setting.Exec(ctx, `INSERT INTO campaign_legacy_write_fences(campaign_id,run_id,fenced_by) VALUES ($1,$2,$3)`, f.review, run.RunID, testOperator); err != nil {
				t.Fatal(err)
			}
			written := make(chan error, 1)
			go func() {
				_, err := writer.Exec(ctx, `UPDATE launchpad_campaigns SET title='Stale' WHERE id=$1`, f.review)
				written <- err
			}()
			waitForLockWaiter(t, f.pool, "")
			select {
			case err = <-written:
				t.Fatalf("the writer must wait for the fence being set, returned %v", err)
			default:
			}
			if err = setting.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err = <-written; sqlState(err) != "40001" {
				t.Fatalf("the waiting writer must fail with a serialization error, got %v", err)
			}
		})
	}
}

// A lift waits for a verification holding the campaign lock, so a verified
// result stands until the verification commits.
func TestLiftWaitsForVerification(t *testing.T) {
	f := seedStaging(t)
	ctx := context.Background()
	run := capture(t, f.pool)
	if err := legacyimport.Fence(ctx, f.pool, run.RunID, []string{f.public}, testOperator); err != nil {
		t.Fatal(err)
	}
	lifted := make(chan error, 1)
	restore := legacyimport.SetVerifyHookForTest(func() {
		go func() {
			_, err := legacyimport.Lift(ctx, f.pool, run.RunID, []string{f.public}, testOperator)
			lifted <- err
		}()
		waitForLockWaiter(t, f.pool, "advisory")
		select {
		case err := <-lifted:
			t.Errorf("the lift must wait for the verification, returned %v", err)
		default:
		}
	})
	defer restore()
	if result, err := legacyimport.VerifyFenced(ctx, f.pool, run.RunID, f.public); err != nil || !result.Verified {
		t.Fatalf("verify: %+v %v", result, err)
	}
	if err := <-lifted; err != nil {
		t.Fatal(err)
	}
	if _, err := legacyimport.VerifyFenced(ctx, f.pool, run.RunID, f.public); !errors.Is(err, legacyimport.ErrNotFenced) {
		t.Fatalf("after the lift the campaign is not fenced: %v", err)
	}
}

// A run never ends abandoned with an active fence: an abandon that meets a
// fence being set waits for it and is refused, and a fence that meets an
// abandon in progress waits for it and is refused.
func TestAbandonAndFenceSerializeOnTheRun(t *testing.T) {
	f := seedStaging(t)
	ctx := context.Background()
	run := capture(t, f.pool)
	setting, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = setting.Rollback(ctx) }()
	if _, err = setting.Exec(ctx, `INSERT INTO campaign_legacy_write_fences(campaign_id,run_id,fenced_by) VALUES ($1,$2,$3)`, f.public, run.RunID, testOperator); err != nil {
		t.Fatal(err)
	}
	abandoned := make(chan error, 1)
	go func() { abandoned <- legacyimport.Abandon(ctx, f.pool, run.RunID, testOperator) }()
	waitForLockWaiter(t, f.pool, "transactionid")
	if err = setting.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-abandoned; err == nil || !strings.Contains(err.Error(), "23514") {
		t.Fatalf("an abandon must be refused once the fence commits: %v", err)
	}
	if missing, err := legacyimport.Lift(ctx, f.pool, run.RunID, []string{f.public}, testOperator); err != nil || len(missing) != 0 {
		t.Fatalf("lift: %v %v", missing, err)
	}

	abandoning, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = abandoning.Rollback(ctx) }()
	if _, err = abandoning.Exec(ctx, `UPDATE campaign_legacy_import_runs SET status='abandoned', abandoned_by=$2 WHERE id=$1`, run.RunID, testOperator); err != nil {
		t.Fatal(err)
	}
	fenced := make(chan error, 1)
	go func() { fenced <- legacyimport.Fence(ctx, f.pool, run.RunID, []string{f.review}, testOperator) }()
	waitForLockWaiter(t, f.pool, "transactionid")
	if err = abandoning.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-fenced; err == nil || !strings.Contains(err.Error(), "23514") {
		t.Fatalf("a fence must be refused once the run is abandoned: %v", err)
	}
}

// A capture and an abandon of the same run serialize on the run row: a run
// abandoned before the capture started is refused even when nothing would be
// archived, and an abandon racing the capture waits for it, so the run ends
// abandoned with its archive instead of a capture that half-happened.
func TestCaptureAndAbandonSerializeOnTheRun(t *testing.T) {
	f := seedStaging(t)
	ctx := context.Background()
	abandoned := newCapturingRun(t, f.pool)
	if err := legacyimport.Abandon(ctx, f.pool, abandoned, testOperator); err != nil {
		t.Fatal(err)
	}
	if _, err := legacyimport.CaptureRunForTest(ctx, f.pool, abandoned); err == nil || !strings.Contains(err.Error(), "run is abandoned") {
		t.Errorf("a run abandoned before the capture must be refused: %v", err)
	}

	racing := newCapturingRun(t, f.pool)
	done := make(chan error, 1)
	restore := legacyimport.SetCaptureHookForTest(func() {
		go func() { done <- legacyimport.Abandon(ctx, f.pool, racing, testOperator) }()
		waitForLockWaiter(t, f.pool, "transactionid")
		select {
		case err := <-done:
			t.Errorf("the abandon must wait for the capture, returned %v", err)
		default:
		}
	})
	defer restore()
	if result, err := legacyimport.CaptureRunForTest(ctx, f.pool, racing); err != nil || result.Status != "captured" {
		t.Fatalf("capture: %+v %v", result, err)
	}
	if err := <-done; err != nil {
		t.Fatalf("the abandon applies once the capture commits: %v", err)
	}
	var status string
	var rows int64
	if err := f.pool.QueryRow(ctx, `SELECT status, row_count FROM campaign_legacy_import_runs WHERE id=$1`, racing).Scan(&status, &rows); err != nil || status != "abandoned" || rows == 0 {
		t.Fatalf("the run ends abandoned with its archive: %s %d %v", status, rows, err)
	}
}

// newCapturingRun records a run that has not captured anything yet.
func newCapturingRun(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	raw[6], raw[8] = raw[6]&0x0f|0x40, raw[8]&0x3f|0x80
	id := fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])
	exec(t, pool, `INSERT INTO campaign_legacy_import_runs(id,algorithm,operator,limits,retain_until) VALUES ($1,'legacy-row-json/v2',$2,'{}',clock_timestamp()+interval '1 day')`, id, testOperator)
	return id
}

// 8: an interrupted capture leaves a 'capturing' run that can only be
// abandoned.
func TestInterruptedCaptureCanOnlyBeAbandoned(t *testing.T) {
	f := seedStaging(t)
	ctx := context.Background()
	runID := "00000000-0000-4000-8000-" + hex.EncodeToString([]byte(f.prefix[len(f.prefix)-6:]))
	exec(t, f.pool, `INSERT INTO campaign_legacy_import_runs(id,algorithm,operator,limits,retain_until) VALUES ($1,'legacy-row-json/v2',$2,'{}',clock_timestamp()+interval '1 day')`, runID, testOperator)
	if _, err := f.pool.Exec(ctx, `UPDATE campaign_legacy_import_runs SET status='captured' WHERE id=$1`, runID); sqlState(err) != "23514" {
		t.Fatalf("a run cannot claim a capture it never recorded, got %v", err)
	}
	if err := legacyimport.Fence(ctx, f.pool, runID, []string{f.public}, testOperator); err == nil {
		t.Fatal("a capturing run must not fence")
	}
	if _, err := legacyimport.VerifyFenced(ctx, f.pool, runID, f.public); !errors.Is(err, legacyimport.ErrNotFenced) {
		t.Fatalf("verify: %v", err)
	}
	if err := legacyimport.Abandon(ctx, f.pool, runID, testOperator); err != nil {
		t.Fatal(err)
	}
	if err := legacyimport.Abandon(ctx, f.pool, runID, testOperator); err == nil {
		t.Fatal("abandon is terminal")
	}
}

// waitForAdvisoryWaiter returns once some backend waits for an advisory lock.
func waitForAdvisoryWaiter(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	waitForLockWaiter(t, pool, "advisory")
}

// waitForLockWaiter returns once some backend waits for a lock of the type
// ("advisory" for a campaign lock, "transactionid" for a row another
// transaction holds, "" for any).
func waitForLockWaiter(t *testing.T, pool *pgxpool.Pool, locktype string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var waiting int
		if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM pg_locks WHERE ($1 = '' OR locktype = $1) AND NOT granted`, locktype).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	if locktype == "" {
		locktype = "any"
	}
	t.Fatalf("nothing waited for a lock (type %s)", locktype)
}
