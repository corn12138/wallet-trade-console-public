package campaignhttp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth/authtest"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/legacyactivation"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/legacyimport"
	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/application"
)

const (
	testRun        = "019aa123-4567-7000-8000-0000000000aa"
	testCampaign   = "legacy-campaign-1"
	testDigest     = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testReviewID   = "019aa123-4567-7000-8000-0000000000bb"
	testActivation = "019aa123-4567-7000-8000-0000000000cc"
	planPath       = "/campaign-admin/legacy-imports/runs/" + testRun
	reviewPath     = planPath + "/campaigns/" + testCampaign + "/review"
	activationPath = planPath + "/campaigns/" + testCampaign + "/activation"
	statusPath     = "/campaign-admin/legacy-imports/campaigns/" + testCampaign + "/activation"
	reviewBody     = `{"clientCommandId":"019aa123-4567-7000-8000-000000000001","decision":"import-public","acceptedDifferences":["walletState"],"acknowledgedFindings":["PARTICIPANT_WALLET_CASE_LOOKUP"],"reason":"checked"}`
	activationBody = `{"clientCommandId":"019aa123-4567-7000-8000-000000000002","reason":"activate","legacyRowSha256":"` + testDigest + `","interactionsSha256":"` + testDigest + `"}`
)

// legacyProbe stands in for the activation engine: it records what reached it
// and runs the re-authorization the engine runs inside its transaction.
type legacyProbe struct {
	calls         int
	runID, cursor string
	review        legacyactivation.ReviewInput
	activation    legacyactivation.ActivationInput
	plan          legacyactivation.Plan
	reviewed      legacyactivation.Review
	activated     legacyactivation.Activation
	found         bool
	err           error
}

func (p *legacyProbe) Plan(_ context.Context, runID, cursor string) (legacyactivation.Plan, error) {
	p.calls++
	p.runID, p.cursor = runID, cursor
	return p.plan, p.err
}
func (p *legacyProbe) RecordReview(ctx context.Context, in legacyactivation.ReviewInput) (legacyactivation.Review, error) {
	p.calls++
	p.review = in
	if in.Authorize == nil {
		return legacyactivation.Review{}, errors.New("no re-authorization")
	}
	if err := in.Authorize(ctx); err != nil {
		return legacyactivation.Review{}, err
	}
	return p.reviewed, p.err
}
func (p *legacyProbe) Activate(ctx context.Context, in legacyactivation.ActivationInput) (legacyactivation.Activation, error) {
	p.calls++
	p.activation = in
	if in.Authorize == nil {
		return legacyactivation.Activation{}, errors.New("no re-authorization")
	}
	if err := in.Authorize(ctx); err != nil {
		return legacyactivation.Activation{}, err
	}
	return p.activated, p.err
}
func (p *legacyProbe) ActivationOf(_ context.Context, campaignID string) (legacyactivation.Activation, bool, error) {
	p.calls++
	p.runID = campaignID
	return p.activated, p.found, p.err
}

// accessProbe allows the first checks and refuses every later one.
type accessProbe struct {
	allowed, checks int
	refusal         error
	actors          []string
	admin           []bool
}

func (a *accessProbe) Require(_ context.Context, actor string, admin bool) error {
	a.checks++
	a.actors, a.admin = append(a.actors, actor), append(a.admin, admin)
	if a.checks > a.allowed {
		return a.refusal
	}
	return nil
}

func legacyRouter(legacy LegacyImports, access application.Authorizer) http.Handler {
	return Router(Config{Queries: &readProbe{}, Verifier: testAuth.Verifier, Enabled: true, Legacy: legacy, Access: access})
}

func adminOnly(wallet string) Authorizer {
	return Authorizer{Verifier: testAuth.Verifier, Admins: func() []string { return []string{wallet} }}
}

var legacyRoutes = []struct{ method, path, body string }{
	{"GET", planPath, ""}, {"POST", reviewPath, reviewBody}, {"POST", activationPath, activationBody}, {"GET", statusPath, ""},
}

func TestLegacyRoutesRefuseBeforeTheEngine(t *testing.T) {
	probe := &legacyProbe{}
	other := adminOnly("0x2222222222222222222222222222222222222222")
	down := authtest.New(t)
	downToken, _ := down.Login(t, testWallet, 1)
	down.Sessions.SetDown(true)
	cases := []struct {
		name          string
		router        http.Handler
		authorization string
		status        int
		code          string
	}{
		{"anonymous", legacyRouter(probe, other), "", http.StatusUnauthorized, "UNAUTHORIZED"},
		{"not an administrator", legacyRouter(probe, other), bearer(t), http.StatusForbidden, "CAMPAIGN_ADMIN_REQUIRED"},
		{"session store down", Router(Config{Queries: &readProbe{}, Verifier: down.Verifier, Enabled: true, Legacy: probe,
			Access: Authorizer{Verifier: down.Verifier, Admins: func() []string { return []string{testWallet} }}}), "Bearer " + downToken, http.StatusServiceUnavailable, "WEB3_SESSION_UNAVAILABLE"},
		{"engine not wired", Router(Config{Queries: &readProbe{}, Verifier: testAuth.Verifier, Enabled: true, Access: adminOnly(testWallet)}), bearer(t), http.StatusServiceUnavailable, "CAMPAIGN_UNAVAILABLE"},
		{"authorizer not wired", Router(Config{Queries: &readProbe{}, Verifier: testAuth.Verifier, Enabled: true, Legacy: probe}), bearer(t), http.StatusServiceUnavailable, "CAMPAIGN_UNAVAILABLE"},
		{"disabled", Router(Config{Queries: &readProbe{}, Verifier: testAuth.Verifier, Legacy: probe, Access: adminOnly(testWallet)}), bearer(t), http.StatusServiceUnavailable, "CAMPAIGN_UNAVAILABLE"},
	}
	for _, route := range legacyRoutes {
		for _, tc := range cases {
			w := response(t, tc.router, route.method, route.path, route.body, tc.authorization)
			if w.Code != tc.status || code(t, w) != tc.code {
				t.Errorf("%s %s, %s: %d %s", route.method, route.path, tc.name, w.Code, w.Body.String())
			}
		}
	}
	if probe.calls != 0 {
		t.Fatalf("refused callers reached the engine %d times", probe.calls)
	}
}

// The session and administrator grant are checked on entry and again inside
// the engine's transaction; a refusal there is the response.
func TestLegacyCommandsReauthorizeInsideTheEngine(t *testing.T) {
	for _, route := range []struct{ path, body string }{{reviewPath, reviewBody}, {activationPath, activationBody}} {
		probe := &legacyProbe{}
		access := &accessProbe{allowed: 1, refusal: &v2.DomainError{Code: "CAMPAIGN_ADMIN_REQUIRED", Status: http.StatusForbidden}}
		w := response(t, legacyRouter(probe, access), "POST", route.path, route.body, bearer(t))
		if w.Code != http.StatusForbidden || code(t, w) != "CAMPAIGN_ADMIN_REQUIRED" || probe.calls != 1 || access.checks != 2 {
			t.Errorf("%s: a grant withdrawn while waiting must refuse: %d %s, engine %d, checks %d", route.path, w.Code, w.Body.String(), probe.calls, access.checks)
		}
		for i, actor := range access.actors {
			if actor != testWallet || !access.admin[i] {
				t.Errorf("%s: check %d was for %s admin=%v", route.path, i, actor, access.admin[i])
			}
		}
		session := &accessProbe{allowed: 1, refusal: &v2.DomainError{Code: "UNAUTHORIZED", Status: http.StatusUnauthorized}}
		if w = response(t, legacyRouter(&legacyProbe{}, session), "POST", route.path, route.body, bearer(t)); w.Code != http.StatusUnauthorized || code(t, w) != "UNAUTHORIZED" {
			t.Errorf("%s: a session ended while waiting must refuse: %d %s", route.path, w.Code, w.Body.String())
		}
	}
}

func TestLegacyInputsReachTheEngineUnchanged(t *testing.T) {
	probe := &legacyProbe{reviewed: legacyactivation.Review{ID: testReviewID, RunID: testRun, CampaignID: testCampaign, Decision: "import-public",
		AcceptedDifferences: []string{"walletState"}, AcknowledgedFindings: []string{"PARTICIPANT_WALLET_CASE_LOOKUP"}, Reason: "checked", Reviewer: testWallet,
		ClientCommandID: "019aa123-4567-7000-8000-000000000001", DecidedAt: time.Now()}, activated: activation(),
		plan: legacyactivation.Plan{Run: legacyactivation.RunSummary{RunID: testRun, Status: "capturing", Operator: "lg02b2-operator", RetainUntil: time.Now()}}}
	router := legacyRouter(probe, adminOnly(testWallet))
	if w := response(t, router, "POST", reviewPath, reviewBody, bearer(t)); w.Code != http.StatusCreated {
		t.Fatalf("review: %d %s", w.Code, w.Body.String())
	}
	review := probe.review
	if review.RunID != testRun || review.CampaignID != testCampaign || review.Decision != "import-public" || strings.Join(review.AcceptedDifferences, ",") != "walletState" ||
		strings.Join(review.AcknowledgedFindings, ",") != "PARTICIPANT_WALLET_CASE_LOOKUP" || review.Reason != "checked" || review.Reviewer != testWallet ||
		review.ClientCommandID != "019aa123-4567-7000-8000-000000000001" {
		t.Errorf("review input: %+v", review)
	}
	w := response(t, router, "POST", activationPath, activationBody, bearer(t))
	if w.Code != http.StatusCreated {
		t.Fatalf("activation: %d %s", w.Code, w.Body.String())
	}
	in := probe.activation
	if in.RunID != testRun || in.CampaignID != testCampaign || in.Actor != testWallet || in.ClientCommandID != "019aa123-4567-7000-8000-000000000002" ||
		in.Reason != "activate" || in.LegacyRowSHA256 != testDigest || in.InteractionsSHA256 != testDigest || in.RequestID == "" || in.RequestID != w.Header().Get("X-Request-Id") {
		t.Errorf("activation input: %+v", in)
	}
	if w = response(t, router, "GET", planPath+"?cursor=bGVnYWN5", "", bearer(t)); w.Code != http.StatusOK || probe.runID != testRun || probe.cursor != "bGVnYWN5" {
		t.Errorf("plan input: %d %s %q", w.Code, probe.runID, probe.cursor)
	}
	if w = response(t, router, "GET", statusPath, "", bearer(t)); w.Code != http.StatusNotFound || code(t, w) != "CAMPAIGN_LEGACY_NOT_ACTIVATED" || probe.runID != testCampaign {
		t.Errorf("activation status of an unactivated campaign: %d %s", w.Code, w.Body.String())
	}
	calls := probe.calls
	for _, tc := range []struct{ method, path, body, contentType string }{
		{"GET", planPath + "?limit=5", "", ""},
		{"GET", planPath + "?cursor=a&cursor=b", "", ""},
		{"GET", statusPath + "?run=" + testRun, "", ""},
		{"POST", reviewPath, strings.Replace(reviewBody, `"reason"`, `"reviewer":"`+testWallet+`","reason"`, 1), ""},
		{"POST", activationPath, strings.Replace(activationBody, testDigest+`","interactions`, "short\",\"interactions", 1), ""},
		{"POST", activationPath + "?force=true", activationBody, ""},
		{"POST", reviewPath, reviewBody, "text/plain"},
	} {
		var r *httptest.ResponseRecorder
		if tc.contentType == "" {
			r = response(t, router, tc.method, tc.path, tc.body, bearer(t))
		} else {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", tc.contentType)
			req.Header.Set("Authorization", bearer(t))
			r = httptest.NewRecorder()
			router.ServeHTTP(r, req)
		}
		if r.Code != http.StatusBadRequest && r.Code != http.StatusUnsupportedMediaType {
			t.Errorf("%s %s must be refused before the engine: %d %s", tc.method, tc.path, r.Code, r.Body.String())
		}
	}
	if probe.calls != calls {
		t.Fatalf("closed input reached the engine")
	}
}

func activation() legacyactivation.Activation {
	return legacyactivation.Activation{ID: testActivation, CampaignID: testCampaign, RunID: testRun, Decision: "import-unpublished",
		RevisionID: "019aa123-4567-7000-8000-0000000000dd", CommandID: "019aa123-4567-7000-8000-0000000000ee", ClientCommandID: "019aa123-4567-7000-8000-000000000002",
		PayloadHash: testDigest, ActivatedBy: testWallet, ActivatedAt: time.Now(),
		InteractionVersion: 3, ImportedInteractions: 3, Comparison: legacyactivation.Comparison{Instant: "2026-10-09T00:00:00.000Z", Wallets: 3,
			Differences: []legacyactivation.Difference{{Class: "visibility", Field: "visible", Before: "true", After: "false"}}, Classes: []string{"visibility"}, Accepted: []string{"visibility"}}}
}

func TestLegacyResponsesFollowTheContract(t *testing.T) {
	captured := time.Date(2026, 10, 9, 1, 2, 3, 456000000, time.FixedZone("CST", 8*3600))
	probe := &legacyProbe{plan: legacyactivation.Plan{Run: legacyactivation.RunSummary{RunID: testRun, Status: "captured", Operator: "lg02b2-operator", CapturedAt: &captured,
		SourceSHA256: testDigest, Rows: 12, RetainUntil: captured.Add(30 * 24 * time.Hour)}, NextCursor: "bGVnYWN5LWNhbXBhaWduLTE", UnlistedItems: 2,
		Items: []legacyactivation.PlanItem{{CampaignID: testCampaign, Decision: "review-required", LegacyRowSHA256: testDigest, InteractionsSHA256: testDigest, ParticipantRows: 2, ReminderRows: 1,
			Findings: []legacyactivation.FindingSummary{{Kind: "PARTICIPANT_WALLET_CASE_LOOKUP", Severity: "review", Count: 2}}, Fence: &legacyactivation.FenceState{FencedBy: "lg02b2-operator", FencedAt: captured},
			Review: &legacyactivation.Review{ID: testReviewID, RunID: testRun, CampaignID: testCampaign, Decision: "import-public", AcceptedDifferences: []string{}, AcknowledgedFindings: []string{"PARTICIPANT_WALLET_CASE_LOOKUP"},
				Reason: "checked", Reviewer: testWallet, ClientCommandID: "019aa123-4567-7000-8000-000000000001", DecidedAt: captured},
			Activation: &legacyactivation.ActivationSummary{ActivationID: testActivation, Decision: "import-public", ActivatedBy: testWallet, ActivatedAt: captured, Classes: []string{"walletState"}}},
			{CampaignID: "legacy-campaign-2", Decision: "import-public", LegacyRowSHA256: testDigest, InteractionsSHA256: testDigest, Findings: []legacyactivation.FindingSummary{}}}}}
	router := legacyRouter(probe, adminOnly(testWallet))
	w := response(t, router, "GET", planPath, "", bearer(t))
	var plan v2.LegacyImportPlanResponse
	if err := v2.DecodeContractResponse("LegacyImportPlanResponse", w.Body.Bytes(), &plan); w.Code != http.StatusOK || err != nil {
		t.Fatalf("plan: %d %v %s", w.Code, err, w.Body.String())
	}
	first, second := plan.Items[0], plan.Items[1]
	if plan.NextCursor == nil || *plan.NextCursor != "bGVnYWN5LWNhbXBhaWduLTE" || plan.UnlistedItems != 2 || *plan.Run.CapturedAt != "2026-10-08T17:02:03.456Z" ||
		first.Fence == nil || first.Fence.LiftedAt != nil || first.Review == nil || first.Review.ReviewID != testReviewID || first.Activation == nil ||
		strings.Join(first.Activation.Classes, ",") != "walletState" || first.Findings[0].Count != 2 || second.Fence != nil || second.Review != nil || second.Activation != nil {
		t.Errorf("plan view: %s", w.Body.String())
	}

	probe.reviewed = *probe.plan.Items[0].Review
	for _, replayed := range []bool{false, true} {
		probe.reviewed.Replayed, probe.activated = replayed, activation()
		probe.activated.Replayed = replayed
		want := map[bool]int{false: http.StatusCreated, true: http.StatusOK}[replayed]
		w = response(t, router, "POST", reviewPath, reviewBody, bearer(t))
		var review v2.LegacyReviewResponse
		if err := v2.DecodeContractResponse("LegacyReviewResponse", w.Body.Bytes(), &review); w.Code != want || err != nil || review.Replayed != replayed || review.Review.ReviewID != testReviewID {
			t.Errorf("review replayed=%v: %d %v %s", replayed, w.Code, err, w.Body.String())
		}
		w = response(t, router, "POST", activationPath, activationBody, bearer(t))
		var activated v2.LegacyActivationResponse
		if err := v2.DecodeContractResponse("LegacyActivationResponse", w.Body.Bytes(), &activated); w.Code != want || err != nil || activated.Replayed != replayed {
			t.Errorf("activation replayed=%v: %d %v %s", replayed, w.Code, err, w.Body.String())
		}
		view := activated.Activation
		if view.ReviewID != nil || view.PublishCommandID != nil || view.PublicationVersion != "0" || view.InteractionVersion != "3" || len(view.Comparison.Groups) != 1 || view.Comparison.Groups[0].Count != 1 {
			t.Errorf("activation view: %s", w.Body.String())
		}
	}
	probe.found, probe.activated.Replayed = true, false
	if w = response(t, router, "GET", statusPath, "", bearer(t)); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"replayed":false`) {
		t.Errorf("activation status: %d %s", w.Code, w.Body.String())
	}
	// A result the contract does not admit is never sent.
	probe.activated.ID = "not-an-activation-id"
	if w = response(t, router, "GET", statusPath, "", bearer(t)); w.Code != http.StatusServiceUnavailable || code(t, w) != "CAMPAIGN_RESPONSE_INVALID" {
		t.Errorf("an invalid result must not be sent: %d %s", w.Code, w.Body.String())
	}
}

func TestLegacyRefusalsAreBoundedAndDigestOnly(t *testing.T) {
	probe := &legacyProbe{}
	router := legacyRouter(probe, adminOnly(testWallet))
	drift := []legacyimport.Drift{}
	for i := 0; i < 60; i++ {
		if i%2 == 0 {
			drift = append(drift, legacyimport.Drift{Table: "campaign_participants", RowRef: fmt.Sprintf("%016x>%016x", i, i+1000), Change: "changed"})
		} else {
			drift = append(drift, legacyimport.Drift{Table: "launchpad_campaigns", RowRef: fmt.Sprintf("%016x", i), Change: "added"})
		}
	}
	probe.err = &legacyactivation.Error{Code: "CAMPAIGN_LEGACY_DRIFT", Status: http.StatusConflict, Detail: "secret-detail", Drift: drift}
	w := response(t, router, "POST", activationPath, activationBody, bearer(t))
	var refusal v2.LegacyImportRefusal
	if err := v2.DecodeContractResponse("LegacyImportRefusal", w.Body.Bytes(), &refusal); w.Code != http.StatusConflict || err != nil {
		t.Fatalf("drift refusal: %d %v %s", w.Code, err, w.Body.String())
	}
	if refusal.Code != "CAMPAIGN_LEGACY_DRIFT" || len(refusal.Drift) != 50 || !refusal.DriftTruncated || refusal.Comparison != nil || refusal.RequestID != w.Header().Get("X-Request-Id") ||
		refusal.Drift[0].RowRef != "0000000000000000" || refusal.Drift[0].AfterRef == nil || *refusal.Drift[0].AfterRef != "00000000000003e8" || refusal.Drift[1].AfterRef != nil ||
		strings.Contains(w.Body.String(), "secret-detail") {
		t.Errorf("drift refusal body: %s", w.Body.String())
	}

	differences := []legacyactivation.Difference{}
	for i := 0; i < 30; i++ {
		differences = append(differences, legacyactivation.Difference{Class: "walletState", Field: "isParticipating", WalletRef: fmt.Sprintf("%016x", i), Before: "fcbcf165908dd18a", After: "b5bea41b6c623f7c"})
	}
	for i := 0; i < 60; i++ {
		differences = append(differences, legacyactivation.Difference{Class: "field", Field: fmt.Sprintf("field%02d", i), Before: "absent", After: fmt.Sprintf("%016x", i)})
	}
	probe.err = &legacyactivation.Error{Code: "CAMPAIGN_LEGACY_COMPARISON_FAILED", Status: http.StatusConflict, Detail: "secret-detail",
		Comparison: &legacyactivation.Comparison{Instant: "2026-10-09T00:00:00.000Z", Wallets: 31, Differences: differences, Classes: []string{"field", "walletState"}, Accepted: []string{"walletState"}}}
	w = response(t, router, "POST", activationPath, activationBody, bearer(t))
	refusal = v2.LegacyImportRefusal{}
	if err := v2.DecodeContractResponse("LegacyImportRefusal", w.Body.Bytes(), &refusal); w.Code != http.StatusConflict || err != nil || refusal.Comparison == nil {
		t.Fatalf("comparison refusal: %d %v %s", w.Code, err, w.Body.String())
	}
	groups := refusal.Comparison.Groups
	if len(groups) != 50 || !refusal.Comparison.GroupsTruncated || groups[0].Count != 30 || len(groups[0].WalletRefs) != 20 || groups[1].Count != 1 || len(groups[1].WalletRefs) != 0 ||
		len(refusal.Drift) != 0 || refusal.DriftTruncated || strings.Contains(w.Body.String(), "secret-detail") {
		t.Errorf("comparison refusal body: %s", w.Body.String())
	}

	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{&legacyactivation.Error{Code: "CAMPAIGN_LEGACY_PLAN_CHANGED", Status: http.StatusConflict, Detail: "secret-detail"}, http.StatusConflict, "CAMPAIGN_LEGACY_PLAN_CHANGED"},
		{&legacyactivation.Error{Code: "CAMPAIGN_LEGACY_NOT_FOUND", Status: http.StatusNotFound, Detail: "secret-detail"}, http.StatusNotFound, "CAMPAIGN_LEGACY_NOT_FOUND"},
		{&legacyactivation.Error{Code: "CAMPAIGN_DATA_UNAVAILABLE", Status: http.StatusServiceUnavailable, Detail: "secret-detail (SQLSTATE 08006)"}, http.StatusServiceUnavailable, "CAMPAIGN_DATA_UNAVAILABLE"},
		{errors.New("secret-detail postgres://user:" + strings.Repeat("fixture", 4) + "@db/campaign"), http.StatusServiceUnavailable, "CAMPAIGN_UNAVAILABLE"},
	} {
		probe.err = tc.err
		for _, route := range legacyRoutes {
			w = response(t, router, route.method, route.path, route.body, bearer(t))
			if w.Code != tc.status || code(t, w) != tc.code || strings.Contains(w.Body.String(), "secret-detail") {
				t.Errorf("%s %s with %v: %d %s", route.method, route.path, tc.err, w.Code, w.Body.String())
			}
		}
	}
}
