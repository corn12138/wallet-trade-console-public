//go:build integration

package campaign_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign"
	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/application"
	campaignhttp "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/http"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

const compatibilityWallet = "0x2222222222222222222222222222222222222222"

func compatibilityToken(t *testing.T, wallet string) string {
	t.Helper()
	token, _ := campaign.CampaignAuth.Login(t, wallet, 1)
	return token
}
func compatibilityContext(t *testing.T, verifier *auth.Verifier, wallet string) context.Context {
	t.Helper()
	var ctx context.Context
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+compatibilityToken(t, wallet))
	auth.StrictMiddleware(verifier)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { ctx = r.Context() })).ServeHTTP(httptest.NewRecorder(), r)
	if ctx == nil {
		t.Fatal("strict test identity unavailable")
	}
	return ctx
}
func compatibilityCommandID(t *testing.T) string {
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
	v := hex.EncodeToString(raw[:])
	return v[:8] + "-" + v[8:12] + "-" + v[12:16] + "-" + v[16:20] + "-" + v[20:]
}
func compatibilityCommand(t *testing.T, op, id string) application.Command {
	return application.Command{Operation: op, CampaignID: id, ClientCommandID: compatibilityCommandID(t), Reason: "Compatibility integration", RequestID: "legacy-compatibility-integration"}
}
func compatibilityExecute(t *testing.T, s *application.Service, ctx context.Context, actor string, c application.Command) application.Result {
	t.Helper()
	out, err := s.Execute(ctx, actor, c)
	if err != nil {
		t.Fatalf("%s: %v", c.Operation, err)
	}
	return out.Original
}
func compatibilityRequest(t *testing.T, h http.Handler, path, token string, want int) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s: status=%d want=%d body=%s", path, w.Code, want, w.Body.String())
	}
	return w
}

func TestCompatibleReadsUseRealPublishedStateAndRetainLegacyRows(t *testing.T) {
	url := os.Getenv("CAMPAIGN_LEGACY_TEST_DATABASE_URL")
	if url == "" {
		t.Fatal("dedicated CAMPAIGN_LEGACY_TEST_DATABASE_URL is required")
	}
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal("invalid test connection")
	}
	if !strings.HasPrefix(config.ConnConfig.Database, "campaign_test_legacy") && !strings.HasPrefix(config.ConnConfig.Database, "migration_probe_legacy") {
		t.Fatal("dedicated legacy database required")
	}
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal("test database unavailable")
	}
	defer pool.Close()
	// clock_timestamp() scans as a host-local time.Time. Legacy columns are
	// TIMESTAMP without time zone holding UTC (Prisma convention), so every
	// write below converts with .UTC(); otherwise a +08:00 host shifts the
	// fixture window by eight hours and the phase assertion fails.
	var now time.Time
	if err = pool.QueryRow(t.Context(), `SELECT clock_timestamp()`).Scan(&now); err != nil {
		t.Fatal(err)
	}
	verifier := campaign.CampaignAuth.Verifier
	authorizer := campaignhttp.Authorizer{Verifier: verifier, Admins: func() []string { return []string{campaign.FixtureWallet} }}
	available := true
	caps := func() v2.Capabilities {
		return v2.Capabilities{Templates: map[string]bool{"campaignCardV1": available}}
	}
	store := postgres.New(pool)
	writer := application.NewService(store, authorizer, caps)
	reader := application.NewQueryService(store, authorizer, caps)
	svc := campaign.NewCompatibleService(campaign.NewRepository(pool), reader)
	h := campaign.Router(svc, campaign.StrictResolver(verifier), auth.StrictMiddleware(verifier))
	t.Setenv("CAMPAIGN_V2_ENABLED", "false")
	adminCtx := compatibilityContext(t, verifier, campaign.FixtureWallet)
	walletCtx := compatibilityContext(t, verifier, compatibilityWallet)
	create := func(title string, start, end time.Time, publish bool) application.Result {
		value := *campaign.CompatibilityView(t, "fixture").Content
		value.MaterialTerms.Copy.En.Title = title
		value.MaterialTerms.Copy.Zh.Title = title
		value.MaterialTerms.StartAt = start.UTC().Format("2006-01-02T15:04:05Z")
		value.MaterialTerms.EndAt = end.UTC().Format("2006-01-02T15:04:05Z")
		c := compatibilityCommand(t, "create", "")
		c.Revision = &value
		out := compatibilityExecute(t, writer, adminCtx, campaign.FixtureWallet, c)
		if publish {
			c = compatibilityCommand(t, "publish", out.CampaignID)
			c.ExpectedPublication = &out.PublicationVersion
			c.CandidateRevisionID = out.RevisionID
			out = compatibilityExecute(t, writer, adminCtx, campaign.FixtureWallet, c)
		}
		return out
	}
	draft := create("PRIVATE DRAFT CONTENT", now.Add(-time.Hour), now.Add(time.Hour), false)
	active := create("Published active", now.Add(-time.Hour), now.Add(time.Hour), true)
	upcoming := create("Published upcoming", now.Add(time.Hour), now.Add(2*time.Hour), true)
	ended := create("Published ended", now.Add(-2*time.Hour), now.Add(-time.Hour), true)
	t.Cleanup(func() {
		// The legacy test database is reused across runs. Archive this run's
		// published campaigns, even after a failed assertion, so enabled
		// leftovers cannot crowd later runs' legacy rows out of the bounded
		// list. The pool below is fresh because the test closes its own.
		cleanupPool, err := pgxpool.NewWithConfig(context.Background(), config)
		if err != nil {
			t.Errorf("cleanup pool: %v", err)
			return
		}
		defer cleanupPool.Close()
		cleanup := application.NewService(postgres.New(cleanupPool), authorizer, caps)
		for _, id := range []string{active.CampaignID, upcoming.CampaignID, ended.CampaignID} {
			var version int64
			var mode string
			if err := cleanupPool.QueryRow(context.Background(), `SELECT publication_version,mode FROM campaign_publications WHERE campaign_id=$1`, id).Scan(&version, &mode); err != nil || mode == "archived" {
				continue
			}
			c := compatibilityCommand(t, "archive", id)
			expected := uint64(version)
			c.ExpectedPublication = &expected
			if _, err := cleanup.Execute(adminCtx, campaign.FixtureWallet, c); err != nil {
				t.Errorf("cleanup archive %s: %v", id, err)
			}
		}
	})
	privateCandidate := *campaign.CompatibilityView(t, "candidate").Content
	privateCandidate.MaterialTerms.Copy.En.Title = "PRIVATE CANDIDATE CONTENT"
	c := compatibilityCommand(t, "revision", active.CampaignID)
	c.ExpectedPublication = &active.PublicationVersion
	c.Revision = &privateCandidate
	compatibilityExecute(t, writer, adminCtx, campaign.FixtureWallet, c)
	legacyID := "legacy-compat-" + compatibilityCommandID(t)
	_, err = pool.Exec(t.Context(), `INSERT INTO launchpad_campaigns(id,title,start_date,end_date,status,participants,updated_at) VALUES($1,'Unimported legacy',$2,$3,'ended',77,clock_timestamp())`, legacyID, now.Add(-time.Hour).UTC(), now.Add(time.Hour).UTC())
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(t.Context(), `INSERT INTO campaign_participants(id,campaign_id,wallet_address) VALUES($1,$2,$3)`, compatibilityCommandID(t), legacyID, compatibilityWallet)
	if err != nil {
		t.Fatal(err)
	}
	zero := uint64(0)
	c = compatibilityCommand(t, "join", active.CampaignID)
	c.ExpectedPublication = &active.PublicationVersion
	c.ExpectedState = &zero
	compatibilityExecute(t, writer, walletCtx, compatibilityWallet, c)
	response := compatibilityRequest(t, h, "/?limit=50", "", 200)
	var rows []campaign.Campaign
	if err = json.Unmarshal(response.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	found := map[string]campaign.Campaign{}
	for _, row := range rows {
		found[row.ID] = row
		if row.IsParticipating != nil || row.HasReminder != nil {
			t.Fatal("private fields on public list")
		}
	}
	for _, id := range []string{active.CampaignID, upcoming.CampaignID, ended.CampaignID, legacyID} {
		if _, ok := found[id]; !ok {
			t.Fatalf("missing %s", id)
		}
	}
	if _, ok := found[draft.CampaignID]; ok || strings.Contains(response.Body.String(), "PRIVATE") {
		t.Fatal("draft or candidate leaked")
	}
	if found[active.CampaignID].ParticipantCount != 1 || found[legacyID].ParticipantCount != 1 || found[legacyID].Participants != 77 || found[legacyID].Status != "active" {
		t.Fatal("wrong count or database phase authority")
	}
	if found[active.CampaignID].CreatedAt != nil || found[active.CampaignID].UpdatedAt != nil || found[legacyID].CreatedAt == nil {
		t.Fatal("invented or lost lifecycle time")
	}
	for _, phase := range []string{"active", "upcoming", "ended"} {
		w := compatibilityRequest(t, h, "/?status="+phase, "", 200)
		var list []campaign.Campaign
		if json.Unmarshal(w.Body.Bytes(), &list) != nil {
			t.Fatal("invalid list")
		}
		for _, row := range list {
			if row.Status != phase {
				t.Fatal("phase filter mismatch")
			}
		}
	}
	compatibilityRequest(t, h, "/"+draft.CampaignID, "", 404)
	compatibilityRequest(t, h, "/missing-random-id", "", 404)
	personal := compatibilityRequest(t, h, "/"+active.CampaignID, compatibilityToken(t, compatibilityWallet), 200)
	if personal.Header().Get("Cache-Control") != "private, no-store" || !strings.Contains(personal.Body.String(), `"isParticipating":true`) {
		t.Fatal("real wallet state missing")
	}
	other := compatibilityRequest(t, h, "/"+active.CampaignID, compatibilityToken(t, campaign.FixtureWallet), 200)
	if !strings.Contains(other.Body.String(), `"isParticipating":false`) {
		t.Fatal("cross-wallet state leak")
	}
	writeRequest := httptest.NewRequest(http.MethodPost, "/"+legacyID+"/join", strings.NewReader("retired body"))
	writeRequest.Header.Set("Authorization", "Bearer "+compatibilityToken(t, campaign.FixtureWallet))
	writeResponse := httptest.NewRecorder()
	h.ServeHTTP(writeResponse, writeRequest)
	if writeResponse.Code != 409 || !strings.Contains(writeResponse.Body.String(), "CAMPAIGN_CLIENT_UPGRADE_REQUIRED") {
		t.Fatal("disabled v2 flag reopened legacy mutation")
	}
	available = false
	compatibilityRequest(t, h, "/"+active.CampaignID, "", 422)
	available = true
	for _, op := range []string{"pause", "archive"} {
		c = compatibilityCommand(t, op, active.CampaignID)
		c.ExpectedPublication = &active.PublicationVersion
		active = compatibilityExecute(t, writer, adminCtx, campaign.FixtureWallet, c)
		compatibilityRequest(t, h, "/"+active.CampaignID, "", 422)
		// A paused or archived campaign is unrepresentable in the old DTO; the
		// list must omit it rather than fail for every old client.
		listed := compatibilityRequest(t, h, "/?limit=50", "", 200)
		if strings.Contains(listed.Body.String(), active.CampaignID) || !strings.Contains(listed.Body.String(), legacyID) || !strings.Contains(listed.Body.String(), upcoming.CampaignID) {
			t.Fatalf("%s: legacy list must stay available and omit only the unrepresentable campaign", op)
		}
	}
	// Omitting the archived campaign from the old list must not remove the
	// participant's versioned history or cancellation (TD §15.5).
	one := uint64(1)
	c = compatibilityCommand(t, "leave", active.CampaignID)
	c.ExpectedState = &one
	if left := compatibilityExecute(t, writer, walletCtx, compatibilityWallet, c); left.State == nil || left.State.Status != "left" {
		t.Fatal("cancellation must stay reachable after archive")
	}
	history, err := reader.History(walletCtx, compatibilityWallet, application.HistoryQuery{})
	if err != nil {
		t.Fatal(err)
	}
	operations := map[string]bool{}
	for _, item := range history.Items {
		if item.CampaignID == active.CampaignID && item.Operation != nil {
			operations[*item.Operation] = true
		}
	}
	if !operations["join"] || !operations["leave"] {
		t.Fatalf("archived campaign history must keep join and leave: %v", operations)
	}
	pool.Close()
	compatibilityRequest(t, h, "/"+legacyID, "", 503)
	compatibilityRequest(t, h, "/", "", 503)
	t.Logf("real compatibility: published=%s,%s,%s draft=%s legacy=%s; strict private reads, DB phase, 404/422/503 boundaries", active.CampaignID, upcoming.CampaignID, ended.CampaignID, draft.CampaignID, legacyID)
}
