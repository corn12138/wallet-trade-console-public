package outboxrepair

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func execRepair(t *testing.T, db interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(context.Background(), query, args...); err != nil {
		t.Fatal(err)
	}
}
func refusedSQL(t *testing.T, pool *pgxpool.Pool, query string, args ...any) {
	t.Helper()
	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer rollback(tx)
	if _, err = tx.Exec(context.Background(), query, args...); err == nil {
		err = tx.Commit(context.Background())
	}
	var pgerr *pgconn.PgError
	if !errors.As(err, &pgerr) || pgerr.Code != "23514" {
		t.Fatalf("expected integrity refusal, got %v", err)
	}
}
func completedRepair(t *testing.T, pool *pgxpool.Pool) Prepared {
	t.Helper()
	p := preparedRepair(t, pool, seedRepair(t, pool, 1, true)[0])
	store := NewPostgres(pool)
	if _, err := store.Admit(context.Background(), p, allowRepair); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Complete(context.Background(), p, allowRepair); err != nil {
		t.Fatal(err)
	}
	return p
}
func TestPostgresDirectResetFabricationAndHistoricalAuditCannotBypass(t *testing.T) {
	pool := repairDatabase(t)
	id := seedRepair(t, pool, 1, true)[0]
	p := preparedRepair(t, pool, id)
	if _, err := NewPostgres(pool).Admit(context.Background(), p, allowRepair); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`UPDATE public.durable_outbox SET dead_lettered_at=NULL WHERE id=$1`,
		`UPDATE public.durable_outbox SET replay_generation=replay_generation+1 WHERE id=$1`,
		`UPDATE public.durable_outbox SET replay_transaction=pg_current_xact_id()::text WHERE id=$1`,
		`UPDATE public.durable_outbox SET dead_lettered_at=NULL,attempts=0,error_code=NULL,replay_generation=replay_generation+1 WHERE id=$1`,
		`UPDATE public.durable_outbox SET dead_lettered_at=NULL,attempts=1,error_code=NULL,replay_generation=replay_generation+1 WHERE id=$1`,
		`UPDATE public.durable_outbox SET dead_lettered_at=NULL,attempts=0,replay_generation=replay_generation+1 WHERE id=$1`,
	} {
		refusedSQL(t, pool, query, id)
	}
	for _, history := range []string{"1,NULL", "0,pg_current_xact_id()::text"} {
		refusedSQL(t, pool, `INSERT INTO public.durable_outbox(id,source,aggregate_id,aggregate_version,topic,owner_scope,payload_version,payload,occurred_at,available_at,replay_generation,replay_transaction) SELECT $2,source,aggregate_id,aggregate_version,topic,owner_scope,payload_version,payload,occurred_at,available_at,`+history+` FROM public.durable_outbox WHERE id=$1`, id, repairID(t))
	}
	refusedSQL(t, pool, `INSERT INTO public.outbox_replay_outcomes(command_id,event_id,replay_generation,status,code,before_state,after_state,completed_at) VALUES($1,$2,0,'succeeded','replayed','{}','{}',clock_timestamp())`, p.CommandID, id)
	assertRepairCount(t, pool, "outbox_replay_outcomes", 0)
	if _, err := NewPostgres(pool).Complete(context.Background(), p, allowRepair); err != nil {
		t.Fatal(err)
	}
	execRepair(t, pool, `UPDATE public.durable_outbox SET dead_lettered_at=$2,attempts=3,error_code='publisherUnavailable' WHERE id=$1`, id, p.Expected.DeadLetterAt)
	refusedSQL(t, pool, `UPDATE public.durable_outbox SET dead_lettered_at=NULL,attempts=0,error_code=NULL,replay_generation=replay_generation+1 WHERE id=$1`, id)
	gen, dead := scalarRepair(t, pool, id)
	if gen != 1 || dead == nil {
		t.Fatal("historical audit justified a new reset")
	}
}
func TestPostgresAuditRowsCannotBeChangedDeletedOrTruncated(t *testing.T) {
	pool := repairDatabase(t)
	completedRepair(t, pool)
	for _, table := range []string{"outbox_replay_intents", "outbox_replay_outcomes"} {
		refusedSQL(t, pool, "UPDATE public."+table+" SET schema_version=schema_version")
		refusedSQL(t, pool, "DELETE FROM public."+table)
	}
	refusedSQL(t, pool, "TRUNCATE public.outbox_replay_intents CASCADE")
	refusedSQL(t, pool, "TRUNCATE public.outbox_replay_outcomes")
	assertRepairCount(t, pool, "outbox_replay_intents", 1)
	assertRepairCount(t, pool, "outbox_replay_outcomes", 1)
}
func TestPostgresDirectRefusalLocksEventAndUsesDatabaseTime(t *testing.T) {
	pool := repairDatabase(t)
	ctx := context.Background()
	p := preparedRepair(t, pool, seedRepair(t, pool, 1, true)[0])
	if _, err := NewPostgres(pool).Admit(ctx, p, allowRepair); err != nil {
		t.Fatal(err)
	}
	execRepair(t, pool, `UPDATE public.durable_outbox SET attempts=attempts+1 WHERE id=$1`, p.EventID)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollback(tx)
	var completed time.Time
	err = tx.QueryRow(ctx, `INSERT INTO public.outbox_replay_outcomes(command_id,event_id,replay_generation,status,code,before_state,after_state,completed_at,write_transaction) VALUES($1,$2,0,'refused','stateChanged','{"private":"must disappear"}','{}','1970-01-01','forged') RETURNING completed_at`, p.CommandID, p.EventID).Scan(&completed)
	if err != nil || completed.Year() == 1970 {
		t.Fatalf("server stamp: %v %v", completed, err)
	}
	other, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollback(other)
	execRepair(t, other, `SET LOCAL lock_timeout='150ms'`)
	_, err = other.Exec(ctx, `UPDATE public.durable_outbox SET attempts=attempts+1 WHERE id=$1`, p.EventID)
	var pgerr *pgconn.PgError
	if !errors.As(err, &pgerr) || pgerr.Code != "55P03" {
		t.Fatalf("refusal did not retain event lock: %v", err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	result, err := NewPostgres(pool).Query(ctx, p.CommandID, p.Authority.Principal)
	if err != nil || result.Outcome.Before.Attempts != 4 || result.Outcome.After.Attempts != 4 {
		t.Fatalf("closed refusal: %+v %v", result, err)
	}
}
func TestPostgresTwoResetsInOneTransactionCannotFabricateSuccess(t *testing.T) {
	pool := repairDatabase(t)
	ctx := context.Background()
	p := preparedRepair(t, pool, seedRepair(t, pool, 1, true)[0])
	if _, err := NewPostgres(pool).Admit(ctx, p, allowRepair); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollback(tx)
	before, err := readEvent(ctx, tx, p.EventID)
	if err != nil {
		t.Fatal(err)
	}
	execRepair(t, tx, `UPDATE public.durable_outbox SET dead_lettered_at=NULL,attempts=0,error_code=NULL,replay_generation=replay_generation+1 WHERE id=$1`, p.EventID)
	after, err := readEvent(ctx, tx, p.EventID)
	if err != nil {
		t.Fatal(err)
	}
	execRepair(t, tx, `INSERT INTO public.outbox_replay_outcomes(command_id,event_id,replay_generation,status,code,before_state,after_state,completed_at) VALUES($1,$2,1,'succeeded','replayed',$3,$4,clock_timestamp())`, p.CommandID, p.EventID, before.raw, after.raw)
	execRepair(t, tx, `UPDATE public.durable_outbox SET dead_lettered_at=clock_timestamp(),attempts=3,error_code='publisherUnavailable' WHERE id=$1`, p.EventID)
	execRepair(t, tx, `UPDATE public.durable_outbox SET dead_lettered_at=NULL,attempts=0,error_code=NULL,replay_generation=replay_generation+1 WHERE id=$1`, p.EventID)
	if err = tx.Commit(ctx); err == nil {
		t.Fatal("two resets reused one success")
	}
	gen, dead := scalarRepair(t, pool, p.EventID)
	if gen != 0 || dead == nil {
		t.Fatal("invalid multi-reset committed")
	}
	assertRepairCount(t, pool, "outbox_replay_outcomes", 0)
}
