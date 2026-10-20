package outboxrun

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/durableoutbox"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func fixture(t *testing.T) (*pgxpool.Pool, *pgxpool.Pool, string, string) {
	t.Helper()
	dsn, template := os.Getenv("OUTBOX_TEST_ADMIN_URL"), os.Getenv("OUTBOX_TEST_TEMPLATE")
	if dsn == "" || template == "" {
		t.Skip("runtime database evidence unproven: OUTBOX_TEST_ADMIN_URL / OUTBOX_TEST_TEMPLATE unset")
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
	name := fmt.Sprintf("outboxrun_test_%d_%d", os.Getpid(), time.Now().UnixNano())
	role := name + "_r"
	secretBytes := make([]byte, 24)
	if _, err = rand.Read(secretBytes); err != nil {
		t.Fatal(err)
	}
	secret := hex.EncodeToString(secretBytes)
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()+" TEMPLATE "+pgx.Identifier{template}.Sanitize()); err != nil {
		admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
			t.Error(err)
		}
		if _, err := admin.Exec(ctx, "DROP ROLE IF EXISTS "+pgx.Identifier{role}.Sanitize()); err != nil {
			t.Error(err)
		}
		admin.Close(ctx)
	})
	// Disposable credentials also exercise SCRAM-protected CI clusters. They
	// stay in process memory and never enter evidence or connection diagnostics.
	if _, err = admin.Exec(ctx, "CREATE ROLE "+pgx.Identifier{role}.Sanitize()+" LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD '"+secret+"'"); err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	owner, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(owner.Close)
	for _, sql := range []string{
		"GRANT USAGE ON SCHEMA public TO ",
		"GRANT SELECT ON durable_outbox,outbox_streams TO ",
		"GRANT UPDATE(available_at,published_at,lease_owner,lease_until,attempts,error_code,dead_lettered_at) ON durable_outbox TO ",
	} {
		mustExec(t, owner, sql+pgx.Identifier{role}.Sanitize())
	}
	u.User = url.UserPassword(role, secret)
	restricted, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restricted.Close)
	return owner, restricted, u.String(), role
}

func mustExec(t *testing.T, p *pgxpool.Pool, query string) {
	t.Helper()
	if _, err := p.Exec(context.Background(), query); err != nil {
		t.Fatal(err)
	}
}

func TestDeliveryRoleRejectsPayloadAndOtherDomainWrites(t *testing.T) {
	owner, restricted, _, role := fixture(t)
	ctx := context.Background()
	if err := CheckRole(ctx, owner); !errors.Is(err, ErrRole) {
		t.Fatalf("owner accepted: %v", err)
	}
	if err := CheckRole(ctx, restricted); err != nil {
		t.Fatalf("delivery role refused: %v", err)
	}
	quoted := pgx.Identifier{role}.Sanitize()
	for _, privilege := range []string{"INSERT(id) ON durable_outbox", "REFERENCES(id) ON durable_outbox", "UPDATE(payload) ON durable_outbox", "INSERT ON consumer_receipts", "UPDATE(publication_version) ON campaign_publications"} {
		mustExec(t, owner, "GRANT "+privilege+" TO "+quoted)
		if err := CheckRole(ctx, restricted); !errors.Is(err, ErrRole) {
			t.Fatalf("unsafe grant accepted %s: %v", privilege, err)
		}
		mustExec(t, owner, "REVOKE "+privilege+" FROM "+quoted)
	}
	if err := CheckRole(ctx, restricted); err != nil {
		t.Fatalf("restored role refused: %v", err)
	}
}

func TestRuntimeRoleRefusesReadOnlyAndPrivilegedLogin(t *testing.T) {
	owner, _, dsn, role := fixture(t)
	ctx := context.Background()
	readonly, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	readonly.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	ro, err := pgxpool.NewWithConfig(ctx, readonly)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if err = CheckRole(ctx, ro); !errors.Is(err, ErrRole) {
		t.Fatalf("read-only runtime accepted: %v", err)
	}
	masquerade := owner.Config()
	masquerade.ConnConfig.RuntimeParams["role"] = role
	privileged, err := pgxpool.NewWithConfig(ctx, masquerade)
	if err != nil {
		t.Fatal(err)
	}
	defer privileged.Close()
	if err = CheckRole(ctx, privileged); !errors.Is(err, ErrRole) {
		t.Fatalf("privileged login accepted after SET ROLE: %v", err)
	}
}

func TestDeliveryRoleRejectsInheritedAndSettableWriter(t *testing.T) {
	owner, restricted, _, role := fixture(t)
	ctx := context.Background()
	parent := role + "_parent"
	quoted := pgx.Identifier{parent}.Sanitize()
	mustExec(t, owner, "CREATE ROLE "+quoted+" NOLOGIN")
	t.Cleanup(func() { mustExec(t, owner, "DROP OWNED BY "+quoted); mustExec(t, owner, "DROP ROLE "+quoted) })
	mustExec(t, owner, "GRANT INSERT(id) ON durable_outbox TO "+quoted)
	mustExec(t, owner, "GRANT "+quoted+" TO "+pgx.Identifier{role}.Sanitize())
	if err := CheckRole(ctx, restricted); !errors.Is(err, ErrRole) {
		t.Fatalf("inherited writer accepted: %v", err)
	}
	mustExec(t, owner, "ALTER ROLE "+pgx.Identifier{role}.Sanitize()+" NOINHERIT")
	if err := CheckRole(ctx, restricted); !errors.Is(err, ErrRole) {
		t.Fatalf("SET ROLE writer accepted: %v", err)
	}
}

func TestRestrictedRuntimeRecoversPendingAndClosesOnCancellation(t *testing.T) {
	owner, _, dsn, _ := fixture(t)
	// Producers commit the stream counter and immutable event together. The
	// runtime role cannot insert either row or consume its business receipt.
	mustExec(t, owner, `BEGIN;
 INSERT INTO outbox_streams VALUES('runtime-pending','campaign',1);
 INSERT INTO durable_outbox(id,source,aggregate_id,aggregate_version,topic,payload_version,payload,occurred_at,available_at)
 VALUES('019a0000-0000-7000-8000-000000000001','campaign','runtime-pending',1,'campaign.publication.changed',1,'{}',clock_timestamp(),clock_timestamp()); COMMIT`)
	cfg, err := Load(func(key string) string {
		if key == "CAMPAIGN_OUTBOX_ENABLED" {
			return "true"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var published int
	err = Run(ctx, dsn, cfg, func(p durableoutbox.PassResult) {
		published += p.Published
		if p.Published == 1 {
			cancel()
		}
	})
	if err != nil || published != 1 {
		t.Fatalf("runtime: published=%d err=%v", published, err)
	}
	var attempts, receipts int
	if err := owner.QueryRow(context.Background(), `SELECT attempts,(SELECT count(*) FROM consumer_receipts) FROM durable_outbox WHERE aggregate_id='runtime-pending' AND published_at IS NOT NULL AND lease_owner IS NULL`).Scan(&attempts, &receipts); err != nil || attempts != 1 || receipts != 0 {
		t.Fatalf("delivery changed consumption: attempts=%d receipts=%d err=%v", attempts, receipts, err)
	}
}
