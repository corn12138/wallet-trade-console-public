//go:build integration

package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/application"
)

func TestQueryMeKeepsAbsentTombstoneAndOwnerIsolated(t *testing.T) {
	pool := poolFor(t)
	write, read := service(pool), reader(pool)
	ctx := context.Background()
	now := time.Now()
	r := publish(t, write, draft(t, write, now.Add(-time.Hour), now.Add(time.Hour)))
	value, err := read.Me(ctx, wallet, []string{r.CampaignID})
	if err != nil {
		t.Fatal(err)
	}
	state := value.Items[0].Participation
	if state.State != "absent" || state.StateVersion == nil || *state.StateVersion != "0" || state.Status != nil || state.ChangedAt != nil {
		t.Fatal("absence must be proven separately")
	}
	execute(t, write, wallet, interaction(t, "leave", r.CampaignID, r.PublicationVersion, 0))
	value, err = read.Me(ctx, wallet, []string{r.CampaignID})
	if err != nil {
		t.Fatal(err)
	}
	state = value.Items[0].Participation
	if state.State != "known" || *state.StateVersion != "1" || *state.Status != "left" || *state.AcceptanceProvenance != "neverAccepted" || state.AcceptedAt != nil || state.ChangedAt == nil || value.Items[0].Reminder.State != "absent" {
		t.Fatal("tombstone or reminder namespace lost")
	}
	other, err := read.Me(ctx, admin, []string{r.CampaignID})
	if err != nil || other.Items[0].Participation.State != "absent" {
		t.Fatal("owner state leaked")
	}
	hidden := draft(t, write, now.Add(time.Hour), now.Add(2*time.Hour))
	_, err = read.Me(ctx, wallet, []string{hidden.CampaignID})
	expectCode(t, err, "CAMPAIGN_NOT_FOUND")
	_, err = read.Me(ctx, wallet, []string{"missing-" + commandID(t)})
	expectCode(t, err, "CAMPAIGN_NOT_FOUND")
}
func TestQueryHistoryRetainsOriginalTermsAfterDisplayPublicationAndArchive(t *testing.T) {
	pool := poolFor(t)
	write, read := service(pool), reader(pool)
	ctx := context.Background()
	now := time.Now()
	r := publish(t, write, draft(t, write, now.Add(-time.Hour), now.Add(time.Hour)))
	execute(t, write, wallet, interaction(t, "join", r.CampaignID, r.PublicationVersion, 0))
	preview, err := read.Preview(ctx, admin, r.CampaignID, r.RevisionID)
	if err != nil {
		t.Fatal(err)
	}
	input := *preview.Item.Content
	input.DisplayContent.Layout = "featured"
	c := command(t, "revision", r.CampaignID)
	c.ExpectedPublication = pointer(r.PublicationVersion)
	c.Revision = &input
	candidate := execute(t, write, admin, c).Original
	published := publish(t, write, candidate)
	execute(t, write, wallet, interaction(t, "leave", r.CampaignID, published.PublicationVersion, 1))
	c = command(t, "archive", r.CampaignID)
	c.ExpectedPublication = pointer(published.PublicationVersion)
	execute(t, write, admin, c)
	cursor := ""
	seen := map[string]bool{}
	found := 0
	for page := 0; page < 200; page++ {
		response, err := read.History(ctx, wallet, application.HistoryQuery{Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range response.Items {
			key := entry.CampaignID + ":" + entry.Kind + ":" + *entry.State.StateVersion
			if seen[key] {
				t.Fatal("duplicate history cursor row")
			}
			seen[key] = true
			if entry.CampaignID == r.CampaignID {
				found++
				if entry.SnapshotState != "ready" || entry.AcceptedTerms == nil || entry.State.AcceptedRevisionID == nil || *entry.State.AcceptedRevisionID != r.RevisionID || entry.State.AcceptedPublicationVersion == nil || *entry.State.AcceptedPublicationVersion != "1" || entry.AcceptedTerms.Copy.En.Title != input.MaterialTerms.Copy.En.Title || entry.State.ChangedAt == nil || *entry.State.ChangedAt != entry.OccurredAt {
					t.Fatal("history substituted current revision or time")
				}
			}
		}
		if response.NextCursor == nil {
			break
		}
		cursor = *response.NextCursor
	}
	if found != 2 {
		t.Fatalf("archived participation history missing: %d", found)
	}
	value, err := read.Me(ctx, wallet, []string{r.CampaignID})
	if err != nil || *value.Items[0].Participation.Status != "left" {
		t.Fatal("archive hid personal state")
	}
	if cursor != "" {
		_, err = read.History(ctx, admin, application.HistoryQuery{Cursor: cursor})
		expectCode(t, err, "CAMPAIGN_INVALID_INPUT")
	}
}
func TestQueryCommandReturnsImmutableOriginalAndCurrentWithoutReexecution(t *testing.T) {
	pool := poolFor(t)
	write, read := service(pool), reader(pool)
	ctx := context.Background()
	now := time.Now()
	r := publish(t, write, draft(t, write, now.Add(-time.Hour), now.Add(time.Hour)))
	join := interaction(t, "join", r.CampaignID, r.PublicationVersion, 0)
	execute(t, write, wallet, join)
	execute(t, write, wallet, interaction(t, "leave", r.CampaignID, r.PublicationVersion, 1))
	before := count(t, pool, `SELECT count(*) FROM campaign_commands WHERE target_id=$1`, r.CampaignID)
	value, err := read.Command(ctx, wallet, r.CampaignID, "join", join.ClientCommandID)
	if err != nil {
		t.Fatal(err)
	}
	if !value.Replayed || value.Original.State.Status != "joined" || value.Original.State.StateVersion != "1" || value.Current.State.Status != "left" || value.Current.State.StateVersion != "2" || value.ServerNow != value.AsOf {
		t.Fatal("ledger retry replaced original or current")
	}
	if count(t, pool, `SELECT count(*) FROM campaign_commands WHERE target_id=$1`, r.CampaignID) != before {
		t.Fatal("GET reexecuted command")
	}
	_, err = read.Command(ctx, admin, r.CampaignID, "join", join.ClientCommandID)
	expectCode(t, err, "CAMPAIGN_NOT_FOUND")
	_, err = read.Command(ctx, wallet, r.CampaignID, "join", "00000000-0000-7000-8000-000000000001")
	expectCode(t, err, "CAMPAIGN_NOT_FOUND")
}
func TestQueryCollectionCommandRetainsDraftIdentity(t *testing.T) {
	pool := poolFor(t)
	write, read := service(pool), reader(pool)
	ctx := context.Background()
	c := command(t, "create", "")
	value := input(time.Now().Add(time.Hour), time.Now().Add(2*time.Hour))
	c.Revision = &value
	created := execute(t, write, admin, c).Original
	result, err := read.Command(ctx, admin, "campaigns", "create", c.ClientCommandID)
	if err != nil || result.Original.CampaignID != created.CampaignID || result.Original.RevisionID != created.RevisionID || result.Current.RevisionID != "" || result.Current.PublicationVersion != 0 {
		t.Fatalf("creation ledger target invalid: %v", err)
	}
}
