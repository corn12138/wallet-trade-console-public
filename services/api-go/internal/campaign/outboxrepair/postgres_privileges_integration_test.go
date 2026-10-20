package outboxrepair

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func repairRole(t *testing.T, admin *pgxpool.Pool, grants []string) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	name := fmt.Sprintf("repair_role_%d_%d", os.Getpid(), repairDatabaseCounter.Add(1))
	role := pgx.Identifier{name}.Sanitize()
	execRepair(t, admin, "CREATE ROLE "+role+" NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT")
	var pool *pgxpool.Pool
	t.Cleanup(func() {
		if pool != nil {
			pool.Close()
		}
		execRepair(t, admin, "DROP OWNED BY "+role)
		execRepair(t, admin, "DROP ROLE "+role)
	})
	execRepair(t, admin, "GRANT USAGE ON SCHEMA public TO "+role)
	for _, grant := range grants {
		execRepair(t, admin, grant+" TO "+role)
	}
	config := admin.Config().Copy()
	config.AfterConnect = func(ctx context.Context, c *pgx.Conn) error { _, err := c.Exec(ctx, "SET ROLE "+role); return err }
	var err error
	pool, err = pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	return pool
}
func deniedRepair(t *testing.T, pool *pgxpool.Pool, query string, args ...any) {
	t.Helper()
	_, err := pool.Exec(context.Background(), query, args...)
	var pgerr *pgconn.PgError
	if !errors.As(err, &pgerr) || pgerr.Code != "42501" {
		t.Fatalf("expected privilege refusal, got %v", err)
	}
}

var repairDeliverySelect = `GRANT SELECT(id,source,replay_generation,replay_transaction,attempts,dead_lettered_at,error_code,published_at,available_at,lease_owner,lease_until) ON public.durable_outbox`
var repairDeliveryUpdate = `GRANT UPDATE(dead_lettered_at,error_code,attempts,lease_owner,lease_until,replay_generation) ON public.durable_outbox`
var repairSnapshotExecute = `GRANT EXECUTE ON FUNCTION public.outbox_replay_snapshot(BIGINT,INTEGER,TIMESTAMPTZ,TEXT,TIMESTAMPTZ,TIMESTAMPTZ,TIMESTAMPTZ,TIMESTAMPTZ)`

func TestPostgresMetadataInspectorHasNoRawPrivateColumnPrivileges(t *testing.T) {
	admin := repairDatabase(t)
	ctx := context.Background()
	ids := []string{}
	for range 3 {
		ids = append(ids, seedRepair(t, admin, 1, true)[0])
	}
	unknown := seedRepairAs(t, admin, 1, true, "campaign", "private-topic-never-returned")[0]
	execRepair(t, admin, `UPDATE public.durable_outbox SET error_code='private-error-never-returned' WHERE id=$1`, unknown)
	chain := seedRepairAs(t, admin, 1, true, "chain", "chain.event")[0]
	seedRepair(t, admin, 1, false)
	pool := repairRole(t, admin, []string{`GRANT SELECT ON public.campaign_outbox_dead_letters`})
	for _, column := range []string{"payload", "owner_scope", "aggregate_id", "lease_owner"} {
		deniedRepair(t, pool, "SELECT "+column+" FROM public.durable_outbox")
	}
	deniedRepair(t, pool, `SELECT public.outbox_replay_snapshot(0,0,NULL,NULL,NULL,clock_timestamp(),NULL,clock_timestamp())`)
	store := NewPostgres(pool)
	seen := map[string]bool{}
	var position *Position
	for range 3 {
		page, err := store.ListDeadLetters(ctx, position, 2)
		if err != nil || page.ObservedAt.IsZero() || len(page.Entries) > 2 {
			t.Fatalf("page: %+v %v", page, err)
		}
		for _, e := range page.Entries {
			if seen[e.EventID] || e.EventID == chain || len(e.StreamDigest) != 64 {
				t.Fatalf("unsafe inspection entry: %+v", e)
			}
			seen[e.EventID] = true
			if e.EventID == unknown && (e.Topic != "unsupported" || e.ErrorCode != nil) {
				t.Fatal("unknown metadata was disclosed")
			}
		}
		raw, _ := json.Marshal(page)
		for _, secret := range []string{"private-wallet-payload", "private-topic", "private-error", "0x111111", "must-not-leak", "lease_owner", "payload", "owner_scope"} {
			if strings.Contains(string(raw), secret) {
				t.Fatal("inspection disclosed private data")
			}
		}
		position = page.Next
		if position == nil {
			break
		}
	}
	if len(seen) != len(ids)+1 {
		t.Fatalf("bounded pagination omitted entries: %v", seen)
	}
	for _, limit := range []int{0, 51} {
		if _, err := store.ListDeadLetters(ctx, nil, limit); err != ErrInvalidInput {
			t.Fatal(err)
		}
	}
}
func TestPostgresNarrowMaintenanceRoleCanAuditButCannotReadPrivatePayload(t *testing.T) {
	admin := repairDatabase(t)
	p := preparedRepair(t, admin, seedRepair(t, admin, 1, true)[0])
	pool := repairRole(t, admin, []string{repairDeliverySelect, repairDeliveryUpdate, repairSnapshotExecute, `GRANT SELECT,INSERT ON public.outbox_replay_intents,public.outbox_replay_outcomes`, `GRANT SELECT ON public.campaign_outbox_dead_letters`})
	for _, column := range []string{"payload", "owner_scope", "aggregate_id", "topic"} {
		deniedRepair(t, pool, "SELECT "+column+" FROM public.durable_outbox")
	}
	store := NewPostgres(pool)
	ctx := context.Background()
	if _, err := store.Admit(ctx, p, allowRepair); err != nil {
		t.Fatal(err)
	}
	result, err := store.Complete(ctx, p, allowRepair)
	if err != nil || result.Outcome.Status != "succeeded" {
		t.Fatalf("narrow repair: %+v %v", result, err)
	}
	if _, err = store.Query(ctx, p.CommandID, p.Authority.Principal); err != nil {
		t.Fatal(err)
	}
	deniedRepair(t, pool, `UPDATE public.outbox_replay_intents SET reason=reason`)
	deniedRepair(t, pool, `DELETE FROM public.outbox_replay_outcomes`)
	deniedRepair(t, pool, `TRUNCATE public.outbox_replay_outcomes`)
}
func TestPostgresPublisherDeliveryGrantsCannotClearDeadLetters(t *testing.T) {
	admin := repairDatabase(t)
	id := seedRepair(t, admin, 1, true)[0]
	pool := repairRole(t, admin, []string{repairDeliverySelect, `GRANT UPDATE(dead_lettered_at,error_code,attempts,lease_owner,lease_until,available_at,published_at) ON public.durable_outbox`})
	refusedSQL(t, pool, `UPDATE public.durable_outbox SET dead_lettered_at=NULL,attempts=0,error_code=NULL,lease_owner=NULL,lease_until=NULL WHERE id=$1`, id)
	deniedRepair(t, pool, `UPDATE public.durable_outbox SET replay_generation=replay_generation+1 WHERE id=$1`, id)
	deniedRepair(t, pool, `INSERT INTO public.outbox_replay_outcomes(command_id,event_id,replay_generation,status,code,before_state,after_state,completed_at) VALUES($1,$2,0,'refused','stateChanged','{}','{}',clock_timestamp())`, repairID(t), id)
	gen, dead := scalarRepair(t, admin, id)
	if gen != 0 || dead == nil {
		t.Fatal("publisher bypassed audited reset")
	}
}
