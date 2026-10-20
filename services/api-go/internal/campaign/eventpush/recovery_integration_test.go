package eventpush

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth/authtest"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/eventfeed"
	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/application"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/postgres"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/durableoutbox"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/web3session"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	walletA = "0x1111111111111111111111111111111111111111"
	walletB = "0x2222222222222222222222222222222222222222"
)

func database(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn, template := os.Getenv("OUTBOX_TEST_ADMIN_URL"), os.Getenv("OUTBOX_TEST_TEMPLATE")
	if dsn == "" || template == "" {
		t.Skip("campaign push migrated database evidence requires OUTBOX_TEST_ADMIN_URL and OUTBOX_TEST_TEMPLATE")
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
	name := fmt.Sprintf("eventpush_test_%d_%d", os.Getpid(), time.Now().UnixNano()%1e6)
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()+" TEMPLATE "+pgx.Identifier{template}.Sanitize()); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	var pool *pgxpool.Pool
	t.Cleanup(func() {
		if pool != nil {
			pool.Close()
		}
		if _, err := admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
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

type allow struct{}

func (allow) Require(context.Context, string, bool) error { return nil }

func revisionInput() v2.RevisionInput {
	copy := v2.LocalizedCopy{Title: "Push test", Description: "Invalidation", Rules: "Wallet participation", RewardDescription: "Display only"}
	return v2.RevisionInput{SchemaVersion: 1, TermsCanonicalVersion: 1, TemplateID: "campaignCardV1", DisplayContent: v2.DisplayContent{Layout: "compact", SortOrder: 0},
		MaterialTerms: v2.MaterialTerms{Copy: v2.Translations{En: copy, Zh: copy}, StartAt: time.Now().Add(-time.Hour).UTC().Format("2006-01-02T15:04:05Z"),
			EndAt: time.Now().Add(time.Hour).UTC().Format("2006-01-02T15:04:05Z"), Rules: v2.RuleSet{Join: v2.JoinRule{Kind: "walletJoinV1", Version: 1},
				Reminder: v2.ReminderRule{Kind: "reminderIntentV1", Version: 1}}, RewardDelivery: "display-only"}, Cta: v2.CTA{ActionKind: "none"}}
}

func execute(t *testing.T, s *application.Service, owner string, c application.Command) application.Result {
	t.Helper()
	id, err := application.NewID()
	if err != nil {
		t.Fatal(err)
	}
	millis := time.Now().UnixMilli()
	c.ClientCommandID = fmt.Sprintf("%08x-%04x-7%s", millis>>16, millis&0xffff, id[15:])
	c.RequestID = "eventpush-test"
	c.Reason = "Isolated push fixture"
	out, err := s.Execute(context.Background(), owner, c)
	if err != nil {
		t.Fatalf("%s: %v", c.Operation, err)
	}
	return out.Original
}

type deliveryState struct{ published, leased, attempts, dead, receipts int }

func delivery(t *testing.T, pool *pgxpool.Pool) deliveryState {
	t.Helper()
	var d deliveryState
	err := pool.QueryRow(context.Background(), `SELECT count(*) FILTER (WHERE published_at IS NOT NULL), count(*) FILTER (WHERE lease_owner IS NOT NULL),
 coalesce(sum(attempts),0), count(*) FILTER (WHERE dead_lettered_at IS NOT NULL), (SELECT count(*) FROM consumer_receipts) FROM durable_outbox`).Scan(&d.published, &d.leased, &d.attempts, &d.dead, &d.receipts)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func latestEvent(t *testing.T, pool *pgxpool.Pool, campaign, topic string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(), `SELECT id::text FROM durable_outbox WHERE topic=$2 AND aggregate_id LIKE $1 ORDER BY occurred_at DESC, aggregate_version DESC LIMIT 1`,
		`["campaign","`+campaign+`",%`, topic).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func expectInvalidate(t *testing.T, conn *fakeConn, family, head string) sent {
	t.Helper()
	frame := conn.next(t)
	if frame.event != EventInvalidate || frame.payload["family"] != family || frame.payload["headVersion"] != head {
		t.Fatalf("frame %s %v, want %s head %s", frame.event, frame.payload, family, head)
	}
	return frame
}

func listening(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND query LIKE 'LISTEN campaign_outbox_wakeup%'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestMigratedPushRecoveryWithoutPublisherAndAcrossListenerLoss uses genuine
// Campaign command transactions, the real event feed authorization path, the
// persistent session store and the real LISTEN loop on a disposable clone.
func TestMigratedPushRecoveryWithoutPublisherAndAcrossListenerLoss(t *testing.T) {
	pool := database(t)
	ctx := context.Background()
	sessions := web3session.New(pool)
	f := authtest.New(t, func(p *auth.Policy) { p.Sessions = sessions })
	tokenA, sidA := f.Login(t, walletA, 11155111)
	tokenB, _ := f.Login(t, walletB, 11155111)
	commands := application.NewService(postgres.New(pool), allow{}, func() v2.Capabilities { return v2.Capabilities{Templates: map[string]bool{"campaignCardV1": true}} })
	revision := revisionInput()
	draft := execute(t, commands, walletA, application.Command{Operation: "create", Revision: &revision})

	cfg := DefaultConfig()
	cfg.SweepInterval = 300 * time.Millisecond
	hub := New(eventfeed.New(pool, f.Verifier), durableoutbox.NewRepository(pool), cfg)
	defer hub.Close()
	// No Listen yet: this phase has neither a publisher nor a listener.

	anon, anonConn := attach(t, hub, nil)
	mustFail(t, subscribe(t, anon, draft.CampaignID, "publication"), "not_found")
	a, connA := attach(t, hub, map[string]any{"token": tokenA})
	b, connB := attach(t, hub, map[string]any{"token": tokenB})
	mustFail(t, subscribe(t, a, draft.CampaignID, "personal"), "not_found")

	pub := execute(t, commands, walletA, application.Command{Operation: "publish", CampaignID: draft.CampaignID, ExpectedPublication: &draft.PublicationVersion, CandidateRevisionID: draft.RevisionID})
	id := pub.CampaignID
	mustSucceed(t, subscribe(t, anon, id, "interaction"))
	mustSucceed(t, subscribe(t, a, id, "personal"))
	mustSucceed(t, subscribe(t, b, id, "personal"))
	zero := uint64(0)
	execute(t, commands, walletA, application.Command{Operation: "join", CampaignID: id, ExpectedPublication: &pub.PublicationVersion, ExpectedState: &zero})
	// Only the durable sweep can find this mutation.
	expectInvalidate(t, anonConn, "interaction", "1").ack(true)
	expectInvalidate(t, connA, "personal", "1").ack(true)
	connB.none(t, 700*time.Millisecond)

	hub.Close()
	if connA.isClosed() || anonConn.isClosed() {
		t.Fatal("closing the hub disconnected transports; clients would not reconnect")
	}
	if ack, _ := a.Subscribe(ctx, map[string]any{"campaignId": id, "family": "personal"}); ack["code"] != "unavailable" {
		t.Fatalf("subscribe after close: %v", ack)
	}

	// Listener phase on a fresh runtime (process restart): no periodic sweep,
	// so a published wakeup is the only way to learn of the next mutation.
	cfg.SweepInterval = time.Hour
	hub = New(eventfeed.New(pool, f.Verifier), durableoutbox.NewRepository(pool), cfg)
	defer hub.Close()
	hub.Listen(pool)
	waitFor(t, func() bool { return listening(t, pool) == 1 })
	waitFor(t, func() bool { hub.mu.Lock(); defer hub.mu.Unlock(); return !hub.sweepAll })
	anon, anonConn = attach(t, hub, nil)
	a, connA = attach(t, hub, map[string]any{"token": tokenA})
	b, connB = attach(t, hub, map[string]any{"token": tokenB})
	mustSucceed(t, subscribe(t, anon, id, "interaction"))
	mustSucceed(t, subscribe(t, a, id, "personal"))
	mustSucceed(t, subscribe(t, b, id, "personal"))
	execute(t, commands, walletB, application.Command{Operation: "join", CampaignID: id, ExpectedPublication: &pub.PublicationVersion, ExpectedState: &zero})
	connB.none(t, 300*time.Millisecond)
	event := latestEvent(t, pool, id, "campaign.personal.changed")
	if _, err := pool.Exec(ctx, `SELECT pg_notify('campaign_outbox_wakeup', $1)`, `{"schemaVersion":1,"eventId":"`+event+`"}`); err != nil {
		t.Fatal(err)
	}
	expectInvalidate(t, connB, "personal", "1").ack(true)
	expectInvalidate(t, anonConn, "interaction", "2").ack(true)
	// Duplicate and malformed wakeups add nothing.
	for _, payload := range []string{`{"schemaVersion":1,"eventId":"` + event + `"}`, `{"schemaVersion":1,"eventId":"` + event + `"}`, `not json`, `{"schemaVersion":2,"eventId":"` + event + `"}`} {
		if _, err := pool.Exec(ctx, `SELECT pg_notify('campaign_outbox_wakeup', $1)`, payload); err != nil {
			t.Fatal(err)
		}
	}
	connA.none(t, 200*time.Millisecond)
	connB.none(t, 0)

	// Listener loss: the mutation's wakeup is lost, re-registration sweeps.
	var terminated bool
	if err := pool.QueryRow(ctx, `SELECT bool_and(pg_terminate_backend(pid)) FROM pg_stat_activity WHERE datname=current_database() AND query LIKE 'LISTEN campaign_outbox_wakeup%'`).Scan(&terminated); err != nil || !terminated {
		t.Fatalf("terminate listener: %v %v", terminated, err)
	}
	one := uint64(1)
	execute(t, commands, walletA, application.Command{Operation: "leave", CampaignID: id, ExpectedState: &one})
	expectInvalidate(t, connA, "personal", "2").ack(true)
	expectInvalidate(t, anonConn, "interaction", "3").ack(true)
	waitFor(t, func() bool { return listening(t, pool) == 1 })

	// Revocation in the persistent store stops private delivery at the next
	// observation, with no new event required.
	if _, err := sessions.Revoke(ctx, auth.SessionRef{ID: sidA, Wallet: walletA, ChainID: 11155111, Version: 1, Epoch: f.Policy.Epoch, KeyID: f.Policy.Current.ID}, web3session.ReasonLogout, "self"); err != nil {
		t.Fatal(err)
	}
	hub.RequestSweep()
	reset := connA.next(t)
	if reset.event != EventReset || reset.payload["reason"] != "unauthorized" || !connA.isClosed() {
		t.Fatalf("revocation frame %v closed=%v", reset, connA.isClosed())
	}
	if _, code := hub.Authenticate(ctx, map[string]any{"token": tokenA}); code != RejectUnauthorized {
		t.Fatalf("revoked credential reconnect: %q", code)
	}

	// Fixture-only visibility loss (no domain command removes a published
	// revision): public delivery must stop.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err == nil {
		_, err = tx.Exec(ctx, `UPDATE campaign_publications SET active_revision_id=NULL WHERE campaign_id=$1`, id)
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	hub.RequestSweep()
	if frame := anonConn.next(t); frame.event != EventReset || frame.payload["reason"] != "not_found" {
		t.Fatalf("visibility frame %v", frame)
	}

	d := delivery(t, pool)
	if d.receipts != 0 || d.published != 0 || d.leased != 0 || d.attempts != 0 || d.dead != 0 {
		t.Fatalf("serving subscriptions changed delivery state: %+v", d)
	}
	hub.Close()
	// pgxpool's background health check briefly acquires idle connections, so
	// wait for the count to settle rather than sampling it once.
	waitFor(t, func() bool { return pool.Stat().AcquiredConns() == 0 })
	waitFor(t, func() bool { return listening(t, pool) == 0 })
}
