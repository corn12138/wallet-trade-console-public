package outboxrepair

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sync/atomic"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/application"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var repairDatabaseCounter atomic.Int64

func repairDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn, template := os.Getenv("OUTBOX_TEST_ADMIN_URL"), os.Getenv("OUTBOX_REPAIR_TEST_TEMPLATE")
	if dsn == "" || template == "" {
		t.Skip("maintenance migrated database proof requires OUTBOX_TEST_ADMIN_URL and OUTBOX_REPAIR_TEST_TEMPLATE")
	}
	u, err := url.Parse(dsn)
	if err != nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") || !regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`).MatchString(template) {
		t.Fatal("loopback admin and plain template required")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("repair_test_%d_%d", os.Getpid(), repairDatabaseCounter.Add(1))
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()+" TEMPLATE "+pgx.Identifier{template}.Sanitize()); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	var pool *pgxpool.Pool
	t.Cleanup(func() {
		if pool != nil {
			pool.Close()
		}
		if _, err := admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
			t.Error(err)
		}
		_ = admin.Close(ctx)
	})
	u.Path = "/" + name
	pool, err = pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	return pool
}
func repairID(t *testing.T) string {
	t.Helper()
	id, err := application.NewID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func seedRepair(t *testing.T, pool *pgxpool.Pool, count int, dead bool) []string {
	return seedRepairAs(t, pool, count, dead, "campaign", "campaign.personal.changed")
}
func seedRepairAs(t *testing.T, pool *pgxpool.Pool, count int, dead bool, source, topic string) []string {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollback(tx)
	stream := "private-wallet-payload:" + repairID(t)
	ids := []string{}
	for n := 1; n <= count; n++ {
		if n == 1 {
			_, err = tx.Exec(ctx, `INSERT INTO public.outbox_streams(stream_key,source,last_stream_version)VALUES($1,$2,1)`, stream, source)
		} else {
			_, err = tx.Exec(ctx, `UPDATE public.outbox_streams SET last_stream_version=last_stream_version+1 WHERE stream_key=$1`, stream)
		}
		if err != nil {
			t.Fatal(err)
		}
		id := repairID(t)
		ids = append(ids, id)
		_, err = tx.Exec(ctx, `INSERT INTO public.durable_outbox(id,source,aggregate_id,aggregate_version,topic,owner_scope,payload_version,payload,occurred_at,available_at) VALUES($1,$4,$2,$3,$5,'0x1111111111111111111111111111111111111111',1,'{"privateState":"must-not-leak"}',clock_timestamp(),clock_timestamp())`, id, stream, n, source, topic)
		if err != nil {
			t.Fatal(err)
		}
	}
	if dead {
		_, err = tx.Exec(ctx, `UPDATE public.durable_outbox SET attempts=3,error_code='publisherUnavailable',dead_lettered_at=clock_timestamp() WHERE id=$1`, ids[0])
		if err != nil {
			t.Fatal(err)
		}
	}
	if tx.Commit(ctx) != nil {
		t.Fatal("seed transaction failed")
	}
	return ids
}
func preparedRepair(t *testing.T, pool *pgxpool.Pool, id string) Prepared {
	t.Helper()
	var expected Expected
	err := pool.QueryRow(context.Background(), `SELECT replay_generation,dead_lettered_at,attempts,error_code FROM public.durable_outbox WHERE id=$1`, id).Scan(&expected.Generation, &expected.DeadLetterAt, &expected.Attempts, &expected.ErrorCode)
	if err != nil {
		t.Fatal(err)
	}
	r := Request{CommandID: repairID(t), EventID: id, Expected: expected, Reason: "retry supported Campaign delivery", RequestID: "integration-1"}
	return prepare(r, Authority{Principal: "maintenance:test", Role: "fixture-replay", Version: 1})
}
func allowRepair(context.Context) error { return nil }
func assertRepairCount(t *testing.T, pool *pgxpool.Pool, table string, want int) {
	t.Helper()
	var count int
	if pool.QueryRow(context.Background(), "SELECT count(*) FROM public."+table).Scan(&count) != nil || count != want {
		t.Fatalf("%s count %d want %d", table, count, want)
	}
}
func scalarRepair(t *testing.T, pool *pgxpool.Pool, id string) (int64, *time.Time) {
	t.Helper()
	var gen int64
	var dead *time.Time
	if err := pool.QueryRow(context.Background(), `SELECT replay_generation,dead_lettered_at FROM public.durable_outbox WHERE id=$1`, id).Scan(&gen, &dead); err != nil {
		t.Fatal(err)
	}
	return gen, dead
}
