//go:build integration

package adminclient_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/adminclient"
)

func draftAndPublish(t *testing.T, o *operator, start, end time.Time) (string, string) {
	created := o.commit(t, adminclient.PlanInput{Operation: "create", RevisionJSON: revisionJSON(t, start, end, "")})
	id, revision := created.Result.Original.CampaignID, *created.Result.Original.RevisionID
	o.commit(t, adminclient.PlanInput{Operation: "publish", CampaignID: id, CandidateRevisionID: revision})
	return id, revision
}

func TestToolLifecycleWithRealSIWEAndLedger(t *testing.T) {
	s := newStack(t)
	admin := s.operator(t)
	s.setAdmins(admin.signer.address)
	start, end := time.Now().Add(time.Hour), time.Now().Add(2*time.Hour)
	planned := admin.plan(t, adminclient.PlanInput{Operation: "create", Slug: "tool-" + strings.ToLower(admin.signer.address[2:10]), RevisionJSON: revisionJSON(t, start, end, "")})
	if planned.Status != adminclient.StatusPlanned || len(planned.Attempts) != 0 || planned.Review.Candidate.HashSource == "server preview" {
		t.Fatal("planning must persist an unsent intent with a locally marked hash")
	}
	if s.count(t, `SELECT count(*) FROM campaign_commands WHERE client_command_id=$1`, planned.ID) != 0 {
		t.Fatal("planning reached the server ledger")
	}
	created := admin.submit(t, planned.ID, accept)
	if created.Status != adminclient.StatusCommitted || created.Result.Replayed || created.Attempts[0].HTTPStatus != 201 {
		t.Fatal("create not executed exactly once")
	}
	id, revision := created.Result.Original.CampaignID, *created.Result.Original.RevisionID
	publish := admin.plan(t, adminclient.PlanInput{Operation: "publish", CampaignID: id, CandidateRevisionID: revision})
	if publish.Review.Candidate.HashSource != "server preview" || *publish.Review.ExpectedPublicationVersion != "0" || !strings.Contains(strings.Join(publish.Review.Impact, ";"), "0 -> 1") {
		t.Fatalf("publish review incomplete: %+v", publish.Review)
	}
	// The local precompute must equal the server's authoritative hash.
	if publish.Review.Candidate.MaterialTermsHash != planned.Review.Candidate.MaterialTermsHash {
		t.Fatal("local terms hash disagrees with the server")
	}
	if done := admin.submit(t, publish.ID, accept); done.Status != adminclient.StatusCommitted || done.Result.Current.PublicationVersion != "1" {
		t.Fatal("publish did not commit at version 1")
	}
	if _, err := adminclient.Submit(context.Background(), admin.home, admin.client, admin.cred, publish.ID, time.Now, accept); err == nil {
		t.Fatal("a committed intent cannot be sent again")
	}
	if again, err := adminclient.Reconcile(context.Background(), admin.home, admin.client, admin.cred, publish.ID, time.Now); err != nil || again.Status != adminclient.StatusCommitted || !again.Result.Replayed {
		t.Fatal("reconcile must read the original ledger record")
	}
	declined := admin.plan(t, adminclient.PlanInput{Operation: "pause", CampaignID: id})
	if _, err := adminclient.Submit(context.Background(), admin.home, admin.client, admin.cred, declined.ID, time.Now, func(*adminclient.Intent, *adminclient.CurrentState) error { return errors.New("declined") }); err == nil {
		t.Fatal("declined confirmation must not send")
	}
	if s.count(t, `SELECT count(*) FROM campaign_commands WHERE client_command_id=$1`, declined.ID) != 0 {
		t.Fatal("declined intent reached the server")
	}
	if s.count(t, `SELECT count(*) FROM campaign_audit_events WHERE campaign_id=$1`, id) != 2 {
		t.Fatal("every committed administrator command needs one audit row")
	}
}

// Two administrators reviewed the same version. B confirms only after A has
// committed, so B's request reaches the locked CAS with a stale expectation.
func TestToolTwoAdministratorsCASOnlyOneWins(t *testing.T) {
	s := newStack(t)
	a, b := s.operator(t), s.operator(t)
	s.setAdmins(a.signer.address, b.signer.address)
	id, _ := draftAndPublish(t, a, time.Now().Add(time.Hour), time.Now().Add(2*time.Hour))
	ra := a.commit(t, adminclient.PlanInput{Operation: "revision", CampaignID: id, RevisionJSON: revisionJSON(t, time.Now().Add(time.Hour), time.Now().Add(3*time.Hour), "A")})
	rb := b.commit(t, adminclient.PlanInput{Operation: "revision", CampaignID: id, RevisionJSON: revisionJSON(t, time.Now().Add(time.Hour), time.Now().Add(4*time.Hour), "B")})
	pa := a.plan(t, adminclient.PlanInput{Operation: "publish", CampaignID: id, CandidateRevisionID: *ra.Result.Original.RevisionID})
	pb := b.plan(t, adminclient.PlanInput{Operation: "publish", CampaignID: id, CandidateRevisionID: *rb.Result.Original.RevisionID})
	result := b.submit(t, pb.ID, func(*adminclient.Intent, *adminclient.CurrentState) error {
		if won := a.submit(t, pa.ID, accept); won.Status != adminclient.StatusCommitted {
			t.Fatal("first administrator must win")
		}
		return nil
	})
	if result.Status != adminclient.StatusRejected || result.Retryable || result.Attempts[0].Code != "CAMPAIGN_VERSION_CONFLICT" {
		t.Fatalf("second administrator must get a final 409: %s", adminclient.RenderOutcome(result))
	}
	if s.count(t, `SELECT count(*) FROM campaign_publication_history WHERE campaign_id=$1 AND publication_version=2`, id) != 1 ||
		s.count(t, `SELECT count(*) FROM campaign_commands WHERE client_command_id=$1`, pb.ID) != 0 {
		t.Fatal("lost update or rejected command persisted")
	}
	if _, err := adminclient.Submit(context.Background(), b.home, b.client, b.cred, pb.ID, time.Now, accept); err == nil {
		t.Fatal("a CAS-rejected intent must be re-planned, not re-sent")
	}
}

func TestToolUnknownOutcomesSettleWithoutDuplicates(t *testing.T) {
	s := newStack(t)
	admin := s.operator(t)
	s.setAdmins(admin.signer.address)
	id, _ := draftAndPublish(t, admin, time.Now().Add(time.Hour), time.Now().Add(2*time.Hour))
	publications := "/api/v2/campaign-admin/campaigns/" + id + "/publications"

	// Committed but answered with a gateway page: reconcile finds the ledger.
	s.inject(publications, faultDropResponse)
	pause := admin.submit(t, admin.plan(t, adminclient.PlanInput{Operation: "pause", CampaignID: id}).ID, accept)
	if pause.Status != adminclient.StatusUnknown {
		t.Fatal("gateway page after commit must be unknown")
	}
	if settled, err := adminclient.Reconcile(context.Background(), admin.home, admin.client, admin.cred, pause.ID, time.Now); err != nil || settled.Status != adminclient.StatusCommitted || settled.Result.Original.PublicationVersion != "2" {
		t.Fatal("reconcile must settle a committed unknown")
	}

	// Committed, connection cut mid-body: same-key resubmit replays.
	s.inject(publications, faultResetAfter)
	resume := admin.submit(t, admin.plan(t, adminclient.PlanInput{Operation: "resume", CampaignID: id}).ID, accept)
	if resume.Status != adminclient.StatusUnknown {
		t.Fatal("cut connection must be unknown")
	}
	resume = admin.submit(t, resume.ID, accept)
	if resume.Status != adminclient.StatusCommitted || !resume.Result.Replayed || s.count(t, `SELECT count(*) FROM campaign_commands WHERE client_command_id=$1`, resume.ID) != 1 {
		t.Fatal("resubmit must replay, not execute twice")
	}

	// Never delivered: 404 on reconcile is not proof; resubmit executes once.
	s.inject(publications, faultDropRequest)
	archive := admin.submit(t, admin.plan(t, adminclient.PlanInput{Operation: "archive", CampaignID: id}).ID, accept)
	archive, err := adminclient.Reconcile(context.Background(), admin.home, admin.client, admin.cred, archive.ID, time.Now)
	if err != nil || archive.Status != adminclient.StatusUnknown || !strings.Contains(archive.Attempts[len(archive.Attempts)-1].Note, "not proof") {
		t.Fatal("ledger 404 must keep the intent unknown")
	}
	if _, err = adminclient.Abandon(admin.home, archive.ID, time.Now()); err == nil {
		t.Fatal("an unknown intent cannot be abandoned")
	}
	archive = admin.submit(t, archive.ID, accept)
	if archive.Status != adminclient.StatusCommitted || archive.Result.Replayed || archive.Result.Original.PublicationVersion != "4" {
		t.Fatal("undelivered intent must execute exactly once on resubmit")
	}
}

func TestToolRevokedAndExpiredSessionsAreRetryableWithSameKey(t *testing.T) {
	s := newStack(t)
	admin, owner := s.operator(t), s.operator(t)
	s.setAdmins(admin.signer.address)
	id, _ := draftAndPublish(t, admin, time.Now().Add(time.Hour), time.Now().Add(2*time.Hour))
	if _, err := adminclient.Plan(context.Background(), owner.home, owner.client, owner.cred, adminclient.PlanInput{Operation: "pause", CampaignID: id, Reason: "not admin"}, time.Now()); err == nil || !strings.Contains(err.Error(), "CAMPAIGN_ADMIN_REQUIRED") {
		t.Fatal("an ordinary SIWE owner must not read administrator state")
	}
	pause := admin.plan(t, adminclient.PlanInput{Operation: "pause", CampaignID: id})
	revoke := func(*adminclient.Intent, *adminclient.CurrentState) error { s.setAdmins(); return nil }
	if denied := admin.submit(t, pause.ID, revoke); denied.Status != adminclient.StatusRejected || !denied.Retryable || denied.Attempts[0].Code != "CAMPAIGN_ADMIN_REQUIRED" {
		t.Fatal("revocation between review and send must be a retryable refusal")
	}
	s.setAdmins(admin.signer.address)
	admin.cred.Token = admin.cred.Token[:len(admin.cred.Token)-4] + "AAAA"
	admin.client, _ = admin.client.WithCredential(admin.cred)
	// The refused intent is still certain, so it re-reads state before any
	// resend; an invalid session is stopped there and nothing is sent.
	if _, err := adminclient.Submit(context.Background(), admin.home, admin.client, admin.cred, pause.ID, time.Now, accept); err == nil || !strings.Contains(err.Error(), "UNAUTHORIZED") {
		t.Fatalf("an invalid session must stop at the pre-send read: %v", err)
	}
	if commands, _ := s.committedFacts(t, pause.ID); commands != 0 {
		t.Fatal("nothing may be sent with an invalid session")
	}
	admin.login(t)
	if done := admin.submit(t, pause.ID, accept); done.Status != adminclient.StatusCommitted || done.Result.Replayed || len(done.Attempts) != 2 {
		t.Fatal("same key must execute once after re-authentication")
	}
	if commands, audits := s.committedFacts(t, pause.ID); commands != 1 || audits != 1 {
		t.Fatal("exactly one ledger and audit row after the retry")
	}
}

func TestToolTerminalArchiveTermsLockAndStalePlans(t *testing.T) {
	s := newStack(t)
	a, b := s.operator(t), s.operator(t)
	s.setAdmins(a.signer.address, b.signer.address)
	start, end := time.Now().Add(time.Hour), time.Now().Add(2*time.Hour)
	id, _ := draftAndPublish(t, a, start, end)
	other, otherRevision := draftAndPublish(t, a, start, end)
	if _, err := adminclient.Plan(context.Background(), a.home, a.client, a.cred, adminclient.PlanInput{Operation: "publish", CampaignID: id, CandidateRevisionID: otherRevision, Reason: "cross"}, time.Now()); err == nil {
		t.Fatal("another campaign's revision must not be plannable")
	}

	// Stale plan: B moves the publication after A reviewed it.
	stale := a.plan(t, adminclient.PlanInput{Operation: "pause", CampaignID: id})
	b.commit(t, adminclient.PlanInput{Operation: "pause", CampaignID: id})
	if _, err := adminclient.Submit(context.Background(), a.home, a.client, a.cred, stale.ID, time.Now, accept); err == nil || !strings.Contains(err.Error(), "moved") {
		t.Fatal("a stale first send must be refused locally")
	}
	if s.count(t, `SELECT count(*) FROM campaign_commands WHERE client_command_id=$1`, stale.ID) != 0 {
		t.Fatal("stale intent reached the server")
	}
	if _, err := adminclient.Abandon(a.home, stale.ID, time.Now()); err != nil {
		t.Fatal(err)
	}

	// Accepted history locks material terms even when cancelled later.
	reminder := s.reminder(t, other)
	if !reminder {
		t.Fatal("reminder fixture failed")
	}
	changed := a.commit(t, adminclient.PlanInput{Operation: "revision", CampaignID: other, RevisionJSON: revisionJSON(t, start, end, "Changed eligibility")})
	locked := a.plan(t, adminclient.PlanInput{Operation: "publish", CampaignID: other, CandidateRevisionID: *changed.Result.Original.RevisionID})
	if !strings.Contains(strings.Join(locked.Review.Warnings, ";"), "CAMPAIGN_TERMS_LOCKED") {
		t.Fatal("review must predict the terms lock")
	}
	if refused := a.submit(t, locked.ID, accept); refused.Status != adminclient.StatusRejected || refused.Attempts[0].Code != "CAMPAIGN_TERMS_LOCKED" {
		t.Fatal("server must refuse a material change after acceptance")
	}

	archived := a.commit(t, adminclient.PlanInput{Operation: "archive", CampaignID: id})
	if archived.Result.Current.PublicationVersion != "3" {
		t.Fatal("archive version")
	}
	resume := a.plan(t, adminclient.PlanInput{Operation: "resume", CampaignID: id})
	if !strings.Contains(strings.Join(resume.Review.Warnings, ";"), "CAMPAIGN_ARCHIVED") {
		t.Fatal("review must flag the terminal archive")
	}
	if refused := a.submit(t, resume.ID, accept); refused.Status != adminclient.StatusRejected || refused.Attempts[0].Code != "CAMPAIGN_ARCHIVED" {
		t.Fatal("archived campaign must stay terminal")
	}
}

// Join and pause contend for the same publication row lock. Whatever order the
// database picks, the join is either admitted before the pause or refused after
// it; it is never admitted under the paused version.
func TestToolPauseAndJoinSerializeOnTheCampaignLock(t *testing.T) {
	s := newStack(t)
	admin, participant := s.operator(t), s.operator(t)
	s.setAdmins(admin.signer.address)
	id, _ := draftAndPublish(t, admin, time.Now().Add(-time.Minute), time.Now().Add(time.Hour))
	pause := admin.plan(t, adminclient.PlanInput{Operation: "pause", CampaignID: id})
	joined := make(chan adminclient.Reply, 1)
	result := admin.submit(t, pause.ID, func(*adminclient.Intent, *adminclient.CurrentState) error {
		go func() {
			joined <- participant.interact(t, http.MethodPost, id+"/join", map[string]string{"expectedStateVersion": "0", "expectedPublicationVersion": "1"})
		}()
		return nil
	})
	join := <-joined
	if result.Status != adminclient.StatusCommitted {
		t.Fatal("pause must commit")
	}
	code, _ := join.ErrorCode()
	switch {
	case join.Status == http.StatusOK:
		if s.count(t, `SELECT accepted_publication_version::int FROM campaign_interaction_states WHERE campaign_id=$1`, id) != 1 {
			t.Fatal("admitted join must bind the pre-pause publication")
		}
	case join.Status == http.StatusConflict && (code == "CAMPAIGN_VERSION_CONFLICT" || code == "CAMPAIGN_NOT_OPEN"):
		if s.count(t, `SELECT count(*) FROM campaign_interaction_states WHERE campaign_id=$1`, id) != 0 {
			t.Fatal("refused join left state behind")
		}
	default:
		t.Fatalf("unexpected join outcome %s", join.Describe())
	}
}
