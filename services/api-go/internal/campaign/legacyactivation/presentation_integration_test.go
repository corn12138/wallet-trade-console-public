//go:build integration

package legacyactivation_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/legacyactivation"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/legacyimport"
	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/application"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/postgres"
)

// presentation imports three legacy campaigns for two fresh wallets: x joined
// and set a reminder on one, joined a second and joined a third that is
// imported unpublished; y joined the first only.
type presentation struct {
	fixture
	one, two, hidden string
	x, y, stranger   string
}

func seedPresentation(t *testing.T) presentation {
	t.Helper()
	pool := legacyPool(t)
	random := make([]byte, 6)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	p := "lg02b2-" + hex.EncodeToString(random)
	s := presentation{fixture: fixture{pool: pool, prefix: p}, one: p + "-one", two: p + "-two", hidden: p + "-hidden",
		x: randomWallet(t), y: randomWallet(t), stranger: randomWallet(t)}
	active := `(now() AT TIME ZONE 'UTC') - interval '1 day', (now() AT TIME ZONE 'UTC') + interval '30 days'`
	upcoming := `(now() AT TIME ZONE 'UTC') + interval '2 days', (now() AT TIME ZONE 'UTC') + interval '30 days'`
	exec(t, pool, `INSERT INTO launchpad_campaigns(id,title,description,reward,start_date,end_date,status,participants,created_at,updated_at) VALUES
		($1,'Imported <b>one</b>','Plain <a href="https://legacy.invalid">text</a>',NULL,`+active+`,'active',2,'2020-01-01','2020-01-02'),
		($2,'Imported two',NULL,NULL,`+upcoming+`,'upcoming',1,'2020-01-01','2020-01-01'),
		($3,'Hidden import',NULL,'Legacy pool',`+active+`,'active',1,'2020-01-01','2020-01-01')`, s.one, s.two, s.hidden)
	exec(t, pool, `INSERT INTO campaign_participants(id,campaign_id,wallet_address,joined_at) VALUES
		($1,$2,$3,'2020-01-15 00:00:00'),($4,$2,$5,'2020-01-16 00:00:00'),($6,$7,$3,'2020-02-01 00:00:00'),($8,$9,$3,'2020-03-01 00:00:00')`,
		p+"-p1", s.one, s.x, p+"-p2", s.y, p+"-p3", s.two, p+"-p4", s.hidden)
	exec(t, pool, `INSERT INTO campaign_reminders(id,campaign_id,wallet_address,created_at) VALUES ($1,$2,$3,'2020-01-10 00:00:00')`, p+"-r1", s.one, s.x)
	ctx := context.Background()
	captured, err := legacyimport.Capture(ctx, pool, legacyimport.CaptureOptions{Operator: operator})
	if err != nil || captured.Status != "captured" {
		t.Fatalf("capture: %+v %v", captured, err)
	}
	s.run = captured.RunID
	if err = legacyimport.Fence(ctx, pool, s.run, []string{s.one, s.two, s.hidden}, operator); err != nil {
		t.Fatal(err)
	}
	s.engine = legacyactivation.New(pool, caps)
	store := postgres.New(pool)
	s.queries = application.NewQueryService(store, allowed{}, caps)
	s.commands = application.NewService(store, allowed{}, caps)
	s.compat = campaign.NewCompatibleService(campaign.NewRepository(pool), s.queries)
	for _, id := range []string{s.one, s.two} {
		if decision, _, _ := s.decision(t, id); decision != "import-public" {
			t.Fatalf("%s decision %s", id, decision)
		}
		if _, err = s.engine.Activate(ctx, s.input(t, id)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.review(t, s.hidden, "import-unpublished", []string{"REWARD_TEXT"}, []string{"visibility"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.engine.Activate(ctx, s.input(t, s.hidden)); err != nil {
		t.Fatal(err)
	}
	return s
}

// read sends a GET with the given variants header ("" for none).
func (h *adminHTTP) read(t *testing.T, path, token, variants string) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, h.server.URL+"/api/v2"+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if variants != "" {
		req.Header.Set(v2.VariantsHeader, variants)
	}
	response, err := h.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, response.Header, raw
}

func strictClientSchema(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("../../../../../packages/shared/contracts/fixtures/campaign-v2-schema-strict-client-2026-10-09.json")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// historyPages reads a wallet's whole history with the given page size and
// variants, checking every page's entry rules.
func (h *adminHTTP) historyPages(t *testing.T, token, variants string, limit int) ([]application.HistoryView, []string) {
	t.Helper()
	var items []application.HistoryView
	var cursors []string
	allowed := v2.ParseVariants([]string{variants})
	path := "/campaigns/me/history?limit=" + strconv.Itoa(limit)
	for pages := 0; ; pages++ {
		if pages > 400 {
			t.Fatal("history did not end")
		}
		status, _, raw := h.read(t, path, token, variants)
		page := decodeAs[application.HistoryResponse](t, "QueryHistoryResponse", status, http.StatusOK, raw)
		if err := v2.CheckHistoryResponse(page, allowed); err != nil {
			t.Fatalf("history page: %v %s", err, raw)
		}
		items = append(items, page.Items...)
		if page.NextCursor == nil {
			return items, cursors
		}
		cursors = append(cursors, *page.NextCursor)
		path = "/campaigns/me/history?limit=" + strconv.Itoa(limit) + "&cursor=" + url.QueryEscape(*page.NextCursor)
	}
}

func entryKey(item application.HistoryView) string {
	return deref(item.Entry) + "|" + item.CampaignID + "|" + item.Kind + "|" + deref(item.State.StateVersion) + "|" + item.OccurredAt
}

func TestImportedPresentationOverHTTP(t *testing.T) {
	s := seedPresentation(t)
	h := newAdminHTTP(t, s.fixture)
	for _, wallet := range []string{s.x, s.y, s.stranger} {
		h.login(t, wallet)
	}
	strict := strictClientSchema(t)
	const baseline, imports = "legacyBaselineV1", "legacyImportHistoryV1"

	// A base client sees exactly the bytes it always decoded: unsupported
	// content, no baseline key, accepted by the 2026-10-09 strict schema.
	status, header, raw := h.read(t, "/campaigns/"+s.one, "", "")
	base := decodeAs[application.DetailResponse](t, "QueryDetailResponse", status, http.StatusOK, raw)
	if base.Item.ContentState != "unsupported" || base.Item.Baseline != nil || strings.Contains(string(raw), `"baseline"`) {
		t.Fatalf("base detail: %s", raw)
	}
	if err := v2.DecodeContractResponseUnder(strict, "QueryDetailResponse", raw, new(any)); err != nil {
		t.Fatalf("a strict client must still decode the base detail: %v", err)
	}
	if header.Get("Cache-Control") != "no-store" || !strings.Contains(strings.Join(header.Values("Vary"), ","), v2.VariantsHeader) {
		t.Fatalf("detail headers: %v", header)
	}

	// A negotiating client gets the verbatim baseline, as plain values.
	status, _, raw = h.read(t, "/campaigns/"+s.one, "", baseline)
	shown := decodeAs[application.DetailResponse](t, "QueryDetailResponse", status, http.StatusOK, raw).Item
	if shown.ContentState != "legacyBaseline" || shown.Baseline == nil || shown.Baseline.Title != "Imported <b>one</b>" ||
		deref(shown.Baseline.Description) != `Plain <a href="https://legacy.invalid">text</a>` || shown.Baseline.Reward != nil ||
		shown.EffectivePhase != "active" || deref(shown.ParticipantCount) != "2" || shown.NextTransitionAt == nil {
		t.Fatalf("negotiated detail: %s", raw)
	}
	if err := v2.DecodeContractResponseUnder(strict, "QueryDetailResponse", raw, new(any)); err == nil {
		t.Fatal("the negotiated shape is exactly what a strict client could not decode")
	}
	if status, _, raw = h.read(t, "/campaigns/"+s.two, "", baseline); status != http.StatusOK || !strings.Contains(string(raw), `"effectivePhase":"upcoming"`) {
		t.Fatalf("upcoming baseline: %d %s", status, raw)
	}

	// Lists: the base page decodes under the strict schema; the negotiated
	// page mixes baselines with other content and passes the cross-field rules.
	for _, variants := range []string{"", baseline, "futureVariantV9"} {
		listed := map[string]application.PublicCampaignView{}
		cursor := ""
		for pages := 0; ; pages++ {
			path := "/campaigns?limit=50"
			if cursor != "" {
				path += "&cursor=" + url.QueryEscape(cursor)
			}
			status, _, raw = h.read(t, path, "", variants)
			page := decodeAs[application.ListResponse](t, "QueryListResponse", status, http.StatusOK, raw)
			if variants != baseline {
				if err := v2.DecodeContractResponseUnder(strict, "QueryListResponse", raw, new(any)); err != nil {
					t.Fatalf("%q list page must stay strict-client compatible: %v", variants, err)
				}
			}
			for _, item := range page.Items {
				if err := v2.CheckPublicCampaignView(item, v2.ParseVariants([]string{variants})); err != nil {
					t.Fatalf("%q list item %s: %v", variants, item.ID, err)
				}
				listed[item.ID] = item
			}
			if page.NextCursor == nil || pages > 100 {
				break
			}
			cursor = *page.NextCursor
		}
		want := "unsupported"
		if variants == baseline {
			want = "legacyBaseline"
		}
		if listed[s.one].ContentState != want || listed[s.two].ContentState != want {
			t.Fatalf("%q list content states: %q %q", variants, listed[s.one].ContentState, listed[s.two].ContentState)
		}
		if _, ok := listed[s.hidden]; ok {
			t.Fatalf("%q: an unpublished import is not listed", variants)
		}
	}
	if status, _, _ = h.read(t, "/campaigns/"+s.hidden, "", baseline); status != http.StatusNotFound {
		t.Fatalf("an unpublished import has no public detail: %d", status)
	}

	// History: the base representation still never shows the import; the
	// negotiated one shows x's four imports, unpublished included.
	ownOf := func(items []application.HistoryView) []application.HistoryView {
		var out []application.HistoryView
		for _, item := range items {
			if item.CampaignID == s.one || item.CampaignID == s.two || item.CampaignID == s.hidden {
				out = append(out, item)
			}
		}
		return out
	}
	x := h.tokens[s.x]
	if items, _ := h.historyPages(t, x, "", 50); len(items) != 0 {
		t.Fatalf("base history shows imports: %+v", items)
	}
	imported, _ := h.historyPages(t, x, imports, 50)
	if len(imported) != 4 || len(ownOf(imported)) != 4 {
		t.Fatalf("x's imported entries: %+v", imported)
	}
	recorded := map[string]string{s.one + "join": "2020-01-15T00:00:00Z", s.one + "reminder": "2020-01-10T00:00:00Z", s.two + "join": "2020-02-01T00:00:00Z", s.hidden + "join": "2020-03-01T00:00:00Z"}
	for _, item := range imported {
		if deref(item.Entry) != "legacyImport" || item.Operation != nil || item.ClientCommandID != nil || deref(item.LegacyImport.LegacyRecordedAt) != recorded[item.CampaignID+item.Kind] {
			t.Fatalf("imported entry: %+v", item)
		}
	}
	// Activations commit in order: hidden, two, then one's join and reminder
	// at one instant, ordered by kind.
	order := []string{s.hidden + "join", s.two + "join", s.one + "join", s.one + "reminder"}
	for i, item := range imported {
		if item.CampaignID+item.Kind != order[i] {
			t.Fatalf("history order %d: %s, want %s", i, item.CampaignID+item.Kind, order[i])
		}
	}
	rawHistory, _ := json.Marshal(imported)
	for _, private := range []string{h.admin[2:], admin[2:], s.run, `"` + s.prefix + `-p1"`, `"` + s.prefix + `-r1"`, s.y[2:]} {
		if strings.Contains(string(rawHistory), private) {
			t.Fatalf("imported history exposes %q", private)
		}
	}

	// Owner isolation: y sees only its own import; a stranger sees none.
	if items, _ := h.historyPages(t, h.tokens[s.y], imports, 50); len(items) != 1 || items[0].CampaignID != s.one || items[0].Kind != "join" {
		t.Fatalf("y's history: %+v", items)
	}
	if items, _ := h.historyPages(t, h.tokens[s.stranger], imports, 50); len(items) != 0 {
		t.Fatalf("stranger's history: %+v", items)
	}

	// The unpublished import stays discoverable and cancellable through the
	// personal state; joining it is refused.
	status, _, raw = h.read(t, "/campaigns/me?ids="+s.hidden, x, "")
	me := decodeAs[application.MeResponse](t, "QueryMeResponse", status, http.StatusOK, raw)
	if deref(me.Items[0].Participation.Status) != "joined" || deref(me.Items[0].Participation.AcceptanceProvenance) != "legacyUnknown" {
		t.Fatalf("unpublished personal state: %s", raw)
	}
	if status, raw = h.call(t, http.MethodDelete, "/campaigns/"+s.hidden+"/join", x, map[string]any{"clientCommandId": commandID(t), "expectedStateVersion": "1"}); status != http.StatusOK {
		t.Fatalf("cancel an unpublished import: %d %s", status, raw)
	}
	if status, raw = h.call(t, http.MethodPost, "/campaigns/"+s.two+"/reminder", x, map[string]any{"clientCommandId": commandID(t), "expectedStateVersion": "0", "expectedPublicationVersion": "1"}); status != http.StatusUnprocessableEntity || errorCode(t, raw) != "CAMPAIGN_UNSUPPORTED" {
		t.Fatalf("an upcoming baseline admits no new reminder: %d %s", status, raw)
	}
	if status, raw = h.call(t, http.MethodPost, "/campaigns/"+s.one+"/join", h.tokens[s.stranger], map[string]any{"clientCommandId": commandID(t), "expectedStateVersion": "0", "expectedPublicationVersion": "1"}); status != http.StatusUnprocessableEntity || errorCode(t, raw) != "CAMPAIGN_UNSUPPORTED" {
		t.Fatalf("an active baseline admits no new join: %d %s", status, raw)
	}
	if status, raw = h.call(t, http.MethodDelete, "/campaigns/"+s.one+"/reminder", x, map[string]any{"clientCommandId": commandID(t), "expectedStateVersion": "1"}); status != http.StatusOK {
		t.Fatalf("cancel an imported reminder on a published baseline: %d %s", status, raw)
	}

	// Pages of one and of fifty give the same sequence: the cancellations
	// first, then the imports, without gaps or repeats.
	whole, _ := h.historyPages(t, x, imports, 50)
	single, cursors := h.historyPages(t, x, imports, 1)
	if len(whole) != 6 || len(single) != len(whole) || len(cursors) != len(whole)-1 {
		t.Fatalf("paging: %d whole, %d single, %d cursors", len(whole), len(single), len(cursors))
	}
	for i := range whole {
		if entryKey(whole[i]) != entryKey(single[i]) {
			t.Fatalf("page sequence differs at %d: %s vs %s", i, entryKey(whole[i]), entryKey(single[i]))
		}
	}
	if deref(whole[0].Entry) != "command" || deref(whole[0].Operation) != "remove-reminder" || deref(whole[1].Operation) != "leave" || whole[1].CampaignID != s.hidden {
		t.Fatalf("later cancellations come first: %+v", whole[:2])
	}
	if base, _ := h.historyPages(t, x, "", 50); len(base) != 2 || deref(base[0].Operation) != "remove-reminder" || base[0].Entry != nil {
		t.Fatalf("base history keeps only x's own commands: %+v", base)
	}

	// A cursor continues only the representation that issued it.
	_, negotiatedCursors := h.historyPages(t, x, imports, 2)
	_, baseCursors := h.historyPages(t, x, "", 1)
	if len(negotiatedCursors) == 0 || len(baseCursors) == 0 {
		t.Fatal("paging produced no cursors")
	}
	if status, _, raw = h.read(t, "/campaigns/me/history?limit=2&cursor="+url.QueryEscape(negotiatedCursors[0]), x, ""); status != http.StatusBadRequest || errorCode(t, raw) != "CAMPAIGN_INVALID_INPUT" {
		t.Fatalf("a negotiated cursor on the base representation: %d %s", status, raw)
	}
	if status, _, raw = h.read(t, "/campaigns/me/history?limit=1&cursor="+url.QueryEscape(baseCursors[0]), x, imports); status != http.StatusBadRequest || errorCode(t, raw) != "CAMPAIGN_INVALID_INPUT" {
		t.Fatalf("a base cursor on the negotiated representation: %d %s", status, raw)
	}
	if status, _, _ = h.read(t, "/campaigns/me/history?limit=2&cursor="+url.QueryEscape(negotiatedCursors[0]), h.tokens[s.y], imports); status != http.StatusBadRequest {
		t.Fatalf("another wallet's cursor: %d", status)
	}

	// Session failures stay failures, never an empty history.
	h.credentials.Sessions.RevokeID(h.sessions[s.y])
	if status, _, raw = h.read(t, "/campaigns/me/history", h.tokens[s.y], imports); status != http.StatusUnauthorized {
		t.Fatalf("revoked session: %d %s", status, raw)
	}
	h.credentials.Sessions.SetDown(true)
	if status, _, raw = h.read(t, "/campaigns/me/history", x, imports); status != http.StatusServiceUnavailable || errorCode(t, raw) != "WEB3_SESSION_UNAVAILABLE" {
		t.Fatalf("session store down: %d %s", status, raw)
	}
	h.credentials.Sessions.SetDown(false)
	if status, _, _ = h.read(t, "/campaigns/me/history", "", imports); status != http.StatusUnauthorized {
		t.Fatalf("anonymous history: %d", status)
	}
	t.Logf("presentation: base and negotiated detail/list/history over real HTTP and PostgreSQL; %d negotiated entries for x paged identically at 1 and 50", len(whole))
}
