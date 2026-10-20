package durableoutbox

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var databaseCounter atomic.Int64

func database(t *testing.T) (*pgxpool.Pool, *Repository) {
	t.Helper()
	dsn, template := os.Getenv("OUTBOX_TEST_ADMIN_URL"), os.Getenv("OUTBOX_TEST_TEMPLATE")
	if dsn == "" || template == "" {
		t.Skip("outbox migrated database evidence unproven: OUTBOX_TEST_ADMIN_URL / _TEMPLATE unset")
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
	name := fmt.Sprintf("outbox_test_%d_%d", os.Getpid(), databaseCounter.Add(1))
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()+" TEMPLATE "+pgx.Identifier{template}.Sanitize()); err != nil {
		admin.Close(ctx)
		t.Fatal(err)
	}
	u.Path = "/" + name
	pool, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, err := admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize())
		if err != nil {
			t.Error(err)
		}
		admin.Close(ctx)
	})
	return pool, NewRepository(pool)
}
func options() Options {
	return Options{Source: "campaign", Batch: 10, Lease: time.Minute, MaxAttempts: 3, RetryBase: time.Second, RetryMax: time.Minute}
}
func seed(t *testing.T, pool *pgxpool.Pool, stream string, count int) []string {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	ids := []string{}
	for version := 1; version <= count; version++ {
		id, err := leaseToken()
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		if version == 1 {
			_, err = tx.Exec(ctx, `INSERT INTO outbox_streams(stream_key,source,last_stream_version) VALUES($1,'campaign',1)`, stream)
		} else {
			_, err = tx.Exec(ctx, `UPDATE outbox_streams SET last_stream_version=last_stream_version+1 WHERE stream_key=$1`, stream)
		}
		if err != nil {
			t.Fatal(err)
		}
		_, err = tx.Exec(ctx, `INSERT INTO durable_outbox(id,source,aggregate_id,aggregate_version,topic,payload_version,payload,occurred_at,available_at)
   VALUES($1::uuid,'campaign',$2,$3,'campaign.publication.changed',1,'{"schemaVersion":1}',clock_timestamp(),clock_timestamp())`, id, stream, version)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return ids
}
func claim(t *testing.T, r *Repository, o Options) []Delivery {
	t.Helper()
	d, err := r.Claim(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	return d
}
func exec(t *testing.T, p *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := p.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}
func scalar(t *testing.T, p *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := p.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestConcurrentClaimsPreserveStreamOrder(t *testing.T) {
	p, r := database(t)
	seed(t, p, "stream-a", 2)
	seed(t, p, "stream-b", 2)
	var wg sync.WaitGroup
	var mu sync.Mutex
	all := []Delivery{}
	errs := []error{}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, err := r.Claim(context.Background(), options())
			mu.Lock()
			defer mu.Unlock()
			all = append(all, d...)
			if err != nil {
				errs = append(errs, err)
			}
		}()
	}
	wg.Wait()
	if len(errs) > 0 || len(all) != 2 {
		t.Fatalf("claims=%d errors=%v", len(all), errs)
	}
	seen := map[string]bool{}
	for _, d := range all {
		if seen[d.ID] || d.Version != 1 {
			t.Fatalf("duplicate or successor claimed: %+v", d)
		}
		seen[d.ID] = true
		if err := r.Ack(context.Background(), d); err != nil {
			t.Fatal(err)
		}
	}
	next := claim(t, r, options())
	if len(next) != 2 {
		t.Fatalf("successors=%d", len(next))
	}
	for _, d := range next {
		if d.Version != 2 {
			t.Fatal("wrong stream version")
		}
	}
}

func TestPoisonBlocksOnlyItsStreamAndRejectsOldPublisher(t *testing.T) {
	p, r := database(t)
	seed(t, p, "poison", 2)
	seed(t, p, "other", 1)
	ctx := context.Background()
	var poison Delivery
	for _, d := range claim(t, r, options()) {
		if d.Stream == "poison" {
			poison = d
			if err := r.Reject(ctx, d, options(), true); err != nil {
				t.Fatal(err)
			}
		} else if err := r.Ack(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	if len(claim(t, r, options())) != 0 {
		t.Fatal("dead letter did not block successor")
	}
	if err := r.Ack(ctx, poison); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("old token ack=%v", err)
	}
}

func TestExpiredFinalAttemptBecomesDeadLetter(t *testing.T) {
	p, r := database(t)
	ids := seed(t, p, "crash", 2)
	o := options()
	o.MaxAttempts = 2
	ctx := context.Background()
	first := claim(t, r, o)[0]
	exec(t, p, `UPDATE durable_outbox SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, ids[0])
	if err := r.Ack(ctx, first); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("expired ack=%v", err)
	}
	second := claim(t, r, o)[0]
	if second.Attempts != 2 || second.Token == first.Token {
		t.Fatal("reclaim did not advance token/attempt")
	}
	if err := r.Reject(ctx, first, o, false); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale reject=%v", err)
	}
	exec(t, p, `UPDATE durable_outbox SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, ids[0])
	if len(claim(t, r, o)) != 0 {
		t.Fatal("exhausted stream was reclaimed")
	}
	if scalar(t, p, `SELECT count(*) FROM durable_outbox WHERE id=$1::uuid AND dead_lettered_at IS NOT NULL AND error_code='leaseExpired'`, ids[0]) != 1 {
		t.Fatal("crash was not observable")
	}
}

func TestConsumerReceiptsAreAtomicOrderedAndIdempotent(t *testing.T) {
	p, r := database(t)
	ids := seed(t, p, "consumer", 2)
	ctx := context.Background()
	exec(t, p, `CREATE TABLE outbox_test_effects(event_id UUID NOT NULL)`)
	supports := func(Event) bool { return true }
	apply := func(ctx context.Context, tx EffectTx, e Event) error {
		_, err := tx.Exec(ctx, `INSERT INTO outbox_test_effects VALUES($1::uuid)`, e.ID)
		return err
	}
	if _, err := r.Consume(ctx, "test", ids[1], supports, apply); !errors.Is(err, ErrSequenceGap) {
		t.Fatalf("gap=%v", err)
	}
	if _, err := r.Consume(ctx, "test", ids[0], func(Event) bool { return false }, apply); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("schema=%v", err)
	}
	failed := errors.New("local effect failed")
	if _, err := r.Consume(ctx, "test", ids[0], supports, func(ctx context.Context, tx EffectTx, e Event) error {
		if err := apply(ctx, tx, e); err != nil {
			return err
		}
		return failed
	}); !errors.Is(err, failed) {
		t.Fatal(err)
	}
	if scalar(t, p, `SELECT count(*) FROM outbox_test_effects`) != 0 || scalar(t, p, `SELECT count(*) FROM consumer_receipts`) != 0 {
		t.Fatal("failed transaction retained effects")
	}
	var applied atomic.Int64
	var wg sync.WaitGroup
	failures := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := r.Consume(ctx, "test", ids[0], supports, apply)
			if err != nil {
				failures <- err
			}
			if ok {
				applied.Add(1)
			}
		}()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	if applied.Load() != 1 || scalar(t, p, `SELECT count(*) FROM outbox_test_effects`) != 1 {
		t.Fatal("duplicate delivery repeated effect")
	}
	if ok, err := r.Consume(ctx, "test", ids[1], supports, apply); err != nil || !ok {
		t.Fatalf("successor: %v %v", ok, err)
	}
	if scalar(t, p, `SELECT count(*) FROM consumer_receipts`) != 2 {
		t.Fatal("missing committed receipt")
	}
}

func TestLongConsumerStreamAndConcurrentAdjacentVersions(t *testing.T) {
	pool, r := database(t)
	ids := seed(t, pool, "long-stream", 128)
	ctx := context.Background()
	supports := func(Event) bool { return true }
	apply := func(context.Context, EffectTx, Event) error { return nil }
	for _, id := range ids[:126] {
		if ok, err := r.Consume(ctx, "long-test", id, supports, apply); err != nil || !ok {
			t.Fatalf("prefix %v %v", ok, err)
		}
	}
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 2)
	go func() {
		_, err := r.Consume(ctx, "long-test", ids[126], supports, func(context.Context, EffectTx, Event) error { close(entered); <-release; return nil })
		done <- err
	}()
	<-entered
	go func() { _, err := r.Consume(ctx, "long-test", ids[127], supports, apply); done <- err }()
	close(release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if scalar(t, pool, `SELECT count(*) FROM consumer_receipts WHERE consumer_id='long-test'`) != 128 {
		t.Fatal("adjacent consumer lost sequence")
	}
}
