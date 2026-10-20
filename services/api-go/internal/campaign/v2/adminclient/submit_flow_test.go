package adminclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
)

// scripted is a fake API whose ledger and command answers are chosen per call,
// so each flow can assert what the client read, what it sent, and in which order.
type scripted struct {
	mu     sync.Mutex
	calls  []string
	bodies []string
	ledger func(n int) (int, []byte)
	post   func(n int) (int, []byte)
	view   []byte
}

func (s *scripted) count(prefix string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, call := range s.calls {
		if strings.HasPrefix(call, prefix) {
			n++
		}
	}
	return n
}

func (s *scripted) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		var status int
		var body []byte
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/commands/"):
			s.calls = append(s.calls, "ledger")
			status, body = s.ledger(len(s.calls))
		case r.Method == http.MethodGet:
			s.calls = append(s.calls, "view")
			status, body = 200, s.view
		default:
			raw, _ := io.ReadAll(r.Body)
			s.bodies = append(s.bodies, string(raw))
			s.calls = append(s.calls, "post")
			status, body = s.post(len(s.bodies))
		}
		w.WriteHeader(status)
		w.Write(body)
	}
}

func adminView(t *testing.T, version string) []byte {
	t.Helper()
	active := testActive
	hash := strings.Repeat("a", 64)
	raw, err := json.Marshal(v2.AdminCampaignResponse{SchemaVersion: 1, ServerNow: "2026-10-09T00:00:00Z", AsOf: "2026-10-09T00:00:00Z", Item: v2.AdminCampaignView{
		CampaignID: testCampaign, Source: "created", PublicationVersion: version, InteractionVersion: "0", Mode: "enabled", ActiveRevisionID: &active,
		EffectivePhase: "upcoming", MaterialTermsLock: "open", Revisions: []v2.AdminRevisionSummary{{ID: testActive, CreatedAt: "2026-10-09T00:00:00Z", SchemaVersion: 1, TermsCanonicalVersion: 1, ContentState: "ready", MaterialTermsHash: hash, ContentHash: hash, Published: true, Active: true}}}})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// flow saves intent in a private home behind a scripted server and returns a
// submit/reconcile pair bound to the same client and credential.
func flow(t *testing.T, intent *Intent, s *scripted) (func() (*Intent, error), func() (*Intent, error), Home) {
	t.Helper()
	server := httptest.NewServer(s.handler(t))
	t.Cleanup(server.Close)
	home, err := OpenHome(filepath.Join(t.TempDir(), "home"))
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(server.URL+"/api", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	cred := Credential{API: client.Base(), Address: testActor, Token: "non-secret-test-token"}
	if client, err = client.WithCredential(cred); err != nil {
		t.Fatal(err)
	}
	intent.API = client.Base()
	if err = home.SaveIntent(intent); err != nil {
		t.Fatal(err)
	}
	accept := func(*Intent, *CurrentState) error { return nil }
	submit := func() (*Intent, error) {
		return Submit(context.Background(), home, client, cred, intent.ID, time.Now, accept)
	}
	reconcile := func() (*Intent, error) {
		return Reconcile(context.Background(), home, client, cred, intent.ID, time.Now)
	}
	return submit, reconcile, home
}

func uncertainIntent(t *testing.T, status string) *Intent {
	intent := testIntent(t, "publish")
	intent.Status, intent.Uncertain = status, status == StatusUnknown
	intent.Attempts = []Attempt{{Kind: "send", Outcome: attemptUncertain, Note: "fixture: earlier send lost its reply"}}
	return intent
}

func persisted(t *testing.T, home Home, id string) *Intent {
	t.Helper()
	intent, err := home.LoadIntent(id)
	if err != nil {
		t.Fatal(err)
	}
	return intent
}

func TestUncertainIntentSettlesFromLedgerWithoutResending(t *testing.T) {
	intent := uncertainIntent(t, StatusUnknown)
	record := validRecord(intent, true)
	record.Current.PublicationVersion = "6"
	s := &scripted{ledger: func(int) (int, []byte) { return 200, encode(t, record) }, post: func(int) (int, []byte) { return 500, nil }}
	submit, _, home := flow(t, intent, s)
	got, err := submit()
	if err != nil || got.Status != StatusCommitted || got.Settlement != "ledger-record" || s.count("post") != 0 {
		t.Fatalf("status %s settlement %s posts %d err %v", got.Status, got.Settlement, s.count("post"), err)
	}
	if text := RenderOutcome(persisted(t, home, intent.ID)); !strings.Contains(text, "current version 6") || !strings.Contains(text, "changed since this command") {
		t.Fatalf("operator text must show the advanced current: %q", text)
	}
}

func TestUncertainIntentResendsSameBytesOnlyAfterAbsentRead(t *testing.T) {
	intent := uncertainIntent(t, StatusUnknown)
	s := &scripted{ledger: func(int) (int, []byte) { return 404, errorBody("CAMPAIGN_NOT_FOUND") }, post: func(int) (int, []byte) { return 200, encode(t, validRecord(intent, false)) }}
	submit, _, _ := flow(t, intent, s)
	got, err := submit()
	if err != nil || got.Status != StatusCommitted || got.Settlement != "executed" || got.Uncertain {
		t.Fatalf("status %s settlement %s err %v", got.Status, got.Settlement, err)
	}
	if strings.Join(s.calls, ",") != "ledger,post" || s.bodies[0] != intent.Body {
		t.Fatalf("order %v; resend must be the frozen body", s.calls)
	}
}

func TestUncertainIntentStopsWhenLedgerCannotBeRead(t *testing.T) {
	for _, c := range []struct {
		name   string
		status int
		body   []byte
	}{{"expired session", 401, errorBody("UNAUTHORIZED")}, {"revoked administrator", 403, errorBody("CAMPAIGN_ADMIN_REQUIRED")}, {"ledger unavailable", 503, errorBody("CAMPAIGN_DATA_UNAVAILABLE")}, {"gateway page", 502, []byte("<html>")}} {
		t.Run(c.name, func(t *testing.T) {
			intent := uncertainIntent(t, StatusUnknown)
			s := &scripted{ledger: func(int) (int, []byte) { return c.status, c.body }, post: func(int) (int, []byte) { return 200, encode(t, validRecord(intent, false)) }}
			submit, _, home := flow(t, intent, s)
			if _, err := submit(); err == nil || s.count("post") != 0 {
				t.Fatalf("must stop without sending: err %v posts %d", err, s.count("post"))
			}
			after := persisted(t, home, intent.ID)
			if after.Status != StatusUnknown || !after.Uncertain || !strings.Contains(RenderOutcome(after), "OUTCOME UNKNOWN") {
				t.Fatalf("unknown must survive: %s", after.Status)
			}
		})
	}
}

func TestUncertainResendRefusalsKeepOrSettleAsTheTableSays(t *testing.T) {
	for _, c := range []struct {
		code   string
		status int
		want   string
	}{{"CAMPAIGN_ADMIN_REQUIRED", 403, StatusUnknown}, {"CAMPAIGN_COMMAND_EXPIRED", 410, StatusUnknown}, {"CAMPAIGN_NOT_FOUND", 404, StatusUnknown}, {"CAMPAIGN_VERSION_CONFLICT", 409, StatusRejected}} {
		t.Run(c.code, func(t *testing.T) {
			intent := uncertainIntent(t, StatusUnknown)
			s := &scripted{ledger: func(int) (int, []byte) { return 404, errorBody("CAMPAIGN_NOT_FOUND") }, post: func(int) (int, []byte) { return c.status, errorBody(c.code) }}
			submit, _, home := flow(t, intent, s)
			if _, err := submit(); err != nil {
				t.Fatal(err)
			}
			after := persisted(t, home, intent.ID)
			if after.Status != c.want {
				t.Fatalf("got %s want %s", after.Status, c.want)
			}
			text := RenderOutcome(after)
			if c.want == StatusRejected && (!strings.HasPrefix(after.Settlement, "proven-not-executed:CAMPAIGN_VERSION_CONFLICT") || !strings.Contains(text, "NOT EXECUTED (proven)")) {
				t.Fatalf("settlement %q text %q", after.Settlement, text)
			}
			if c.want == StatusUnknown && (strings.Contains(text, "refused before execution") || !after.Uncertain) {
				t.Fatalf("a later refusal must not read as non-execution: %q", text)
			}
		})
	}
}

// A crash between persisting "sending" and recording the reply leaves the
// intent uncertain on disk; the next submit reconciles before anything else.
func TestInterruptedSendIsRecoveredThroughTheLedger(t *testing.T) {
	intent := uncertainIntent(t, StatusSending)
	s := &scripted{ledger: func(int) (int, []byte) { return 200, encode(t, validRecord(intent, true)) }, post: func(int) (int, []byte) { return 500, nil }}
	_, reconcile, home := flow(t, intent, s)
	got, err := reconcile()
	if err != nil || got.Status != StatusCommitted || s.count("post") != 0 || persisted(t, home, intent.ID).Status != StatusCommitted {
		t.Fatalf("status %s err %v", got.Status, err)
	}
}

// A certain intent refused for authorization keeps its key; the resend after
// access is restored re-checks freshness and executes exactly once.
func TestCertainRefusalIsRetryableWithFreshnessCheck(t *testing.T) {
	intent := testIntent(t, "publish")
	s := &scripted{view: adminView(t, "4"), ledger: func(int) (int, []byte) { return 404, nil }}
	s.post = func(n int) (int, []byte) {
		if n == 1 {
			return 403, errorBody("CAMPAIGN_ADMIN_REQUIRED")
		}
		return 200, encode(t, validRecord(intent, false))
	}
	submit, _, _ := flow(t, intent, s)
	got, err := submit()
	if err != nil || got.Status != StatusRejected || !got.Retryable {
		t.Fatalf("status %s retryable %t err %v", got.Status, got.Retryable, err)
	}
	got, err = submit()
	if err != nil || got.Status != StatusCommitted || strings.Join(s.calls, ",") != "view,post,view,post" || s.bodies[0] != s.bodies[1] {
		t.Fatalf("status %s calls %v err %v", got.Status, s.calls, err)
	}
	s.view = adminView(t, "5")
	stale := testIntent(t, "publish")
	submit, _, _ = flow(t, stale, s)
	if _, err = submit(); err == nil || !strings.Contains(err.Error(), "moved") {
		t.Fatal("a certain intent must not be sent after the publication moved")
	}
}
