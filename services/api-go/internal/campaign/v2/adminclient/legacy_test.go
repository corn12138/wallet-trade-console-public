package adminclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
)

const (
	legacyRun      = "019aa123-4567-7000-8000-0000000000aa"
	legacyTarget   = "legacy-target"
	legacyRowHash  = "1111111111111111111111111111111111111111111111111111111111111111"
	legacyInterHas = "2222222222222222222222222222222222222222222222222222222222222222"
	legacyNow      = "2026-10-09T00:00:00Z"
)

// legacyServer is a fake administration API: a two-page plan whose target
// item can change, and scripted answers for sends and activation reads.
type legacyServer struct {
	mu         sync.Mutex
	item       v2.LegacyPlanItemView
	posts      []string
	reads      int
	planReads  int
	send       func(n int, body string) (int, any) // nil body with status 0 drops the connection
	activation func(n int) (int, any)
}

// with runs fn under the server's lock: the handlers read this state from
// their own goroutines.
func (s *legacyServer) with(fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn()
}

func (s *legacyServer) postCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.posts)
}

func (s *legacyServer) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		path := strings.TrimPrefix(r.URL.Path, "/api/v2/campaign-admin/legacy-imports")
		var status int
		var value any
		switch {
		case r.Method == http.MethodGet && path == "/runs/"+legacyRun:
			s.planReads++
			status, value = 200, s.page(r.URL.Query().Get("cursor"))
		case r.Method == http.MethodGet && path == "/campaigns/"+legacyTarget+"/activation":
			s.reads++
			status, value = s.activation(s.reads)
		case r.Method == http.MethodPost:
			raw, _ := io.ReadAll(r.Body)
			s.posts = append(s.posts, string(raw))
			status, value = s.send(len(s.posts), string(raw))
		default:
			status, value = 404, v2.ErrorResponse{SchemaVersion: 1, Code: "CAMPAIGN_NOT_FOUND", RequestID: "fake"}
		}
		if status == 0 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}
			return
		}
		raw, err := json.Marshal(value)
		if err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Request-Id", r.Header.Get("X-Request-Id"))
		w.WriteHeader(status)
		_, _ = w.Write(raw)
	}
}

// page serves 50 filler items, then the target on the second page.
func (s *legacyServer) page(cursor string) v2.LegacyImportPlanResponse {
	out := v2.LegacyImportPlanResponse{SchemaVersion: 1, ServerNow: legacyNow, AsOf: legacyNow, Items: []v2.LegacyPlanItemView{},
		Run: v2.LegacyRunView{RunID: legacyRun, Status: "captured", Operator: "lg02b2", Rows: 51, RetainUntil: legacyNow}}
	if cursor == "" {
		for i := 0; i < 50; i++ {
			out.Items = append(out.Items, v2.LegacyPlanItemView{CampaignID: fmt.Sprintf("filler-%02d", i), Decision: "import-public", LegacyRowSha256: legacyRowHash,
				InteractionsSha256: legacyInterHas, Findings: []v2.LegacyFindingSummary{}})
		}
		next := "ZmlsbGVyLTQ5"
		out.NextCursor = &next
		return out
	}
	out.Items = append(out.Items, s.item)
	return out
}

func legacyItem(decision string, findings ...v2.LegacyFindingSummary) v2.LegacyPlanItemView {
	if findings == nil {
		findings = []v2.LegacyFindingSummary{}
	}
	return v2.LegacyPlanItemView{CampaignID: legacyTarget, Decision: decision, LegacyRowSha256: legacyRowHash, InteractionsSha256: legacyInterHas, ParticipantRows: 2,
		Findings: findings, Fence: &v2.LegacyFenceView{FencedBy: "lg02b2", FencedAt: legacyNow}}
}

type legacyFixture struct {
	server *legacyServer
	home   Home
	client *Client
	cred   Credential
}

func newLegacyFixture(t *testing.T, item v2.LegacyPlanItemView) *legacyFixture {
	t.Helper()
	s := &legacyServer{item: item}
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
	return &legacyFixture{server: s, home: home, client: client, cred: cred}
}

func (f *legacyFixture) submit(id string) (*LegacyIntent, error) {
	return SubmitLegacy(context.Background(), f.home, f.client, f.cred, id, time.Now, func(*LegacyIntent, v2.LegacyPlanItemView) error { return nil })
}

func (f *legacyFixture) activationIntent(t *testing.T) *LegacyIntent {
	t.Helper()
	intent, err := PlanLegacyActivation(context.Background(), f.home, f.client, f.cred, legacyRun, legacyTarget, "activate the legacy campaign", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return intent
}

// recordOf is the activation record the server would hold for intent; other
// gives it to another command instead.
func recordOf(t *testing.T, intent *LegacyIntent, other bool) v2.LegacyActivationView {
	t.Helper()
	var body v2.LegacyActivationCommandInput
	if err := json.Unmarshal([]byte(intent.Body), &body); err != nil {
		t.Fatal(err)
	}
	if other {
		body.ClientCommandID = "019aa123-4567-7000-8000-0000000000ff"
	}
	hash, err := v2.LegacyActivationPayloadHash(legacyRun, legacyTarget, body)
	if err != nil {
		t.Fatal(err)
	}
	publish := "019aa123-4567-7000-8000-0000000000ee"
	return v2.LegacyActivationView{ActivationID: "019aa123-4567-7000-8000-0000000000cc", CampaignID: legacyTarget, RunID: legacyRun, Decision: "import-public",
		RevisionID: "019aa123-4567-7000-8000-0000000000dd", CommandID: "019aa123-4567-7000-8000-0000000000de", PublishCommandID: &publish, ClientCommandID: body.ClientCommandID,
		PayloadHash: hash, ActivatedBy: testActor, ActivatedAt: legacyNow, PublicationVersion: "1", InteractionVersion: "2", ImportedInteractions: 2,
		Comparison: v2.LegacyComparisonView{Instant: legacyNow, Wallets: 3, Groups: []v2.LegacyDifferenceGroupView{}, Classes: []string{}, Accepted: []string{}}}
}

func activationReply(view v2.LegacyActivationView, replayed bool) v2.LegacyActivationResponse {
	return v2.LegacyActivationResponse{SchemaVersion: 1, ServerNow: legacyNow, AsOf: legacyNow, Activation: view, Replayed: replayed}
}

func refusal(code string) v2.ErrorResponse {
	return v2.ErrorResponse{SchemaVersion: 1, Code: code, RequestID: "fake"}
}

func notActivated(int) (int, any) { return 404, refusal("CAMPAIGN_LEGACY_NOT_ACTIVATED") }

func TestLegacyReviewIsFrozenThenSent(t *testing.T) {
	f := newLegacyFixture(t, legacyItem("review-required", v2.LegacyFindingSummary{Kind: "PARTICIPANT_WALLET_CASE_LOOKUP", Severity: "review", Count: 1},
		v2.LegacyFindingSummary{Kind: "UNKNOWN_ACCEPTANCE", Severity: "info", Count: 1}))
	ctx := context.Background()
	in := LegacyReviewInput{RunID: legacyRun, CampaignID: legacyTarget, Decision: "import-public", Reason: "checksum wallet becomes visible", Accept: []string{"walletState"}}
	if _, err := PlanLegacyReview(ctx, f.home, f.client, f.cred, in, time.Now()); err == nil || !strings.Contains(err.Error(), "PARTICIPANT_WALLET_CASE_LOOKUP") {
		t.Fatalf("a review must acknowledge the item's review findings: %v", err)
	}
	in.Acknowledge = []string{" PARTICIPANT_WALLET_CASE_LOOKUP "}
	intent, err := PlanLegacyReview(ctx, f.home, f.client, f.cred, in, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// Each freeze pages through the plan to the item: two pages per attempt.
	var reads int
	f.server.with(func() { reads = f.server.planReads })
	if intent.Status != StatusPlanned || intent.Path != legacyPath(LegacyReview, legacyRun, legacyTarget) || f.server.postCount() != 0 || reads != 4 ||
		!strings.Contains(intent.Body, `"acknowledgedFindings":["PARTICIPANT_WALLET_CASE_LOOKUP"]`) {
		t.Fatalf("frozen review: %+v, %d posts, %d plan reads", intent, f.server.postCount(), reads)
	}
	var body v2.LegacyReviewCommandInput
	_ = json.Unmarshal([]byte(intent.Body), &body)
	recorded := v2.LegacyReviewView{ReviewID: "019aa123-4567-7000-8000-0000000000bb", RunID: legacyRun, CampaignID: legacyTarget, Decision: body.Decision,
		AcceptedDifferences: body.AcceptedDifferences, AcknowledgedFindings: body.AcknowledgedFindings, Reason: body.Reason, Reviewer: testActor, ClientCommandID: intent.ID, DecidedAt: legacyNow}
	f.server.with(func() {
		f.server.send = func(int, string) (int, any) {
			return 201, v2.LegacyReviewResponse{SchemaVersion: 1, ServerNow: legacyNow, AsOf: legacyNow, Review: recorded}
		}
	})
	sent, err := f.submit(intent.ID)
	var posted []string
	f.server.with(func() { posted = append(posted, f.server.posts...) })
	if err != nil || sent.Status != StatusCommitted || sent.Settlement != "executed" || sent.Review == nil || len(posted) != 1 || posted[0] != intent.Body {
		t.Fatalf("review send: %+v %v", sent, err)
	}
	if _, err = f.submit(intent.ID); err == nil {
		t.Fatal("a committed intent is not sent again")
	}
	// The item now holds the review; status reads it back as this intent's.
	f.server.with(func() { f.server.item.Review = &recorded })
	if settled, err := ReconcileLegacy(ctx, f.home, f.client, f.cred, intent.ID, time.Now); err != nil || settled.Status != StatusCommitted ||
		settled.Attempts[len(settled.Attempts)-1].Outcome != attemptCommitted {
		t.Fatalf("status of a committed review: %+v %v", settled, err)
	}
}

func TestLegacyIntentIntegrityAndOwnership(t *testing.T) {
	f := newLegacyFixture(t, legacyItem("import-public"))
	intent := f.activationIntent(t)
	path := filepath.Join(f.home.Dir, "legacy-intents", intent.ID+".json")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string]func(*LegacyIntent){
		"body":   func(i *LegacyIntent) { i.Body = strings.Replace(i.Body, "activate the legacy", "activate another", 1) },
		"path":   func(i *LegacyIntent) { i.Path = legacyPath(LegacyActivation, legacyRun, "another-campaign") },
		"target": func(i *LegacyIntent) { i.CampaignID = "another-campaign" },
		"key": func(i *LegacyIntent) {
			i.Body = strings.Replace(i.Body, i.ID, "019aa123-4567-7000-8000-000000000123", 1)
			i.BodySHA256 = digest(i.Body)
		},
	} {
		var tampered LegacyIntent
		_ = json.Unmarshal(original, &tampered)
		edit(&tampered)
		if err = writePrivate(path, &tampered); err != nil {
			t.Fatal(err)
		}
		if _, err = f.submit(intent.ID); err == nil || !strings.Contains(err.Error(), "integrity") {
			t.Errorf("an edited %s must not be sent: %v", name, err)
		}
	}
	if err = os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	other := f.cred
	other.Address = "0x2222222222222222222222222222222222222222"
	if _, err = SubmitLegacy(context.Background(), f.home, f.client, other, intent.ID, time.Now, func(*LegacyIntent, v2.LegacyPlanItemView) error { return nil }); err == nil {
		t.Error("another administrator's session must not send this intent")
	}
	if n := f.server.postCount(); n != 0 {
		t.Fatalf("nothing may be sent, sent %d", n)
	}
}

// A first send re-reads the plan item and refuses one that moved.
func TestLegacySubmitRefusesAChangedItem(t *testing.T) {
	for name, change := range map[string]func(*v2.LegacyPlanItemView){
		"digest": func(i *v2.LegacyPlanItemView) { i.LegacyRowSha256 = strings.Repeat("3", 64) },
		"fence":  func(i *v2.LegacyPlanItemView) { lifted := legacyNow; i.Fence.LiftedAt = &lifted },
		"activated": func(i *v2.LegacyPlanItemView) {
			i.Activation = &v2.LegacyActivationSummary{ActivationID: "019aa123-4567-7000-8000-0000000000cc"}
		},
	} {
		f := newLegacyFixture(t, legacyItem("import-public"))
		intent := f.activationIntent(t)
		f.server.with(func() { change(&f.server.item) })
		if _, err := f.submit(intent.ID); err == nil || f.server.postCount() != 0 {
			t.Errorf("%s: a changed item must stop the send: %v, %d posts", name, err, f.server.postCount())
		}
	}
	f := newLegacyFixture(t, legacyItem("import-public"))
	intent := f.activationIntent(t)
	refuse := func(*LegacyIntent, v2.LegacyPlanItemView) error { return fmt.Errorf("confirmation did not match") }
	if _, err := SubmitLegacy(context.Background(), f.home, f.client, f.cred, intent.ID, time.Now, refuse); err == nil || f.server.postCount() != 0 {
		t.Errorf("an unconfirmed intent must not be sent: %v", err)
	}
}

func TestLegacyFirstSendClassification(t *testing.T) {
	for _, tc := range []struct {
		name       string
		send       func(*testing.T, *LegacyIntent) (int, any)
		status     string
		retryable  bool
		settlement string
	}{
		{"created", func(t *testing.T, i *LegacyIntent) (int, any) {
			return 201, activationReply(recordOf(t, i, false), false)
		}, StatusCommitted, false, "executed"},
		{"another command's record", func(t *testing.T, i *LegacyIntent) (int, any) {
			return 201, activationReply(recordOf(t, i, true), false)
		}, StatusUnknown, false, ""},
		{"replay of a first send", func(t *testing.T, i *LegacyIntent) (int, any) {
			return 200, activationReply(recordOf(t, i, false), true)
		}, StatusUnknown, false, ""},
		{"created yet replayed", func(t *testing.T, i *LegacyIntent) (int, any) {
			return 201, activationReply(recordOf(t, i, false), true)
		}, StatusUnknown, false, ""},
		{"payload hash differs", func(t *testing.T, i *LegacyIntent) (int, any) {
			view := recordOf(t, i, false)
			view.PayloadHash = strings.Repeat("4", 64)
			return 201, activationReply(view, false)
		}, StatusUnknown, false, ""},
		{"session rejected", func(*testing.T, *LegacyIntent) (int, any) { return 401, refusal("UNAUTHORIZED") }, StatusRejected, true, "refused-before-execution:UNAUTHORIZED"},
		{"plan changed", func(*testing.T, *LegacyIntent) (int, any) { return 409, refusal("CAMPAIGN_LEGACY_PLAN_CHANGED") }, StatusRejected, false, "refused-before-execution:CAMPAIGN_LEGACY_PLAN_CHANGED"},
		{"comparison failed", func(*testing.T, *LegacyIntent) (int, any) {
			return 409, v2.LegacyImportRefusal{SchemaVersion: 1, Code: "CAMPAIGN_LEGACY_COMPARISON_FAILED", RequestID: "fake", Drift: []v2.LegacyDriftView{},
				Comparison: &v2.LegacyComparisonView{Instant: legacyNow, Wallets: 2, Classes: []string{"participantCount"}, Accepted: []string{},
					Groups: []v2.LegacyDifferenceGroupView{{Class: "participantCount", Field: "participantCount", Before: "aaaa", After: "bbbb", Count: 1, WalletRefs: []string{}}}}}
		}, StatusRejected, false, "refused-before-execution:CAMPAIGN_LEGACY_COMPARISON_FAILED"},
		{"server error", func(*testing.T, *LegacyIntent) (int, any) { return 503, refusal("CAMPAIGN_DATA_UNAVAILABLE") }, StatusUnknown, false, ""},
		{"connection dropped", func(*testing.T, *LegacyIntent) (int, any) { return 0, nil }, StatusUnknown, false, ""},
	} {
		f := newLegacyFixture(t, legacyItem("import-public"))
		intent := f.activationIntent(t)
		f.server.with(func() { f.server.send = func(int, string) (int, any) { return tc.send(t, intent) } })
		got, err := f.submit(intent.ID)
		if err != nil || got.Status != tc.status || got.Retryable != tc.retryable || got.Settlement != tc.settlement || got.Uncertain != (tc.status == StatusUnknown) {
			t.Errorf("%s: %s retryable=%v settlement=%q uncertain=%v %v", tc.name, got.Status, got.Retryable, got.Settlement, got.Uncertain, err)
		}
		if tc.name == "comparison failed" && (got.Refusal == nil || !strings.Contains(RenderLegacyOutcome(got), "participantCount participantCount: aaaa -> bbbb")) {
			t.Errorf("the refusal report is kept and shown: %s", RenderLegacyOutcome(got))
		}
		if (got.Status == StatusCommitted) != (got.Activation != nil) {
			t.Errorf("%s: only a committed intent keeps a record", tc.name)
		}
	}
}

// An uncertain activation is settled from the campaign's activation record
// before anything is resent, and only a permanent refusal of a resend, with
// no record behind it, proves that nothing committed.
func TestLegacyUncertainActivationSettlesFromTheRecord(t *testing.T) {
	for _, tc := range []struct {
		name       string
		activation func(*testing.T, *LegacyIntent) func(int) (int, any)
		resend     func(*testing.T, *LegacyIntent) (int, any)
		posts      int
		status     string
		settlement string
		failed     bool
	}{
		{"own record", func(t *testing.T, i *LegacyIntent) func(int) (int, any) {
			return func(int) (int, any) { return 200, activationReply(recordOf(t, i, false), false) }
		}, nil, 1, StatusCommitted, "ledger-record", false},
		{"another command's record", func(t *testing.T, i *LegacyIntent) func(int) (int, any) {
			return func(int) (int, any) { return 200, activationReply(recordOf(t, i, true), false) }
		}, nil, 1, StatusRejected, "proven-not-executed:superseded", false},
		{"absent, resend replays", func(*testing.T, *LegacyIntent) func(int) (int, any) { return notActivated },
			func(t *testing.T, i *LegacyIntent) (int, any) {
				return 200, activationReply(recordOf(t, i, false), true)
			}, 2, StatusCommitted, "ledger-record", false},
		{"absent, resend drifts", func(*testing.T, *LegacyIntent) func(int) (int, any) { return notActivated },
			func(*testing.T, *LegacyIntent) (int, any) {
				return 409, v2.LegacyImportRefusal{SchemaVersion: 1, Code: "CAMPAIGN_LEGACY_DRIFT", RequestID: "fake",
					Drift: []v2.LegacyDriftView{{Table: "launchpad_campaigns", Change: "changed", RowRef: "aaaaaaaaaaaaaaaa"}}}
			}, 2, StatusRejected, "proven-not-executed:CAMPAIGN_LEGACY_DRIFT", false},
		{"absent, resend fails the comparison", func(*testing.T, *LegacyIntent) func(int) (int, any) { return notActivated },
			func(*testing.T, *LegacyIntent) (int, any) {
				return 409, v2.LegacyImportRefusal{SchemaVersion: 1, Code: "CAMPAIGN_LEGACY_COMPARISON_FAILED", RequestID: "fake", Drift: []v2.LegacyDriftView{},
					Comparison: &v2.LegacyComparisonView{Instant: legacyNow, Wallets: 1, Groups: []v2.LegacyDifferenceGroupView{}, Classes: []string{"field"}, Accepted: []string{}}}
			}, 2, StatusUnknown, "", false},
		{"absent, resend meets another activation", func(t *testing.T, i *LegacyIntent) func(int) (int, any) {
			return func(n int) (int, any) {
				if n == 1 {
					return notActivated(n)
				}
				return 200, activationReply(recordOf(t, i, true), false)
			}
		}, func(*testing.T, *LegacyIntent) (int, any) { return 409, refusal("CAMPAIGN_LEGACY_ALREADY_ACTIVATED") }, 2, StatusRejected, "proven-not-executed:superseded", false},
		{"record read refused", func(*testing.T, *LegacyIntent) func(int) (int, any) {
			return func(int) (int, any) { return 403, refusal("CAMPAIGN_ADMIN_REQUIRED") }
		}, nil, 1, StatusUnknown, "", true},
	} {
		f := newLegacyFixture(t, legacyItem("import-public"))
		intent := f.activationIntent(t)
		f.server.with(func() { f.server.send = func(int, string) (int, any) { return 0, nil } })
		if lost, err := f.submit(intent.ID); err != nil || lost.Status != StatusUnknown {
			t.Fatalf("%s: a dropped connection leaves the outcome unknown: %+v %v", tc.name, lost, err)
		}
		f.server.with(func() {
			f.server.activation = tc.activation(t, intent)
			f.server.send = func(n int, body string) (int, any) {
				if body != intent.Body {
					t.Errorf("%s: a resend must reuse the frozen bytes", tc.name)
				}
				return tc.resend(t, intent)
			}
		})
		got, err := f.submit(intent.ID)
		if (err != nil) != tc.failed || got.Status != tc.status || got.Settlement != tc.settlement || f.server.postCount() != tc.posts {
			t.Errorf("%s: %s %q after %d posts, err %v", tc.name, got.Status, got.Settlement, f.server.postCount(), err)
		}
		stored, err := f.home.LoadLegacyIntent(intent.ID)
		if err != nil || stored.Status != got.Status || stored.Uncertain != (got.Status == StatusUnknown) {
			t.Errorf("%s: the settled state is saved: %+v %v", tc.name, stored, err)
		}
	}
}

func TestLegacyAbandonAndReconcileNeedTheRightState(t *testing.T) {
	f := newLegacyFixture(t, legacyItem("import-public"))
	intent := f.activationIntent(t)
	if _, err := ReconcileLegacy(context.Background(), f.home, f.client, f.cred, intent.ID, time.Now); err == nil {
		t.Error("a never-sent intent has nothing to reconcile")
	}
	if abandoned, err := AbandonLegacy(f.home, intent.ID, time.Now()); err != nil || abandoned.Status != StatusAbandoned {
		t.Fatalf("abandon: %+v %v", abandoned, err)
	}
	if _, err := f.submit(intent.ID); err == nil || f.server.postCount() != 0 {
		t.Errorf("an abandoned intent is never sent: %v", err)
	}
	listed, err := f.home.ListLegacyIntents()
	if err != nil || len(listed) != 1 || listed[0].ID != intent.ID {
		t.Errorf("list: %+v %v", listed, err)
	}
}
