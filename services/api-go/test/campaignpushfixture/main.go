// Command campaignpushfixture seeds and mutates Campaign state in a disposable
// test database through the genuine application command transactions, for the
// Campaign push process proof. It is test-only — not shipped — and refuses any
// database URL that is not loopback.
//
//	campaignpushfixture seed                      -> {"campaignId","draftId","publicationVersion"}
//	campaignpushfixture join <wallet> <campaign> <publicationVersion> <expectedState>
//	campaignpushfixture leave <wallet> <campaign> <expectedState>
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"time"

	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/application"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

type allow struct{}

func (allow) Require(context.Context, string, bool) error { return nil }

const operator = "0x9999999999999999999999999999999999999999"

func revision() v2.RevisionInput {
	copy := v2.LocalizedCopy{Title: "Push proof campaign", Description: "Local invalidation proof", Rules: "Wallet participation", RewardDescription: "Display only"}
	return v2.RevisionInput{SchemaVersion: 1, TermsCanonicalVersion: 1, TemplateID: "campaignCardV1", DisplayContent: v2.DisplayContent{Layout: "compact", SortOrder: 0},
		MaterialTerms: v2.MaterialTerms{Copy: v2.Translations{En: copy, Zh: copy}, StartAt: time.Now().Add(-time.Hour).UTC().Format("2006-01-02T15:04:05Z"),
			EndAt: time.Now().Add(24 * time.Hour).UTC().Format("2006-01-02T15:04:05Z"), Rules: v2.RuleSet{Join: v2.JoinRule{Kind: "walletJoinV1", Version: 1},
				Reminder: v2.ReminderRule{Kind: "reminderIntentV1", Version: 1}}, RewardDelivery: "display-only"}, Cta: v2.CTA{ActionKind: "none"}}
}

func execute(s *application.Service, owner string, c application.Command) (application.Result, error) {
	id, err := application.NewID()
	if err != nil {
		return application.Result{}, err
	}
	millis := time.Now().UnixMilli()
	c.ClientCommandID = fmt.Sprintf("%08x-%04x-7%s", millis>>16, millis&0xffff, id[15:])
	c.RequestID = "campaign-push-fixture"
	c.Reason = "Local push proof fixture"
	out, err := s.Execute(context.Background(), owner, c)
	return out.Original, err
}

func number(raw string) uint64 {
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		fail(fmt.Errorf("invalid number %q", raw))
	}
	return value
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "campaignpushfixture:", err)
	os.Exit(1)
}

func main() {
	dsn := os.Getenv("DATABASE_URL")
	u, err := url.Parse(dsn)
	if err != nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") || len(os.Args) < 2 {
		fail(fmt.Errorf("loopback DATABASE_URL and a command are required"))
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		fail(err)
	}
	defer pool.Close()
	commands := application.NewService(postgres.New(pool), allow{}, func() v2.Capabilities { return v2.Capabilities{Templates: map[string]bool{"campaignCardV1": true}} })
	switch args := os.Args[1:]; {
	case args[0] == "seed" && len(args) == 1:
		input := revision()
		published, err := execute(commands, operator, application.Command{Operation: "create", Revision: &input})
		if err != nil {
			fail(err)
		}
		pub, err := execute(commands, operator, application.Command{Operation: "publish", CampaignID: published.CampaignID, ExpectedPublication: &published.PublicationVersion, CandidateRevisionID: published.RevisionID})
		if err != nil {
			fail(err)
		}
		draftInput := revision()
		draft, err := execute(commands, operator, application.Command{Operation: "create", Revision: &draftInput})
		if err != nil {
			fail(err)
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"campaignId": pub.CampaignID, "draftId": draft.CampaignID, "publicationVersion": strconv.FormatUint(pub.PublicationVersion, 10)})
	case args[0] == "join" && len(args) == 5:
		publication, state := number(args[3]), number(args[4])
		if _, err := execute(commands, args[1], application.Command{Operation: "join", CampaignID: args[2], ExpectedPublication: &publication, ExpectedState: &state}); err != nil {
			fail(err)
		}
	case args[0] == "leave" && len(args) == 4:
		state := number(args[3])
		if _, err := execute(commands, args[1], application.Command{Operation: "leave", CampaignID: args[2], ExpectedState: &state}); err != nil {
			fail(err)
		}
	default:
		fail(fmt.Errorf("unknown command"))
	}
}
