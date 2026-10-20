package outboxrepair

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func waitRepairLock(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var blocked bool
		err := pool.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE 'SELECT source FROM public.durable_outbox%')`).Scan(&blocked)
		if err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("repair did not wait for the event lock")
}
func TestPostgresAuthorityRecheckedAfterRealLockWait(t *testing.T) {
	for _, phase := range []string{"admit", "complete"} {
		t.Run(phase, func(t *testing.T) {
			pool := repairDatabase(t)
			ctx := context.Background()
			p := preparedRepair(t, pool, seedRepair(t, pool, 1, true)[0])
			store := NewPostgres(pool)
			if phase == "complete" {
				if _, err := store.Admit(ctx, p, allowRepair); err != nil {
					t.Fatal(err)
				}
			}
			lock, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer rollback(lock)
			execRepair(t, lock, `SELECT id FROM public.durable_outbox WHERE id=$1 FOR UPDATE`, p.EventID)
			var revoked atomic.Bool
			var called atomic.Int32
			guard := func(context.Context) error {
				called.Add(1)
				if revoked.Load() {
					return ErrUnauthorized
				}
				return nil
			}
			result := make(chan error, 1)
			go func() {
				var err error
				if phase == "admit" {
					_, err = store.Admit(ctx, p, guard)
				} else {
					_, err = store.Complete(ctx, p, guard)
				}
				result <- err
			}()
			waitRepairLock(t, pool)
			if called.Load() != 0 {
				t.Fatal("authority checked before acquiring event lock")
			}
			revoked.Store(true)
			if err = lock.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			if err = <-result; !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("revoked guard: %v", err)
			}
			gen, dead := scalarRepair(t, pool, p.EventID)
			if gen != 0 || dead == nil {
				t.Fatal("revocation changed delivery state")
			}
			want := 0
			if phase == "complete" {
				want = 1
			}
			assertRepairCount(t, pool, "outbox_replay_intents", want)
			assertRepairCount(t, pool, "outbox_replay_outcomes", 0)
		})
	}
}
func TestPostgresAuditFailureRollsBackResetAndRetainsAcceptedIntent(t *testing.T) {
	for _, deferred := range []bool{false, true} {
		t.Run(map[bool]string{false: "insertFailure", true: "commitFailure"}[deferred], func(t *testing.T) {
			pool := repairDatabase(t)
			ctx := context.Background()
			p := preparedRepair(t, pool, seedRepair(t, pool, 1, true)[0])
			store := NewPostgres(pool)
			if _, err := store.Admit(ctx, p, allowRepair); err != nil {
				t.Fatal(err)
			}
			execRepair(t, pool, `CREATE FUNCTION public.repair_fixture_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture secret database failure'; END $$`)
			trigger := `CREATE TRIGGER repair_fixture_failure BEFORE INSERT ON public.outbox_replay_outcomes FOR EACH ROW EXECUTE FUNCTION public.repair_fixture_failure()`
			if deferred {
				trigger = `CREATE CONSTRAINT TRIGGER repair_fixture_failure AFTER INSERT ON public.outbox_replay_outcomes DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.repair_fixture_failure()`
			}
			execRepair(t, pool, trigger)
			_, err := store.Complete(ctx, p, allowRepair)
			want := ErrUnavailable
			if deferred {
				want = ErrOutcomeUnknown
			}
			if err != want {
				t.Fatalf("masked failure: %v want %v", err, want)
			}
			gen, dead := scalarRepair(t, pool, p.EventID)
			if gen != 0 || dead == nil {
				t.Fatal("audit failure committed delivery reset")
			}
			pending, err := store.Query(ctx, p.CommandID, p.Authority.Principal)
			if err != nil || pending.Outcome != nil {
				t.Fatalf("reconciliation: %+v %v", pending, err)
			}
			assertRepairCount(t, pool, "outbox_replay_intents", 1)
			assertRepairCount(t, pool, "outbox_replay_outcomes", 0)
			execRepair(t, pool, `DROP TRIGGER repair_fixture_failure ON public.outbox_replay_outcomes`)
			execRepair(t, pool, `DROP FUNCTION public.repair_fixture_failure()`)
			if _, err = store.Complete(ctx, p, allowRepair); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestPostgresAdmissionRejectsUnavailableAuthorityAndActiveLease(t *testing.T) {
	pool := repairDatabase(t)
	ctx := context.Background()
	p := preparedRepair(t, pool, seedRepair(t, pool, 1, true)[0])
	store := NewPostgres(pool)
	if _, err := store.Admit(ctx, p, func(context.Context) error { return ErrAuthorityUnavailable }); err != ErrAuthorityUnavailable {
		t.Fatal(err)
	}
	execRepair(t, pool, `UPDATE public.durable_outbox SET lease_owner=$2,lease_until=clock_timestamp()+interval '1 minute' WHERE id=$1`, p.EventID, repairID(t))
	if _, err := store.Admit(ctx, p, allowRepair); err != ErrNotReplayable {
		t.Fatalf("active lease admitted: %v", err)
	}
	assertRepairCount(t, pool, "outbox_replay_intents", 0)
	if _, err := NewPostgres(nil).Query(ctx, p.CommandID, p.Authority.Principal); err != ErrUnavailable {
		t.Fatal(err)
	}
}
