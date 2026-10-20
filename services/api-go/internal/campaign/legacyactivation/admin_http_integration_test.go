//go:build integration

package legacyactivation_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth/authtest"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/legacyactivation"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/legacyimport"
	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/application"
	campaignhttp "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/http"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/postgres"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// adminHTTP serves the administration routes through the real router,
// verifier, authorizer and engine (protocol §5). Sessions are in memory so a
// test can end one, or take the store down, while a request waits.
type adminHTTP struct {
	server        *httptest.Server
	credentials   *authtest.Fixture
	mu            sync.Mutex
	admins        []string
	admin, member string
	tokens        map[string]string
	sessions      map[string]string
}

func randomWallet(t *testing.T) string {
	t.Helper()
	raw := make([]byte, 20)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	return "0x" + hex.EncodeToString(raw)
}

func newAdminHTTP(t *testing.T, f fixture) *adminHTTP {
	t.Helper()
	h := &adminHTTP{credentials: authtest.New(t), admin: randomWallet(t), member: randomWallet(t), tokens: map[string]string{}, sessions: map[string]string{}}
	h.admins = []string{h.admin}
	authorizer := campaignhttp.Authorizer{Verifier: h.credentials.Verifier, Admins: func() []string {
		h.mu.Lock()
		defer h.mu.Unlock()
		return append([]string(nil), h.admins...)
	}}
	store := postgres.New(f.pool)
	router := chi.NewRouter()
	router.Mount("/api/v2", campaignhttp.Router(campaignhttp.Config{Enabled: true, Verifier: h.credentials.Verifier,
		Commands: application.NewService(store, authorizer, caps), Queries: application.NewQueryService(store, authorizer, caps), Legacy: f.engine, Access: authorizer}))
	h.server = httptest.NewServer(router)
	t.Cleanup(h.server.Close)
	h.login(t, h.admin)
	h.login(t, h.member)
	return h
}

func (h *adminHTTP) login(t *testing.T, wallet string) {
	t.Helper()
	h.tokens[wallet], h.sessions[wallet] = h.credentials.Login(t, wallet, 11155111)
}

func (h *adminHTTP) setAdmins(wallets ...string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.admins = wallets
}

// do sends one request with token as the bearer credential ("" for none).
func (h *adminHTTP) do(method, path, token string, body any) (int, []byte, error) {
	var payload io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		payload = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, h.server.URL+"/api/v2"+path, payload)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := h.server.Client().Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	return response.StatusCode, raw, err
}

func (h *adminHTTP) call(t *testing.T, method, path, token string, body any) (int, []byte) {
	t.Helper()
	status, raw, err := h.do(method, path, token, body)
	if err != nil {
		t.Fatal(err)
	}
	return status, raw
}

func errorCode(t *testing.T, raw []byte) string {
	t.Helper()
	var value v2.ErrorResponse
	if err := v2.DecodeContractResponse("ErrorResponse", raw, &value); err != nil {
		t.Fatalf("error envelope: %v %s", err, raw)
	}
	return value.Code
}

func decodeAs[T any](t *testing.T, name string, status, want int, raw []byte) T {
	t.Helper()
	var value T
	if err := v2.DecodeContractResponse(name, raw, &value); status != want || err != nil {
		t.Fatalf("%s: status %d want %d, %v: %s", name, status, want, err, raw)
	}
	return value
}

func planPath(run string) string           { return "/campaign-admin/legacy-imports/runs/" + run }
func reviewPath(run, id string) string     { return planPath(run) + "/campaigns/" + id + "/review" }
func activationPath(run, id string) string { return planPath(run) + "/campaigns/" + id + "/activation" }
func statusPath(id string) string {
	return "/campaign-admin/legacy-imports/campaigns/" + id + "/activation"
}

func (f fixture) activationBody(t *testing.T, id string) map[string]any {
	t.Helper()
	_, row, interactions := f.decision(t, id)
	return map[string]any{"clientCommandId": commandID(t), "reason": "LG-02b2 activation over HTTP", "legacyRowSha256": row, "interactionsSha256": interactions}
}

// planOf reads every page of a run's plan through the route.
func (h *adminHTTP) planOf(t *testing.T, run string) (map[string]v2.LegacyPlanItemView, v2.LegacyImportPlanResponse, int) {
	t.Helper()
	items := map[string]v2.LegacyPlanItemView{}
	cursor, pages := "", 0
	for {
		path := planPath(run)
		if cursor != "" {
			path += "?cursor=" + cursor
		}
		status, raw := h.call(t, "GET", path, h.tokens[h.admin], nil)
		page := decodeAs[v2.LegacyImportPlanResponse](t, "LegacyImportPlanResponse", status, http.StatusOK, raw)
		pages++
		for _, item := range page.Items {
			if _, seen := items[item.CampaignID]; seen {
				t.Fatalf("plan item %s listed twice", item.CampaignID)
			}
			items[item.CampaignID] = item
		}
		if page.NextCursor == nil {
			return items, page, pages
		}
		if len(page.Items) != legacyactivation.PlanPageSize {
			t.Fatalf("a page before the last holds %d items", len(page.Items))
		}
		cursor = *page.NextCursor
	}
}

// reviewKinds are the review-level finding kinds a review must acknowledge,
// read from the plan the way an operator reads them.
func reviewKinds(item v2.LegacyPlanItemView) []string {
	kinds := []string{}
	for _, finding := range item.Findings {
		if finding.Severity == legacyimport.SeverityReview {
			kinds = append(kinds, finding.Kind)
		}
	}
	sort.Strings(kinds)
	return kinds
}

// The administration routes over the real router and database: the plan,
// reviews, activations, replays and refusals.
func TestAdministrationOverHTTP(t *testing.T) {
	pool := legacyPool(t)
	ctx := context.Background()
	// A legacy ID longer than 160 bytes can be neither fenced nor activated;
	// the plan counts it instead of listing it.
	long := "lg02b2-long-" + strings.Repeat("x", 170)
	exec(t, pool, `INSERT INTO launchpad_campaigns(id,title,start_date,end_date,status,participants,updated_at)
		VALUES ($1,'Long ID','2020-01-01','2999-01-01','active',0,now()) ON CONFLICT (id) DO NOTHING`, long)
	f := seed(t)
	h := newAdminHTTP(t, f)
	admin, member := h.tokens[h.admin], h.tokens[h.member]

	if status, raw := h.call(t, "GET", planPath(f.run), "", nil); status != http.StatusUnauthorized || errorCode(t, raw) != "UNAUTHORIZED" {
		t.Errorf("anonymous plan: %d %s", status, raw)
	}
	if status, raw := h.call(t, "GET", planPath(f.run), member, nil); status != http.StatusForbidden || errorCode(t, raw) != "CAMPAIGN_ADMIN_REQUIRED" {
		t.Errorf("non-administrator plan: %d %s", status, raw)
	}

	items, last, pages := h.planOf(t, f.run)
	listed := f.count(t, `SELECT count(*) FROM campaign_legacy_import_items WHERE run_id=$1 AND octet_length(campaign_id) <= 160`, f.run)
	unlisted := f.count(t, `SELECT count(*) FROM campaign_legacy_import_items WHERE run_id=$1 AND octet_length(campaign_id) > 160`, f.run)
	if _, shown := items[long]; len(items) != listed || int(last.UnlistedItems) != unlisted || unlisted < 1 || shown {
		t.Errorf("plan lists %d of %d items, counts %d of %d unlisted, long ID shown=%v", len(items), listed, last.UnlistedItems, unlisted, shown)
	}
	t.Logf("plan: %d items over %d pages, %d unlisted", len(items), pages, last.UnlistedItems)
	for _, id := range []string{f.public, f.mixed, f.dup, f.hidden, f.draft} {
		item := items[id]
		decision, row, interactions := f.decision(t, id)
		if item.Decision != decision || item.LegacyRowSha256 != row || item.InteractionsSha256 != interactions || item.Fence == nil || item.Fence.LiftedAt != nil ||
			item.Fence.FencedBy != operator || item.Review != nil || item.Activation != nil {
			t.Errorf("plan item %s: %+v", id, item)
		}
		var raw []byte
		if err := f.pool.QueryRow(ctx, `SELECT findings FROM campaign_legacy_import_items WHERE run_id=$1 AND campaign_id=$2`, f.run, id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var stored []legacyimport.Finding
		if err := json.Unmarshal(raw, &stored); err != nil {
			t.Fatal(err)
		}
		want, got := map[string]int64{}, map[string]int64{}
		for _, finding := range stored {
			want[finding.Kind+"/"+finding.Severity]++
		}
		for _, summary := range item.Findings {
			got[summary.Kind+"/"+summary.Severity] += summary.Count
		}
		if !reflect.DeepEqual(want, got) {
			t.Errorf("plan item %s findings: summary %v, stored %v", id, got, want)
		}
	}
	for path, want := range map[string]string{
		planPath(f.run) + "?cursor=%21%21": "CAMPAIGN_LEGACY_INVALID_INPUT",
		planPath(f.run) + "?cursor=" + base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("z", 161))): "CAMPAIGN_LEGACY_INVALID_INPUT",
		planPath(f.run) + "?limit=5":                     "CAMPAIGN_INVALID_INPUT",
		planPath("not-a-run"):                            "CAMPAIGN_LEGACY_INVALID_INPUT",
		planPath("019aa123-4567-7000-8000-00000000ffff"): "CAMPAIGN_LEGACY_NOT_FOUND",
	} {
		status, raw := h.call(t, "GET", path, admin, nil)
		if code := errorCode(t, raw); code != want || (status != http.StatusBadRequest && status != http.StatusNotFound) {
			t.Errorf("GET %s: %d %s", path, status, code)
		}
	}

	// import-public over HTTP keeps every legacy answer.
	before := map[string]string{}
	for _, wallet := range []string{"", walletA, walletB, outsider} {
		before[wallet] = f.answer(t, f.public, wallet)
	}
	body := f.activationBody(t, f.public)
	if status, raw := h.call(t, "POST", activationPath(f.run, f.public), member, body); status != http.StatusForbidden || errorCode(t, raw) != "CAMPAIGN_ADMIN_REQUIRED" {
		t.Errorf("non-administrator activation: %d %s", status, raw)
	}
	status, raw := h.call(t, "POST", activationPath(f.run, f.public), admin, body)
	first := decodeAs[v2.LegacyActivationResponse](t, "LegacyActivationResponse", status, http.StatusCreated, raw).Activation
	t.Logf("import-public over HTTP: %s", raw)
	if first.Decision != "import-public" || first.PublicationVersion != "1" || first.ImportedInteractions != 3 || first.ActivatedBy != h.admin || first.ReviewID != nil ||
		first.PublishCommandID == nil || len(first.Comparison.Classes) != 0 || len(first.Comparison.Groups) != 0 || first.Comparison.Wallets != 3 {
		t.Errorf("activation view: %s", raw)
	}
	// The record names the request that made it, so a client can recognize it.
	payload, err := v2.LegacyActivationPayloadHash(f.run, f.public, v2.LegacyActivationCommandInput{ClientCommandID: body["clientCommandId"].(string),
		Reason: body["reason"].(string), LegacyRowSha256: body["legacyRowSha256"].(string), InteractionsSha256: body["interactionsSha256"].(string)})
	if err != nil || first.ClientCommandID != body["clientCommandId"] || first.PayloadHash != payload {
		t.Errorf("activation identity: %s/%s, want %s/%s %v", first.ClientCommandID, first.PayloadHash, body["clientCommandId"], payload, err)
	}
	for wallet, want := range before {
		if got := f.answer(t, f.public, wallet); got != want {
			t.Errorf("legacy answer for %q changed:\nbefore %s\nafter  %s", wallet, want, got)
		}
	}
	status, raw = h.call(t, "POST", activationPath(f.run, f.public), admin, body)
	replayed := decodeAs[v2.LegacyActivationResponse](t, "LegacyActivationResponse", status, http.StatusOK, raw)
	if !replayed.Replayed || replayed.Activation.ActivationID != first.ActivationID || replayed.Activation.CommandID != first.CommandID {
		t.Errorf("activation replay: %s", raw)
	}
	if status, raw = h.call(t, "POST", activationPath(f.run, f.public), admin, f.activationBody(t, f.public)); status != http.StatusConflict || errorCode(t, raw) != "CAMPAIGN_LEGACY_ALREADY_ACTIVATED" {
		t.Errorf("another command meets an activated campaign: %d %s", status, raw)
	}
	status, raw = h.call(t, "GET", statusPath(f.public), admin, nil)
	if recorded := decodeAs[v2.LegacyActivationResponse](t, "LegacyActivationResponse", status, http.StatusOK, raw); recorded.Replayed || recorded.Activation.ActivationID != first.ActivationID ||
		recorded.Activation.ClientCommandID != first.ClientCommandID || recorded.Activation.PayloadHash != first.PayloadHash {
		t.Errorf("activation status: %s", raw)
	}
	if status, raw = h.call(t, "GET", statusPath(f.mixed), admin, nil); status != http.StatusNotFound || errorCode(t, raw) != "CAMPAIGN_LEGACY_NOT_ACTIVATED" {
		t.Errorf("status of an unactivated campaign: %d %s", status, raw)
	}

	// A review over HTTP: replay, conflicting reuse of its key, then the
	// activation it allows.
	review := map[string]any{"clientCommandId": commandID(t), "decision": "import-public", "acceptedDifferences": []string{"walletState"},
		"acknowledgedFindings": reviewKinds(items[f.mixed]), "reason": "checksum wallet becomes visible"}
	if status, raw = h.call(t, "POST", reviewPath(f.run, f.mixed), member, review); status != http.StatusForbidden || errorCode(t, raw) != "CAMPAIGN_ADMIN_REQUIRED" {
		t.Errorf("non-administrator review: %d %s", status, raw)
	}
	if n := f.count(t, `SELECT count(*) FROM campaign_legacy_review_decisions WHERE run_id=$1 AND campaign_id=$2`, f.run, f.mixed); n != 0 {
		t.Fatalf("a refused review wrote %d decisions", n)
	}
	status, raw = h.call(t, "POST", reviewPath(f.run, f.mixed), admin, review)
	recorded := decodeAs[v2.LegacyReviewResponse](t, "LegacyReviewResponse", status, http.StatusCreated, raw).Review
	if recorded.Reviewer != h.admin || recorded.Decision != "import-public" || strings.Join(recorded.AcknowledgedFindings, ",") != "PARTICIPANT_WALLET_CASE_LOOKUP" {
		t.Errorf("review view: %s", raw)
	}
	status, raw = h.call(t, "POST", reviewPath(f.run, f.mixed), admin, review)
	if again := decodeAs[v2.LegacyReviewResponse](t, "LegacyReviewResponse", status, http.StatusOK, raw); !again.Replayed || again.Review.ReviewID != recorded.ReviewID {
		t.Errorf("review replay: %s", raw)
	}
	review["reason"] = "a different reason"
	if status, raw = h.call(t, "POST", reviewPath(f.run, f.mixed), admin, review); status != http.StatusConflict || errorCode(t, raw) != "CAMPAIGN_LEGACY_IDEMPOTENCY_CONFLICT" {
		t.Errorf("one command key records one review: %d %s", status, raw)
	}
	status, raw = h.call(t, "POST", activationPath(f.run, f.mixed), admin, f.activationBody(t, f.mixed))
	mixed := decodeAs[v2.LegacyActivationResponse](t, "LegacyActivationResponse", status, http.StatusCreated, raw).Activation
	t.Logf("checksum wallet over HTTP: %s", raw)
	refs := []string{}
	for _, group := range mixed.Comparison.Groups {
		if group.Class != legacyactivation.ClassWalletState {
			t.Errorf("unexpected group %+v", group)
		}
		refs = append(refs, group.WalletRefs...)
	}
	if mixed.ReviewID == nil || *mixed.ReviewID != recorded.ReviewID || strings.Join(mixed.Comparison.Classes, ",") != "walletState" ||
		strings.Join(refs, ",") != legacyimport.WalletRef(mixedC) {
		t.Errorf("checksum wallet activation: %s", raw)
	}

	// A failed comparison: the grouped report, and nothing written.
	review = map[string]any{"clientCommandId": commandID(t), "decision": "import-public", "acceptedDifferences": []string{},
		"acknowledgedFindings": reviewKinds(items[f.dup]), "reason": "duplicates reviewed"}
	status, raw = h.call(t, "POST", reviewPath(f.run, f.dup), admin, review)
	decodeAs[v2.LegacyReviewResponse](t, "LegacyReviewResponse", status, http.StatusCreated, raw)
	status, raw = h.call(t, "POST", activationPath(f.run, f.dup), admin, f.activationBody(t, f.dup))
	refusal := decodeAs[v2.LegacyImportRefusal](t, "LegacyImportRefusal", status, http.StatusConflict, raw)
	t.Logf("refused case duplicate over HTTP: %s", raw)
	if refusal.Code != "CAMPAIGN_LEGACY_COMPARISON_FAILED" || refusal.Comparison == nil || strings.Join(refusal.Comparison.Classes, ",") != "participantCount" ||
		len(refusal.Comparison.Groups) == 0 || refusal.Comparison.Groups[0].Class != "participantCount" || len(refusal.Drift) != 0 {
		t.Errorf("comparison refusal: %s", raw)
	}
	if f.count(t, `SELECT count(*) FROM campaign_definitions WHERE id=$1`, f.dup)+f.count(t, `SELECT count(*) FROM campaign_legacy_activations WHERE campaign_id=$1`, f.dup) != 0 {
		t.Error("a failed comparison must write nothing")
	}

	// Digests that differ from the reviewed plan item are refused.
	stale := f.activationBody(t, f.draft)
	stale["legacyRowSha256"] = strings.Repeat("0", 64)
	if status, raw = h.call(t, "POST", activationPath(f.run, f.draft), admin, stale); status != http.StatusConflict || errorCode(t, raw) != "CAMPAIGN_LEGACY_PLAN_CHANGED" {
		t.Errorf("a changed plan: %d %s", status, raw)
	}

	items, _, _ = h.planOf(t, f.run)
	if a := items[f.public].Activation; a == nil || a.ActivationID != first.ActivationID || items[f.mixed].Review == nil || items[f.mixed].Review.ReviewID != recorded.ReviewID ||
		items[f.dup].Activation != nil || items[f.dup].Review == nil {
		t.Errorf("the plan shows reviews and activations: %+v / %+v / %+v", items[f.public], items[f.mixed], items[f.dup])
	}
}

// Drift since the capture is refused over HTTP with its row references.
func TestDriftRefusalOverHTTP(t *testing.T) {
	pool := legacyPool(t)
	ctx := context.Background()
	random := make([]byte, 6)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	id := "lg02b2-drift-" + hex.EncodeToString(random)
	exec(t, pool, `INSERT INTO launchpad_campaigns(id,title,start_date,end_date,status,participants,updated_at) VALUES ($1,'Drift','2020-01-01','2999-01-01','active',1,now())`, id)
	exec(t, pool, `INSERT INTO campaign_participants(id,campaign_id,wallet_address,joined_at) VALUES ($1,$2,$3,'2020-01-15')`, id+"-p1", id, walletA)
	captured, err := legacyimport.Capture(ctx, pool, legacyimport.CaptureOptions{Operator: operator})
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{pool: pool, run: captured.RunID, engine: legacyactivation.New(pool, caps)}
	body := f.activationBody(t, id)
	exec(t, pool, `UPDATE launchpad_campaigns SET title='Drifted' WHERE id=$1`, id)
	exec(t, pool, `DELETE FROM campaign_participants WHERE id=$1`, id+"-p1")
	if err = legacyimport.Fence(ctx, pool, f.run, []string{id}, operator); err != nil {
		t.Fatal(err)
	}
	h := newAdminHTTP(t, f)
	status, raw := h.call(t, "POST", activationPath(f.run, id), h.tokens[h.admin], body)
	refusal := decodeAs[v2.LegacyImportRefusal](t, "LegacyImportRefusal", status, http.StatusConflict, raw)
	t.Logf("drift over HTTP: %s", raw)
	changes := map[string]v2.LegacyDriftView{}
	for _, drift := range refusal.Drift {
		changes[drift.Table+"/"+drift.Change] = drift
	}
	changed, removed := changes["launchpad_campaigns/changed"], changes["campaign_participants/removed"]
	if refusal.Code != "CAMPAIGN_LEGACY_DRIFT" || len(refusal.Drift) != 2 || refusal.DriftTruncated || refusal.Comparison != nil ||
		changed.AfterRef == nil || *changed.AfterRef == changed.RowRef || removed.RowRef == "" || removed.AfterRef != nil {
		t.Errorf("drift refusal: %s", raw)
	}
	if f.count(t, `SELECT count(*) FROM campaign_definitions WHERE id=$1`, id) != 0 {
		t.Error("a refused activation must write nothing")
	}
}

// lockWaiters counts backends waiting for the advisory lock named by key.
func lockWaiters(t *testing.T, f fixture, key string) int {
	t.Helper()
	return f.count(t, `WITH k AS (SELECT hashtextextended($1, 0) AS key)
		SELECT count(*) FROM pg_locks l, k WHERE l.locktype='advisory' AND NOT l.granted AND l.objsubid=1
		AND l.classid::bigint = ((k.key >> 32) & 4294967295) AND l.objid::bigint = (k.key & 4294967295)`, key)
}

// waitForFenceLock waits until a backend waits for the campaign's fence lock.
func waitForFenceLock(t *testing.T, f fixture, id string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for lockWaiters(t, f, "campaign-legacy-fence:"+id) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the activation never waited for the fence lock")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// An activation checks the session and grant again once it holds the fence
// lock: a logout, a removed grant or an unavailable session store while it
// waits refuses it, and nothing is written.
func TestActivationReauthorizesAfterTheFenceLock(t *testing.T) {
	f := seed(t)
	h := newAdminHTTP(t, f)
	ctx := context.Background()
	type outcome struct {
		status int
		raw    []byte
		err    error
	}
	for _, tc := range []struct {
		name   string
		end    func()
		status int
		code   string
	}{
		{"logout while waiting", func() { h.credentials.Sessions.RevokeID(h.sessions[h.admin]) }, http.StatusUnauthorized, "UNAUTHORIZED"},
		{"administrator removed while waiting", func() { h.setAdmins() }, http.StatusForbidden, "CAMPAIGN_ADMIN_REQUIRED"},
		{"session store down while waiting", func() { h.credentials.Sessions.SetDown(true) }, http.StatusServiceUnavailable, "WEB3_SESSION_UNAVAILABLE"},
	} {
		h.credentials.Sessions.SetDown(false)
		h.setAdmins(h.admin)
		h.login(t, h.admin)
		token, body := h.tokens[h.admin], f.activationBody(t, f.public)
		holder, err := f.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = legacyimport.VerifyInTx(ctx, holder, f.run, f.public); err != nil {
			t.Fatal(err)
		}
		done := make(chan outcome, 1)
		go func() {
			status, raw, err := h.do("POST", activationPath(f.run, f.public), token, body)
			done <- outcome{status, raw, err}
		}()
		waitForFenceLock(t, f, f.public)
		tc.end()
		if err = holder.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		got := <-done
		if got.err != nil {
			t.Fatal(got.err)
		}
		if got.status != tc.status || errorCode(t, got.raw) != tc.code {
			t.Errorf("%s: %d %s", tc.name, got.status, got.raw)
		}
		if n := f.count(t, `SELECT count(*) FROM campaign_legacy_activations WHERE campaign_id=$1`, f.public) + f.count(t, `SELECT count(*) FROM campaign_definitions WHERE id=$1`, f.public); n != 0 {
			t.Fatalf("%s: a refused activation wrote %d rows", tc.name, n)
		}
	}
	h.credentials.Sessions.SetDown(false)
	h.setAdmins(h.admin)
	h.login(t, h.admin)
	status, raw := h.call(t, "POST", activationPath(f.run, f.public), h.tokens[h.admin], f.activationBody(t, f.public))
	decodeAs[v2.LegacyActivationResponse](t, "LegacyActivationResponse", status, http.StatusCreated, raw)
}
