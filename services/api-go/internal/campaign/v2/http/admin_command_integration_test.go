//go:build integration

package campaignhttp_test

import (
	"encoding/json"
	"testing"

	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/application"
)

// Administrator results must be provably the caller's own ledger record: the
// write response and the later command query report the same stored key,
// actor, operation, target and payload hash, and that hash is exactly what the
// shared body-to-command mapping produces for the request body.
func TestHTTPAdminCommandResponsesCarryLedgerIdentity(t *testing.T) {
	f := newRealHTTP(t)
	body := v2.CreateCommandInput{ClientCommandID: httpID(t), Reason: "identity fixture", Revision: httpInput()}
	created := f.adminCommand(t, "POST", "/campaign-admin/campaigns", body, 201)
	want, err := application.PayloadHash(application.CreateCommand(body))
	if err != nil {
		t.Fatal(err)
	}
	id := created.Command
	if id.ClientCommandID != body.ClientCommandID || id.Operation != "create" || id.TargetID != "campaigns" || id.PayloadHash != want || created.Replayed {
		t.Fatalf("create identity: %+v", id)
	}
	replay := f.adminCommand(t, "POST", "/campaign-admin/campaigns", body, 200)
	if !replay.Replayed || replay.Command != id || !sameJSON(t, replay.Original, created.Original) {
		t.Fatal("a same-key create must replay the same ledger identity")
	}
	lookup := f.adminCommand(t, "GET", "/campaign-admin/commands/"+body.ClientCommandID+"?operation=create", nil, 200)
	if !lookup.Replayed || lookup.Command != id {
		t.Fatal("the ledger query must return the same identity")
	}

	candidate := *created.Original.RevisionID
	publish := v2.PublicationCommandInput{ClientCommandID: httpID(t), Reason: "identity publish", Operation: "publish", ExpectedPublicationVersion: "0", CandidateRevisionID: &candidate}
	path := "/campaign-admin/campaigns/" + created.Original.CampaignID
	published := f.adminCommand(t, "POST", path+"/publications", publish, 200)
	command, err := application.PublicationCommand(created.Original.CampaignID, publish)
	if err != nil {
		t.Fatal(err)
	}
	if want, _ = application.PayloadHash(command); published.Command.PayloadHash != want || published.Command.TargetID != created.Original.CampaignID {
		t.Fatalf("publish identity: %+v", published.Command)
	}
	lookup = f.adminCommand(t, "GET", path+"/commands/"+publish.ClientCommandID+"?operation=publish", nil, 200)
	if lookup.Command != published.Command || *lookup.Original.RevisionID != candidate {
		t.Fatal("publication lookup identity or candidate mismatch")
	}

	// Participant routes keep the deployed CommandResponse shape.
	join := v2.PositiveInteractionInput{ClientCommandID: httpID(t), ExpectedStateVersion: "0", ExpectedPublicationVersion: "1"}
	f.command(t, "POST", "/campaigns/"+created.Original.CampaignID+"/join", f.owner, join, 200)
}

func sameJSON(t *testing.T, a, b any) bool {
	t.Helper()
	x, err1 := json.Marshal(a)
	y, err2 := json.Marshal(b)
	if err1 != nil || err2 != nil {
		t.Fatal("marshal")
	}
	return string(x) == string(y)
}
