package eventfeed

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth/authtest"
	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/application"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/postgres"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/durableoutbox"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/web3session"
)

type fixtureAuthorization struct{}

func (fixtureAuthorization) Require(context.Context, string, bool) error { return nil }
func seedRevision() v2.RevisionInput {
	copy := v2.LocalizedCopy{Title: "Recovery test", Description: "Read repair", Rules: "Wallet participation", RewardDescription: "Display only"}
	return v2.RevisionInput{SchemaVersion: 1, TermsCanonicalVersion: 1, TemplateID: "campaignCardV1", DisplayContent: v2.DisplayContent{Layout: "compact", SortOrder: 0}, MaterialTerms: v2.MaterialTerms{Copy: v2.Translations{En: copy, Zh: copy}, StartAt: time.Now().Add(-time.Hour).UTC().Format("2006-01-02T15:04:05Z"), EndAt: time.Now().Add(time.Hour).UTC().Format("2006-01-02T15:04:05Z"), Rules: v2.RuleSet{Join: v2.JoinRule{Kind: "walletJoinV1", Version: 1}, Reminder: v2.ReminderRule{Kind: "reminderIntentV1", Version: 1}}, RewardDelivery: "display-only"}, Cta: v2.CTA{ActionKind: "none"}}
}
func execute(t *testing.T, s *application.Service, owner string, c application.Command) application.Result {
	t.Helper()
	id, err := application.NewID()
	if err != nil {
		t.Fatal(err)
	}
	millis := time.Now().UnixMilli()
	c.ClientCommandID = fmt.Sprintf("%08x-%04x-7%s", millis>>16, millis&0xffff, id[15:])
	c.RequestID = "eventfeed-test"
	c.Reason = "Isolated recovery fixture"
	out, err := s.Execute(context.Background(), owner, c)
	if err != nil {
		t.Fatalf("%s: %v", c.Operation, err)
	}
	return out.Original
}
func TestMigratedRecoveryIsolationAndPersistentRevocation(t *testing.T) {
	pool := database(t)
	ctx := context.Background()
	sessions := web3session.New(pool)
	f := authtest.New(t, func(p *auth.Policy) { p.Sessions = sessions })
	token, _ := f.Login(t, walletA, 11155111)
	other, _ := f.Login(t, walletB, 11155111)
	commands := application.NewService(postgres.New(pool), fixtureAuthorization{}, func() v2.Capabilities { return v2.Capabilities{Templates: map[string]bool{"campaignCardV1": true}} })
	revision := seedRevision()
	draft := execute(t, commands, walletA, application.Command{Operation: "create", Revision: &revision})
	s := New(pool, f.Verifier)
	_, err := s.Read(ctx, draft.CampaignID, "publication", "", 20, false)
	expectError(t, err, "CAMPAIGN_NOT_FOUND")
	withToken(t, f.Verifier, token, func(ctx context.Context) {
		_, err := s.Read(ctx, draft.CampaignID, "personal", "", 20, true)
		expectError(t, err, "CAMPAIGN_NOT_FOUND")
	})
	pub := execute(t, commands, walletA, application.Command{Operation: "publish", CampaignID: draft.CampaignID, ExpectedPublication: &draft.PublicationVersion, CandidateRevisionID: draft.RevisionID})
	zero := uint64(0)
	cursorA, cursorB, publicCursor := "", "", ""
	withToken(t, f.Verifier, token, func(ctx context.Context) {
		out, err := s.Read(ctx, pub.CampaignID, "personal", "", 20, true)
		if err != nil || out.HeadVersion != "0" {
			t.Fatalf("anchor A: %+v %v", out, err)
		}
		cursorA = out.Cursor
	})
	withToken(t, f.Verifier, other, func(ctx context.Context) {
		out, err := s.Read(ctx, pub.CampaignID, "personal", "", 20, true)
		if err != nil {
			t.Fatal(err)
		}
		cursorB = out.Cursor
	})
	out, err := s.Read(ctx, pub.CampaignID, "interaction", "", 20, false)
	if err != nil {
		t.Fatal(err)
	}
	publicCursor = out.Cursor
	execute(t, commands, walletA, application.Command{Operation: "join", CampaignID: pub.CampaignID, ExpectedPublication: &pub.PublicationVersion, ExpectedState: &zero})
	withToken(t, f.Verifier, token, func(ctx context.Context) {
		out, err := s.Read(ctx, pub.CampaignID, "personal", cursorA, 20, true)
		if err != nil || out.ResetRequired || len(out.Events) != 1 || out.Events[0].StreamVersion != "1" {
			t.Fatalf("private catchup: %+v %v", out, err)
		}
		raw, _ := json.Marshal(out)
		if strings.Contains(string(raw), walletA) || strings.Contains(string(raw), "acceptedTerms") || strings.Contains(string(raw), "state") {
			t.Fatal("private payload disclosed")
		}
	})
	withToken(t, f.Verifier, other, func(ctx context.Context) {
		out, err := s.Read(ctx, pub.CampaignID, "personal", cursorB, 20, true)
		if err != nil || out.HeadVersion != "0" || len(out.Events) != 0 {
			t.Fatalf("cross-owner events: %+v %v", out, err)
		}
	})
	out, err = s.Read(ctx, pub.CampaignID, "interaction", publicCursor, 20, false)
	if err != nil || out.ResetRequired || len(out.Events) != 1 {
		t.Fatalf("public catchup: %+v %v", out, err)
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), walletA) || strings.Contains(string(raw), "state") {
		t.Fatal("public payload disclosed")
	}
	// Revoke the actual persisted session after the real repeatable-read query
	// returns but before the service's disclosure check, not merely on next login.
	original := s.reader
	s.reader = readFunc(func(ctx context.Context, stream string, after *int64, limit int) (durableoutbox.StreamPage, error) {
		page, err := original.ReadStream(ctx, stream, after, limit)
		if err != nil {
			return page, err
		}
		claims, err := auth.RevalidateStrict(ctx, f.Verifier)
		if err != nil {
			return page, err
		}
		_, err = sessions.Revoke(ctx, claims.Session(), web3session.ReasonLogout, "self")
		return page, err
	})
	withToken(t, f.Verifier, token, func(ctx context.Context) {
		out, err := s.Read(ctx, pub.CampaignID, "personal", cursorA, 20, true)
		expectError(t, err, "UNAUTHORIZED")
		if out.Cursor != "" {
			t.Fatal("revoked request returned private cursor")
		}
	})
	var receipts int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM consumer_receipts`).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatalf("HTTP read wrote receipts: %d %v", receipts, err)
	}
}
