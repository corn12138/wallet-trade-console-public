//go:build integration

package adminclient_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/adminclient"
)

func submitErr(t *testing.T, o *operator, id string) (*adminclient.Intent, error) {
	t.Helper()
	return adminclient.Submit(context.Background(), o.home, o.client, o.cred, id, time.Now, accept)
}

func load(t *testing.T, o *operator, id string) *adminclient.Intent {
	t.Helper()
	intent, err := o.home.LoadIntent(id)
	if err != nil {
		t.Fatal(err)
	}
	return intent
}

func requireUnknown(t *testing.T, o *operator, id, step string) {
	t.Helper()
	intent := load(t, o, id)
	text := adminclient.RenderOutcome(intent)
	if intent.Status != adminclient.StatusUnknown || !intent.Uncertain || !strings.Contains(text, "OUTCOME UNKNOWN") || strings.Contains(text, "refused before execution") {
		t.Fatalf("%s: unknown must survive (status %s): %q", step, intent.Status, text)
	}
}

// A command commits but its reply is lost. Revocation, an invalid session and
// a later publication by another administrator must not turn the earlier
// uncertainty into "not executed"; once access returns, the ledger settles it
// without a second send, and the advanced current state is reported as such.
func TestRecoveryLostReplyThroughRevocationAndExpiredSession(t *testing.T) {
	s := newStack(t)
	a, b := s.operator(t), s.operator(t)
	s.setAdmins(a.signer.address, b.signer.address)
	id, _ := draftAndPublish(t, a, time.Now().Add(time.Hour), time.Now().Add(2*time.Hour))
	pause := a.plan(t, adminclient.PlanInput{Operation: "pause", CampaignID: id})
	s.inject("/api/v2/campaign-admin/campaigns/"+id+"/publications", faultDropResponse)
	if got := a.submit(t, pause.ID, accept); got.Status != adminclient.StatusUnknown {
		t.Fatalf("lost reply must be unknown, got %s", got.Status)
	}
	if commands, audits := s.committedFacts(t, pause.ID); commands != 1 || audits != 1 {
		t.Fatal("fixture: the server must have committed the first attempt")
	}

	s.setAdmins(b.signer.address)
	mark := s.mark()
	if _, err := submitErr(t, a, pause.ID); err == nil {
		t.Fatal("a revoked administrator cannot settle or resend")
	}
	if posts := s.requestsSince(mark, "/publications"); len(posts) != 0 {
		t.Fatalf("nothing may be sent while the ledger is unreadable: %v", posts)
	}
	requireUnknown(t, a, pause.ID, "after revocation")

	s.setAdmins(a.signer.address, b.signer.address)
	a.cred.Token = a.cred.Token[:len(a.cred.Token)-4] + "AAAA"
	a.client, _ = a.client.WithCredential(a.cred)
	if _, err := submitErr(t, a, pause.ID); err == nil {
		t.Fatal("an invalid session cannot settle or resend")
	}
	requireUnknown(t, a, pause.ID, "after invalid session")

	// Another administrator moves the campaign on while A is locked out.
	b.commit(t, adminclient.PlanInput{Operation: "resume", CampaignID: id})
	a.login(t)
	mark = s.mark()
	settled := a.submit(t, pause.ID, accept)
	if settled.Status != adminclient.StatusCommitted || settled.Settlement != "ledger-record" || settled.Result.Original.PublicationVersion != "2" || settled.Result.Current.PublicationVersion != "3" {
		t.Fatalf("ledger must settle the original pause with the advanced current: %+v", settled.Result)
	}
	if order := s.requestsSince(mark, "/"+id); len(order) != 1 || !strings.HasPrefix(order[0], "GET ") || !strings.Contains(order[0], "/commands/") {
		t.Fatalf("settling must be one ledger read and no send: %v", order)
	}
	if commands, audits := s.committedFacts(t, pause.ID); commands != 1 || audits != 1 {
		t.Fatal("settling must not execute the command again")
	}
	if text := adminclient.RenderOutcome(settled); !strings.Contains(text, "changed since this command") {
		t.Fatalf("operator must be told current advanced: %q", text)
	}
}

// The request never reached the API. The ledger read finds nothing, so the
// same bytes are resent once, after the read, and execute exactly once.
func TestRecoveryUndeliveredRequestResendsOnceAfterAbsentRead(t *testing.T) {
	s := newStack(t)
	a := s.operator(t)
	s.setAdmins(a.signer.address)
	id, _ := draftAndPublish(t, a, time.Now().Add(time.Hour), time.Now().Add(2*time.Hour))
	pause := a.plan(t, adminclient.PlanInput{Operation: "pause", CampaignID: id})
	s.inject("/api/v2/campaign-admin/campaigns/"+id+"/publications", faultDropRequest)
	a.submit(t, pause.ID, accept)
	if commands, _ := s.committedFacts(t, pause.ID); commands != 0 {
		t.Fatal("fixture: the request must not have reached the API")
	}
	mark := s.mark()
	done := a.submit(t, pause.ID, accept)
	order := s.requestsSince(mark, "/"+id)
	if done.Status != adminclient.StatusCommitted || done.Result.Replayed || done.Settlement != "executed" || len(order) != 2 || !strings.HasPrefix(order[0], "GET ") || !strings.HasPrefix(order[1], "POST ") {
		t.Fatalf("expected ledger read then one send: %s %v", done.Status, order)
	}
	if commands, audits := s.committedFacts(t, pause.ID); commands != 1 || audits != 1 {
		t.Fatal("exactly one execution")
	}
}

// The request never arrived and another administrator then moved the version.
// The resend's 409 comes after the ledger lookup inside the key's acceptance
// window, so the tool may settle "not executed" — and the ledger agrees.
func TestRecoveryStaleVersionAfterUncertaintyIsProvenNotExecuted(t *testing.T) {
	s := newStack(t)
	a, b := s.operator(t), s.operator(t)
	s.setAdmins(a.signer.address, b.signer.address)
	id, _ := draftAndPublish(t, a, time.Now().Add(time.Hour), time.Now().Add(2*time.Hour))
	pause := a.plan(t, adminclient.PlanInput{Operation: "pause", CampaignID: id})
	s.inject("/api/v2/campaign-admin/campaigns/"+id+"/publications", faultDropRequest)
	a.submit(t, pause.ID, accept)
	b.commit(t, adminclient.PlanInput{Operation: "pause", CampaignID: id})
	done := a.submit(t, pause.ID, accept)
	if done.Status != adminclient.StatusRejected || done.Settlement != "proven-not-executed:CAMPAIGN_VERSION_CONFLICT" || done.Uncertain {
		t.Fatalf("got %s %q", done.Status, done.Settlement)
	}
	if commands, _ := s.committedFacts(t, pause.ID); commands != 0 {
		t.Fatal("the ledger must agree that the key never committed")
	}
	if !strings.Contains(adminclient.RenderOutcome(done), "NOT EXECUTED (proven)") {
		t.Fatal("operator text must state the proof")
	}
}

// A key outside its first-submission window gets 410 before any decision, and
// committed keys may be garbage-collected after that window, so 410 cannot
// prove non-execution: the intent must stay unknown.
func TestRecoveryOutsideWindowStaysUnknown(t *testing.T) {
	s := newStack(t)
	a := s.operator(t)
	s.setAdmins(a.signer.address)
	id, _ := draftAndPublish(t, a, time.Now().Add(time.Hour), time.Now().Add(2*time.Hour))
	old, err := adminclient.Plan(context.Background(), a.home, a.client, a.cred, adminclient.PlanInput{Operation: "pause", CampaignID: id, Reason: "aged key"}, time.Now().Add(-8*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	s.inject("/api/v2/campaign-admin/campaigns/"+id+"/publications", faultDropRequest)
	a.submit(t, old.ID, accept)
	got := a.submit(t, old.ID, accept)
	last := got.Attempts[len(got.Attempts)-1]
	if got.Status != adminclient.StatusUnknown || last.HTTPStatus != 410 || last.Code != "CAMPAIGN_COMMAND_EXPIRED" {
		t.Fatalf("410 must leave the intent unknown: %s %+v", got.Status, last)
	}
	requireUnknown(t, a, old.ID, "after 410")
}

// A failing ledger read (gateway error) must stop before any send, and an
// interrupted send recorded as "sending" must settle from the ledger later.
func TestRecoveryUnreadableLedgerAndInterruptedSend(t *testing.T) {
	s := newStack(t)
	a := s.operator(t)
	s.setAdmins(a.signer.address)
	id, _ := draftAndPublish(t, a, time.Now().Add(time.Hour), time.Now().Add(2*time.Hour))
	pause := a.plan(t, adminclient.PlanInput{Operation: "pause", CampaignID: id})
	s.inject("/api/v2/campaign-admin/campaigns/"+id+"/publications", faultDropResponse)
	a.submit(t, pause.ID, accept)
	s.injectMethod("GET", "/api/v2/campaign-admin/campaigns/"+id+"/commands/", faultDropRequest)
	mark := s.mark()
	if _, err := submitErr(t, a, pause.ID); err == nil || len(s.requestsSince(mark, "/publications")) != 0 {
		t.Fatal("an unreadable ledger must stop before sending")
	}
	requireUnknown(t, a, pause.ID, "after unreadable ledger")

	// Simulate a crash after "sending" was persisted: the on-disk record is
	// left mid-flight and the next process must recover through the ledger.
	interrupted := load(t, a, pause.ID)
	interrupted.Status, interrupted.Uncertain = adminclient.StatusSending, false
	if err := a.home.SaveIntent(interrupted); err != nil {
		t.Fatal(err)
	}
	recovered, err := adminclient.Reconcile(context.Background(), a.home, a.client, a.cred, pause.ID, time.Now)
	if err != nil || recovered.Status != adminclient.StatusCommitted || recovered.Settlement != "ledger-record" {
		t.Fatalf("interrupted send must settle from the ledger: %v %s", err, recovered.Status)
	}
	if commands, audits := s.committedFacts(t, pause.ID); commands != 1 || audits != 1 {
		t.Fatal("exactly one execution")
	}
}

// A real rollback through the tool: publish A, publish a display-only B, roll
// back to A. Rollback is a new monotonic version and keeps history.
func TestRecoveryRollbackSucceedsThroughTheTool(t *testing.T) {
	s := newStack(t)
	a := s.operator(t)
	s.setAdmins(a.signer.address)
	start, end := time.Now().Add(time.Hour), time.Now().Add(2*time.Hour)
	id, first := draftAndPublish(t, a, start, end)
	display := a.commit(t, adminclient.PlanInput{Operation: "revision", CampaignID: id, RevisionJSON: revisionJSON(t, start, end, "")})
	a.commit(t, adminclient.PlanInput{Operation: "publish", CampaignID: id, CandidateRevisionID: *display.Result.Original.RevisionID})
	rollback := a.plan(t, adminclient.PlanInput{Operation: "rollback", CampaignID: id, CandidateRevisionID: first})
	if len(rollback.Review.Warnings) != 0 {
		t.Fatalf("a previously published candidate needs no warning: %v", rollback.Review.Warnings)
	}
	done := a.submit(t, rollback.ID, accept)
	if done.Status != adminclient.StatusCommitted || done.Result.Original.PublicationVersion != "3" || *done.Result.Original.RevisionID != first {
		t.Fatalf("rollback must commit version 3 with the first revision: %+v", done.Result)
	}
	if s.count(t, `SELECT count(*) FROM campaign_publication_history WHERE campaign_id=$1`, id) != 3 ||
		s.count(t, `SELECT count(*) FROM campaign_publications WHERE campaign_id=$1 AND active_revision_id::text=$2 AND publication_version=3`, id, first) != 1 {
		t.Fatal("rollback must append history and point at the first revision")
	}
}
