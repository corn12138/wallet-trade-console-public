//go:build integration

package campaignhttp_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth/authtest"
	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/application"
	campaignhttp "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/http"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/postgres"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/web3session"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type realHTTPFixture struct {
	server              *httptest.Server
	pool                *pgxpool.Pool
	admin, owner, other string
	tokens              map[string]string
}

func httpID(t *testing.T) string {
	t.Helper()
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatal(err)
	}
	millis := time.Now().UnixMilli()
	for i := 5; i >= 0; i-- {
		raw[i] = byte(millis)
		millis >>= 8
	}
	raw[6] = (raw[6] & 15) | 0x70
	raw[8] = (raw[8] & 63) | 0x80
	value := hex.EncodeToString(raw[:])
	return value[:8] + "-" + value[8:12] + "-" + value[12:16] + "-" + value[16:20] + "-" + value[20:]
}
func httpWallet(t *testing.T) string {
	return "0x" + strings.ReplaceAll(httpID(t), "-", "") + "00000000"
}
func newRealHTTP(t *testing.T) *realHTTPFixture {
	t.Helper()
	return newRealHTTPWith(t, func() v2.Capabilities { return v2.Capabilities{Templates: map[string]bool{"campaignCardV1": true}} })
}

// newRealHTTPWith serves the real router with a caller-controlled capability
// source, so a test can withdraw a registry entry between requests.
func newRealHTTPWith(t *testing.T, capabilities func() v2.Capabilities) *realHTTPFixture {
	t.Helper()
	url := os.Getenv("CAMPAIGN_TEST_DATABASE_URL")
	if url == "" {
		t.Fatal("explicit disposable campaign database required")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var name string
	if err = pool.QueryRow(context.Background(), `SELECT current_database()`).Scan(&name); err != nil || (!strings.HasPrefix(name, "campaign_test") && !strings.HasPrefix(name, "migration_probe")) {
		t.Fatal("disposable campaign database required")
	}
	fixture := &realHTTPFixture{pool: pool, admin: httpWallet(t), owner: httpWallet(t), other: httpWallet(t), tokens: map[string]string{}}
	// Sessions live in the disposable database, exactly as in cmd/api.
	credentials := authtest.New(t, func(p *auth.Policy) { p.Sessions = web3session.New(pool) })
	verifier := credentials.Verifier
	authorizer := campaignhttp.Authorizer{Verifier: verifier, Admins: func() []string { return []string{fixture.admin} }}
	store := postgres.New(pool)
	router := chi.NewRouter()
	router.Mount("/api/v2", campaignhttp.Router(campaignhttp.Config{Enabled: true, Verifier: verifier, Commands: application.NewService(store, authorizer, capabilities), Queries: application.NewQueryService(store, authorizer, capabilities)}))
	fixture.server = httptest.NewServer(router)
	t.Cleanup(fixture.server.Close)
	for _, actor := range []string{fixture.admin, fixture.owner, fixture.other} {
		fixture.tokens[actor], _ = credentials.Login(t, actor, 11155111)
	}
	return fixture
}
func (f *realHTTPFixture) request(t *testing.T, method, path, actor string, body any, want int) []byte {
	t.Helper()
	var raw []byte
	var err error
	if body != nil {
		raw, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequest(method, f.server.URL+"/api/v2"+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if actor != "" {
		req.Header.Set("Authorization", "Bearer "+f.tokens[actor])
	}
	response, err := f.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 1024*1024+1))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != want {
		t.Fatalf("%s %s: status %d want %d: %s", method, path, response.StatusCode, want, data)
	}
	if !strings.Contains(response.Header.Get("Cache-Control"), "no-store") {
		t.Fatal("campaign response is cacheable")
	}
	if actor != "" && !strings.Contains(response.Header.Get("Cache-Control"), "private") {
		t.Fatal("private campaign response lacks private cache policy")
	}
	if response.Header.Get("X-Request-Id") == "" {
		t.Fatal("missing request correlation")
	}
	return data
}
func (f *realHTTPFixture) command(t *testing.T, method, path, actor string, body any, status int) v2.CommandResponse {
	t.Helper()
	raw := f.request(t, method, path, actor, body, status)
	var result v2.CommandResponse
	if err := v2.DecodeContractResponse("CommandResponse", raw, &result); err != nil {
		t.Fatalf("command wire contract: %v", err)
	}
	if result.ServerNow != result.AsOf {
		t.Fatal("command clocks did not share snapshot")
	}
	return result
}

// adminCommand decodes administrator responses, which carry the ledger
// identity (AdminCommandResponse) instead of the participant shape.
func (f *realHTTPFixture) adminCommand(t *testing.T, method, path string, body any, status int) v2.AdminCommandResponse {
	t.Helper()
	raw := f.request(t, method, path, f.admin, body, status)
	var result v2.AdminCommandResponse
	if err := v2.DecodeContractResponse("AdminCommandResponse", raw, &result); err != nil {
		t.Fatalf("admin command wire contract: %v", err)
	}
	if result.ServerNow != result.AsOf || result.Command.Actor != f.admin {
		t.Fatal("admin command clocks or actor invalid")
	}
	return result
}
func errorCode(t *testing.T, raw []byte, want string) {
	t.Helper()
	var value struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(raw, &value) != nil || value.Code != want {
		t.Fatalf("error code want %s: %s", want, raw)
	}
}
func httpInput() v2.RevisionInput {
	copy := v2.LocalizedCopy{Title: "HTTP campaign", Description: "Signed principal integration", Rules: "Wallet registration", RewardDescription: "Display only"}
	return v2.RevisionInput{SchemaVersion: 1, TermsCanonicalVersion: 1, TemplateID: "campaignCardV1", DisplayContent: v2.DisplayContent{Layout: "compact", SortOrder: 88}, MaterialTerms: v2.MaterialTerms{Copy: v2.Translations{En: copy, Zh: copy}, StartAt: time.Now().Add(-time.Hour).UTC().Format("2006-01-02T15:04:05Z"), EndAt: time.Now().Add(time.Hour).UTC().Format("2006-01-02T15:04:05Z"), Rules: v2.RuleSet{Join: v2.JoinRule{Kind: "walletJoinV1", Version: 1}, Reminder: v2.ReminderRule{Kind: "reminderIntentV1", Version: 1}}, RewardDelivery: "display-only"}, Cta: v2.CTA{ActionKind: "none"}}
}
func (f *realHTTPFixture) create(t *testing.T) v2.AdminCommandResponse {
	return f.adminCommand(t, "POST", "/campaign-admin/campaigns", v2.CreateCommandInput{ClientCommandID: httpID(t), Reason: "HTTP fixture", Revision: httpInput()}, 201)
}
func (f *realHTTPFixture) publish(t *testing.T, created v2.AdminCommandResponse) v2.AdminCommandResponse {
	return f.adminCommand(t, "POST", "/campaign-admin/campaigns/"+created.Original.CampaignID+"/publications", v2.PublicationCommandInput{ClientCommandID: httpID(t), Reason: "HTTP publish", Operation: "publish", ExpectedPublicationVersion: "0", CandidateRevisionID: created.Original.RevisionID}, 200)
}
func TestHTTPCampaignSignedLifecycleAndReconciliation(t *testing.T) {
	f := newRealHTTP(t)
	created := f.create(t)
	id := created.Original.CampaignID
	if created.Replayed || created.Original.RevisionID == nil || created.Current.RevisionID != nil {
		t.Fatal("create did not preserve candidate identity")
	}
	errorCode(t, f.request(t, "GET", "/campaigns/"+id, "", nil, 404), "CAMPAIGN_NOT_FOUND")
	previewPath := "/campaign-admin/campaigns/" + id + "/revisions/" + *created.Original.RevisionID + "/preview"
	raw := f.request(t, "GET", previewPath, f.admin, nil, 200)
	var preview v2.QueryPreviewResponse
	if err := v2.DecodeContractResponse("QueryPreviewResponse", raw, &preview); err != nil || preview.Item.IsPublished || preview.Item.ContentState != "ready" {
		t.Fatalf("draft preview invalid: %v", err)
	}
	errorCode(t, f.request(t, "GET", previewPath, f.owner, nil, 403), "CAMPAIGN_ADMIN_REQUIRED")
	published := f.publish(t, created)
	if published.Current.PublicationVersion != "1" {
		t.Fatal("publication did not advance")
	}
	raw = f.request(t, "GET", "/campaigns/"+id, "", nil, 200)
	var detail v2.QueryDetailResponse
	if err := v2.DecodeContractResponse("QueryDetailResponse", raw, &detail); err != nil || detail.Item.ContentState != "ready" || detail.Item.EffectivePhase != "active" {
		t.Fatalf("published detail invalid: %v", err)
	}
	join := v2.PositiveInteractionInput{ClientCommandID: httpID(t), ExpectedStateVersion: "0", ExpectedPublicationVersion: "1"}
	path := "/campaigns/" + id + "/join"
	first := f.command(t, "POST", path, f.owner, join, 200)
	retry := f.command(t, "POST", path, f.owner, join, 200)
	if first.Replayed || !retry.Replayed || first.Original.State == nil || retry.Current.State.StateVersion != "1" || retry.Original.State.Status != "joined" {
		t.Fatal("same-key join was executed twice")
	}
	left := f.command(t, "DELETE", path, f.owner, v2.CancelInteractionInput{ClientCommandID: httpID(t), ExpectedStateVersion: "1"}, 200)
	if left.Current.State.Status != "left" || left.Current.State.StateVersion != "2" {
		t.Fatal("leave did not retain version")
	}
	old := f.command(t, "POST", path, f.owner, join, 200)
	if !old.Replayed || old.Original.State.Status != "joined" || old.Current.State.Status != "left" || old.Current.State.StateVersion != "2" {
		t.Fatal("old join retry resurrected membership")
	}
	lookup := f.command(t, "GET", "/campaigns/"+id+"/commands/"+join.ClientCommandID+"?operation=join", f.owner, nil, 200)
	if !lookup.Replayed || lookup.Original.State.Status != "joined" || lookup.Current.State.Status != "left" {
		t.Fatal("GET recovery lost original/current distinction")
	}
	errorCode(t, f.request(t, "POST", path, f.owner, v2.PositiveInteractionInput{ClientCommandID: httpID(t), ExpectedStateVersion: "0", ExpectedPublicationVersion: "1"}, 409), "CAMPAIGN_STATE_CONFLICT")
	raw = f.request(t, "GET", "/campaigns/me?ids="+id, f.other, nil, 200)
	var me v2.QueryMeResponse
	if err := v2.DecodeContractResponse("QueryMeResponse", raw, &me); err != nil || me.Items[0].Participation.State != "absent" || *me.Items[0].Participation.StateVersion != "0" {
		t.Fatalf("other owner read leaked state: %v", err)
	}
	errorCode(t, f.request(t, "GET", "/campaigns/"+id+"/commands/"+join.ClientCommandID+"?operation=join", f.other, nil, 404), "CAMPAIGN_NOT_FOUND")
	raw = f.request(t, "GET", "/campaigns/me/history", f.owner, nil, 200)
	var history v2.QueryHistoryResponse
	if err := v2.DecodeContractResponse("QueryHistoryResponse", raw, &history); err != nil || len(history.Items) != 2 || history.Items[0].SnapshotState != "ready" || *history.Items[0].State.AcceptedRevisionID != *created.Original.RevisionID {
		t.Fatalf("original accepted history invalid: %v", err)
	}
	var count int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM campaign_interaction_history WHERE campaign_id=$1 AND wallet_address=$2`, id, f.owner).Scan(&count); err != nil || count != 2 {
		t.Fatal("HTTP replay changed durable history")
	}
}
func TestHTTPCampaignPausedCancellationAndStrictRoles(t *testing.T) {
	f := newRealHTTP(t)
	created := f.create(t)
	id := created.Original.CampaignID
	f.publish(t, created)
	path := "/campaigns/" + id + "/join"
	f.command(t, "POST", path, f.owner, v2.PositiveInteractionInput{ClientCommandID: httpID(t), ExpectedStateVersion: "0", ExpectedPublicationVersion: "1"}, 200)
	pause := v2.PublicationCommandInput{ClientCommandID: httpID(t), Reason: "Pause through real admin", Operation: "pause", ExpectedPublicationVersion: "1"}
	errorCode(t, f.request(t, "POST", "/campaign-admin/campaigns/"+id+"/publications", f.owner, pause, 403), "CAMPAIGN_ADMIN_REQUIRED")
	paused := f.adminCommand(t, "POST", "/campaign-admin/campaigns/"+id+"/publications", pause, 200)
	if paused.Current.PublicationVersion != "2" {
		t.Fatal("pause publication version")
	}
	errorCode(t, f.request(t, "POST", path, f.other, v2.PositiveInteractionInput{ClientCommandID: httpID(t), ExpectedStateVersion: "0", ExpectedPublicationVersion: "2"}, 409), "CAMPAIGN_NOT_OPEN")
	left := f.command(t, "DELETE", path, f.owner, v2.CancelInteractionInput{ClientCommandID: httpID(t), ExpectedStateVersion: "1"}, 200)
	if left.Current.State.Status != "left" || left.Current.State.AcceptedRevisionID == nil {
		t.Fatal("pause prevented cancellation or erased acceptance")
	}
	errorCode(t, f.request(t, "GET", "/campaigns/me?ids="+id, "", nil, 401), "UNAUTHORIZED")
	errorCode(t, f.request(t, "GET", "/campaigns/me?ids="+id+"&owner="+f.owner, f.other, nil, 400), "CAMPAIGN_INVALID_INPUT")
	errorCode(t, f.request(t, "POST", "/campaign-admin/campaigns", f.other, v2.CreateCommandInput{ClientCommandID: httpID(t), Reason: "Unauthorized create", Revision: httpInput()}, 403), "CAMPAIGN_ADMIN_REQUIRED")
}
