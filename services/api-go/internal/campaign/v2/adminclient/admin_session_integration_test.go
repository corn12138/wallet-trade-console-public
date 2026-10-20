//go:build integration

package adminclient_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/adminclient"
)

// holdCampaignLock takes the row lock every campaign command waits for and
// returns its release.
func holdCampaignLock(t *testing.T, s *stack, campaignID string) func() {
	t.Helper()
	tx, err := s.pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(context.Background(), `SELECT 1 FROM campaign_publications WHERE campaign_id=$1 FOR UPDATE`, campaignID); err != nil {
		t.Fatal(err)
	}
	released := false
	release := func() {
		if !released {
			released = true
			_ = tx.Rollback(context.Background())
		}
	}
	t.Cleanup(release)
	return release
}

// waitBlocked returns once a request whose path contains part has reached the
// API and some backend is waiting for a lock: the command passed its first
// authorization and now waits for the campaign row.
func waitBlocked(t *testing.T, s *stack, mark int, part string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if len(s.requestsSince(mark, part)) > 0 && s.count(t, `SELECT count(*) FROM pg_locks WHERE NOT granted`) > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the command never waited on the campaign lock")
}

// Authorization is checked again after the campaign lock: a participant who
// logs out, or an administrator whose sessions an operator revokes, while the
// command waits is refused with nothing written. A later login can still send
// the administrator's same command key, which then executes exactly once.
func TestSessionEndedWhileWaitingForTheCampaignLockIsRefused(t *testing.T) {
	s := newStack(t)
	admin, participant := s.operator(t), s.operator(t)
	s.setAdmins(admin.signer.address)
	id, _ := draftAndPublish(t, admin, time.Now().Add(-time.Minute), time.Now().Add(time.Hour))

	release := holdCampaignLock(t, s, id)
	commandID, err := adminclient.NewCommandID(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]string{"clientCommandId": commandID, "expectedStateVersion": "0", "expectedPublicationVersion": "1"})
	joined := make(chan adminclient.Reply, 1)
	mark := s.mark()
	go func() {
		joined <- participant.client.Do(context.Background(), http.MethodPost, "/v2/campaigns/"+id+"/join", raw, "")
	}()
	waitBlocked(t, s, mark, "/join")
	if outcome, err := adminclient.Logout(context.Background(), participant.home, 5*time.Second); err != nil || outcome.Result != adminclient.LogoutRevoked {
		t.Fatalf("participant logout: %+v %v", outcome, err)
	}
	release()
	join := <-joined
	if code, _ := join.ErrorCode(); join.Status != http.StatusUnauthorized || code != "UNAUTHORIZED" {
		t.Fatalf("a join whose session ended during the lock wait: %s", join.Describe())
	}
	if s.count(t, `SELECT count(*) FROM campaign_interaction_states WHERE campaign_id=$1`, id) != 0 || s.count(t, `SELECT count(*) FROM campaign_commands WHERE client_command_id=$1`, commandID) != 0 {
		t.Fatal("a refused join left state or a ledger row")
	}

	release = holdCampaignLock(t, s, id)
	pause := admin.plan(t, adminclient.PlanInput{Operation: "pause", CampaignID: id})
	submitted := make(chan *adminclient.Intent, 1)
	mark = s.mark()
	go func() {
		intent, _ := adminclient.Submit(context.Background(), admin.home, admin.client, admin.cred, pause.ID, time.Now, accept)
		submitted <- intent
	}()
	waitBlocked(t, s, mark, "/publications")
	s.revokeSessions(t, admin.signer.address)
	release()
	if intent := <-submitted; intent != nil && intent.Status == adminclient.StatusCommitted {
		t.Fatal("a command whose session was revoked during the lock wait cannot commit")
	}
	if commands, audits := s.committedFacts(t, pause.ID); commands != 0 || audits != 0 {
		t.Fatalf("revoked during the lock wait, the pause executed (%d commands, %d audits)", commands, audits)
	}
	if s.count(t, `SELECT publication_version::int FROM campaign_publications WHERE campaign_id=$1`, id) != 1 {
		t.Fatal("the publication changed")
	}

	// Revocation limits future access only; it never rewrites the intent.
	admin.login(t)
	settled := admin.submit(t, pause.ID, accept)
	if settled.Status != adminclient.StatusCommitted {
		t.Fatalf("after a new login the same key must execute: %s", settled.Status)
	}
	if commands, audits := s.committedFacts(t, pause.ID); commands != 1 || audits != 1 {
		t.Fatal("the same command key must execute exactly once")
	}
}

// Logout ends the session on the server: a copy of the token stops working on
// private reads and writes at once, a repeated logout reports that the session
// had already ended, and an intent whose outcome was unknown before logout is
// settled from the ledger after a new login without a second send.
func TestLogoutEndsTheServerSessionAndKeepsIntentsReconcilable(t *testing.T) {
	s := newStack(t)
	a := s.operator(t)
	s.setAdmins(a.signer.address)
	id, _ := draftAndPublish(t, a, time.Now().Add(time.Hour), time.Now().Add(2*time.Hour))
	pause := a.plan(t, adminclient.PlanInput{Operation: "pause", CampaignID: id})
	s.inject("/api/v2/campaign-admin/campaigns/"+id+"/publications", faultDropResponse)
	if got := a.submit(t, pause.ID, accept); got.Status != adminclient.StatusUnknown {
		t.Fatalf("fixture: a lost reply must be unknown, got %s", got.Status)
	}

	copied, stale := a.cred, a.client
	outcome, err := adminclient.Logout(context.Background(), a.home, 5*time.Second)
	if err != nil || outcome.Result != adminclient.LogoutRevoked || outcome.Address != strings.ToLower(a.signer.address) {
		t.Fatalf("logout: %+v %v", outcome, err)
	}
	if _, err := a.home.LoadCredential(time.Now()); !errors.Is(err, adminclient.ErrCredentialMissing) {
		t.Fatal("the local session must be removed")
	}
	ctx := context.Background()
	for _, probe := range []struct{ method, path string }{
		{http.MethodGet, "/v2/campaign-admin/campaigns/" + id},
		{http.MethodGet, "/v2/campaigns/me/history"},
		{http.MethodPost, "/v2/campaign-admin/campaigns/" + id + "/publications"},
		{http.MethodGet, "/auth/me"},
		{http.MethodGet, "/web3-auth/me"},
	} {
		if reply := stale.Do(ctx, probe.method, probe.path, []byte(`{}`), ""); reply.Status != http.StatusUnauthorized {
			t.Fatalf("%s %s with the logged-out token: %s", probe.method, probe.path, reply.Describe())
		}
	}
	if err := a.home.SaveCredential(copied); err != nil {
		t.Fatal(err)
	}
	if again, err := adminclient.Logout(ctx, a.home, 5*time.Second); err != nil || again.Result != adminclient.LogoutAlreadyRevoked {
		t.Fatalf("repeated logout: %+v %v", again, err)
	}

	a.login(t)
	mark := s.mark()
	settled := a.submit(t, pause.ID, accept)
	if settled.Status != adminclient.StatusCommitted || settled.Settlement != "ledger-record" {
		t.Fatalf("the unknown pause must settle from the ledger: %s %s", settled.Status, settled.Settlement)
	}
	for _, request := range s.requestsSince(mark, "/publications") {
		if strings.HasPrefix(request, "POST ") {
			t.Fatalf("settling must not resend: %v", request)
		}
	}
	if commands, audits := s.committedFacts(t, pause.ID); commands != 1 || audits != 1 {
		t.Fatal("the pause must have executed exactly once")
	}
}

// Operator revocation ends every live session of one wallet and no other.
func TestOperatorRevocationEndsEverySessionOfOneWallet(t *testing.T) {
	s := newStack(t)
	a, b := s.operator(t), s.operator(t)
	s.setAdmins(a.signer.address, b.signer.address)
	id, _ := draftAndPublish(t, a, time.Now().Add(time.Hour), time.Now().Add(2*time.Hour))
	first := a.client
	a.login(t)
	second := a.client
	path := "/v2/campaign-admin/campaigns/" + id
	ctx := context.Background()
	for _, client := range []*adminclient.Client{first, second, b.client} {
		if reply := client.Do(ctx, http.MethodGet, path, nil, ""); reply.Status != http.StatusOK {
			t.Fatalf("fixture: live session read: %s", reply.Describe())
		}
	}
	s.revokeSessions(t, a.signer.address)
	for _, client := range []*adminclient.Client{first, second} {
		if reply := client.Do(ctx, http.MethodGet, path, nil, ""); reply.Status != http.StatusUnauthorized {
			t.Fatalf("a revoked session still reads: %s", reply.Describe())
		}
	}
	if reply := b.client.Do(ctx, http.MethodGet, path, nil, ""); reply.Status != http.StatusOK {
		t.Fatalf("another wallet's session must survive: %s", reply.Describe())
	}
	a.login(t)
	if reply := a.client.Do(ctx, http.MethodGet, path, nil, ""); reply.Status != http.StatusOK {
		t.Fatalf("a new login after revocation must work: %s", reply.Describe())
	}
}
