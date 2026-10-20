//go:build integration

package postgres_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/application"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/postgres"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

const admin = "0x1111111111111111111111111111111111111111"
const wallet = "0x2222222222222222222222222222222222222222"

type allowed struct{}

func (allowed) Require(context.Context, string, bool) error { return nil }

func poolFor(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("CAMPAIGN_TEST_DATABASE_URL")
	if url == "" {
		t.Fatal("integration requires explicit CAMPAIGN_TEST_DATABASE_URL for a disposable migrated database")
	}
	pool, err := db.Open(context.Background(), url)
	if err != nil {
		t.Fatal("cannot connect to disposable campaign database")
	}
	t.Cleanup(pool.Close)
	var name string
	if err = pool.QueryRow(context.Background(), `SELECT current_database()`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(name, "campaign_test") && !strings.HasPrefix(name, "migration_probe") {
		t.Fatal("integration database must use campaign_test or migration_probe prefix")
	}
	return pool
}

func caps() v2.Capabilities {
	return v2.Capabilities{Templates: map[string]bool{"campaignCardV1": true}}
}
func service(pool *pgxpool.Pool) *application.Service {
	return application.NewService(postgres.New(pool), allowed{}, caps)
}
func pointer(v uint64) *uint64 { return &v }

func commandID(t *testing.T) string {
	t.Helper()
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatal(err)
	}
	millis := time.Now().UnixMilli()
	for i := 5; i >= 0; i-- {
		raw[i] = byte(millis)
		millis >>= 8
	}
	raw[6] = (raw[6] & 15) | 0x70
	raw[8] = (raw[8] & 63) | 0x80
	value := hex.EncodeToString(raw[:])
	return value[:8] + "-" + value[8:12] + "-" + value[12:16] + "-" + value[16:20] + "-" + value[20:]
}

func input(start, end time.Time) v2.RevisionInput {
	copy := v2.LocalizedCopy{Title: "Campaign integration", Description: "Personal acceptance", Rules: "Wallet participation only", RewardDescription: "Display only"}
	return v2.RevisionInput{SchemaVersion: 1, TermsCanonicalVersion: 1, TemplateID: "campaignCardV1", DisplayContent: v2.DisplayContent{Layout: "compact", SortOrder: 0},
		MaterialTerms: v2.MaterialTerms{Copy: v2.Translations{En: copy, Zh: copy}, StartAt: start.UTC().Format("2006-01-02T15:04:05Z"), EndAt: end.UTC().Format("2006-01-02T15:04:05Z"),
			Rules: v2.RuleSet{Join: v2.JoinRule{Kind: "walletJoinV1", Version: 1}, Reminder: v2.ReminderRule{Kind: "reminderIntentV1", Version: 1}}, RewardDelivery: "display-only"}, Cta: v2.CTA{ActionKind: "none"}}
}

func command(t *testing.T, op, id string) application.Command {
	return application.Command{Operation: op, CampaignID: id, ClientCommandID: commandID(t), Reason: "Integration acceptance", RequestID: "integration"}
}
func execute(t *testing.T, s *application.Service, actor string, c application.Command) application.Outcome {
	t.Helper()
	result, err := s.Execute(context.Background(), actor, c)
	if err != nil {
		t.Fatalf("%s: %v", c.Operation, err)
	}
	return result
}
func draft(t *testing.T, s *application.Service, start, end time.Time) application.Result {
	t.Helper()
	c := command(t, "create", "")
	revision := input(start, end)
	c.Revision = &revision
	return execute(t, s, admin, c).Original
}
func publish(t *testing.T, s *application.Service, r application.Result) application.Result {
	t.Helper()
	c := command(t, "publish", r.CampaignID)
	c.ExpectedPublication = pointer(r.PublicationVersion)
	c.CandidateRevisionID = r.RevisionID
	return execute(t, s, admin, c).Original
}
func interaction(t *testing.T, op, id string, pub, state uint64) application.Command {
	c := command(t, op, id)
	c.ExpectedState = pointer(state)
	if op == "join" || op == "set-reminder" {
		c.ExpectedPublication = pointer(pub)
	}
	return c
}
func expectCode(t *testing.T, err error, code string) {
	t.Helper()
	var domain *v2.DomainError
	if !errors.As(err, &domain) || domain.Code != code {
		t.Fatalf("expected %s; got %v", code, err)
	}
}
func count(t *testing.T, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
