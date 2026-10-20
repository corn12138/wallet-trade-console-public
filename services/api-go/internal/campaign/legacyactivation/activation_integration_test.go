//go:build integration

package legacyactivation_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/legacyactivation"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/legacyimport"
	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/application"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	admin    = "0x1111111111111111111111111111111111111111"
	operator = "lg02b-test"
	walletA  = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1"
	walletB  = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa2"
	mixedC   = "0xCcCcCcCcCcCcCcCcCcCcCcCcCcCcCcCcCcCcCcC3"
	walletD  = "0xddddddddddddddddddddddddddddddddddddddd4"
	walletE  = "0xeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee5"
	outsider = "0x9999999999999999999999999999999999999999"
)

type allowed struct{}

func (allowed) Require(context.Context, string, bool) error { return nil }

func caps() v2.Capabilities {
	return v2.Capabilities{Templates: map[string]bool{"campaignCardV1": true}}
}

type fixture struct {
	pool                              *pgxpool.Pool
	prefix                            string
	public, mixed, dup, hidden, draft string
	run                               string
	engine                            *legacyactivation.Engine
	queries                           *application.QueryService
	commands                          *application.Service
	compat                            *campaign.Service
}

func legacyPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	config, err := pgxpool.ParseConfig(os.Getenv("CAMPAIGN_LEGACY_TEST_DATABASE_URL"))
	if err != nil || !strings.HasPrefix(config.ConnConfig.Database, "campaign_test_legacy") {
		t.Fatal("activation checks require CAMPAIGN_LEGACY_TEST_DATABASE_URL for a dedicated legacy test database")
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func exec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

func commandID(t *testing.T) string {
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
	raw[6], raw[8] = (raw[6]&15)|0x70, (raw[8]&63)|0x80
	value := hex.EncodeToString(raw[:])
	return value[:8] + "-" + value[8:12] + "-" + value[12:16] + "-" + value[16:20] + "-" + value[20:]
}

// seed writes five legacy campaigns, captures them and fences them all. The
// window is relative to the database clock so every campaign is active.
func seed(t *testing.T) fixture {
	t.Helper()
	pool := legacyPool(t)
	random := make([]byte, 6)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	p := "lg02b-" + hex.EncodeToString(random)
	f := fixture{pool: pool, prefix: p, public: p + "-public", mixed: p + "-mixed", dup: p + "-dup", hidden: p + "-hidden", draft: p + "-draft"}
	window := `(now() AT TIME ZONE 'UTC') - interval '1 day', (now() AT TIME ZONE 'UTC') + interval '30 days'`
	exec(t, pool, `INSERT INTO launchpad_campaigns(id,title,description,reward,start_date,end_date,status,participants,created_at,updated_at) VALUES
		($1,'Public legacy','Read-only description',NULL,`+window+`,'active',2,'2020-01-01 00:00:00.123','2020-01-02 03:04:05.678'),
		($2,'Mixed case',NULL,NULL,`+window+`,'active',1,'2020-01-01','2020-01-01'),
		($3,'Case duplicate',NULL,NULL,`+window+`,'active',1,'2020-01-01','2020-01-01'),
		($4,'Hidden reward',NULL,'100 USDC pool',`+window+`,'active',1,'2020-01-01','2020-01-01'),
		($5,'Draft',NULL,NULL,`+window+`,'draft',0,'2020-01-01','2020-01-01')`, f.public, f.mixed, f.dup, f.hidden, f.draft)
	exec(t, pool, `INSERT INTO campaign_participants(id,campaign_id,wallet_address,joined_at) VALUES
		($1,$2,$3,'2020-01-15 00:00:00'),($4,$2,$5,'2020-01-16 12:34:56.789'),($6,$7,$8,'2020-01-17'),
		($9,$10,$11,'2020-01-18'),($12,$10,$13,'2020-01-19'),($14,$15,$16,'2020-01-20')`,
		p+"-p1", f.public, walletA, p+"-p2", walletB, p+"-p3", f.mixed, mixedC,
		p+"-p4", f.dup, walletD, p+"-p5", "0x"+strings.ToUpper(walletD[2:4])+walletD[4:], p+"-p6", f.hidden, walletE)
	exec(t, pool, `INSERT INTO campaign_reminders(id,campaign_id,wallet_address,created_at) VALUES ($1,$2,$3,'2020-01-10')`, p+"-r1", f.public, walletA)
	ctx := context.Background()
	captured, err := legacyimport.Capture(ctx, pool, legacyimport.CaptureOptions{Operator: operator})
	if err != nil || captured.Status != "captured" {
		t.Fatalf("capture: %+v %v", captured, err)
	}
	f.run = captured.RunID
	if err = legacyimport.Fence(ctx, pool, f.run, []string{f.public, f.mixed, f.dup, f.hidden, f.draft}, operator); err != nil {
		t.Fatal(err)
	}
	f.engine = legacyactivation.New(pool, caps)
	store := postgres.New(pool)
	f.queries = application.NewQueryService(store, allowed{}, caps)
	f.commands = application.NewService(store, allowed{}, caps)
	f.compat = campaign.NewCompatibleService(campaign.NewRepository(pool), f.queries)
	return f
}

func (f fixture) decision(t *testing.T, id string) (decision, row, interactions string) {
	t.Helper()
	if err := f.pool.QueryRow(context.Background(), `SELECT decision, legacy_row_sha256, interactions_sha256 FROM campaign_legacy_import_items WHERE run_id=$1 AND campaign_id=$2`,
		f.run, id).Scan(&decision, &row, &interactions); err != nil {
		t.Fatal(err)
	}
	return decision, row, interactions
}

func (f fixture) input(t *testing.T, id string) legacyactivation.ActivationInput {
	t.Helper()
	_, row, interactions := f.decision(t, id)
	return legacyactivation.ActivationInput{RunID: f.run, CampaignID: id, Actor: admin, ClientCommandID: commandID(t), Reason: "LG-02b activation test",
		RequestID: "lg02b-test", LegacyRowSHA256: row, InteractionsSHA256: interactions}
}

func (f fixture) review(t *testing.T, id, decision string, findings, accepted []string) (legacyactivation.Review, error) {
	t.Helper()
	return f.engine.RecordReview(context.Background(), legacyactivation.ReviewInput{RunID: f.run, CampaignID: id, Decision: decision, AcknowledgedFindings: findings,
		AcceptedDifferences: accepted, Reason: "Reviewed in the LG-02b test", Reviewer: admin, ClientCommandID: commandID(t)})
}

// logActivation records what an activation compared; the record carries
// digests and wallet references only.
func logActivation(t *testing.T, label string, activation legacyactivation.Activation) {
	t.Helper()
	raw, _ := json.Marshal(activation.Comparison)
	t.Logf("%s: decision=%s publication=%d interactions=%d comparison=%s", label, activation.Decision, activation.PublicationVersion, activation.ImportedInteractions, raw)
}

func code(err error) string {
	var refusal *legacyactivation.Error
	if errors.As(err, &refusal) {
		return refusal.Code
	}
	return ""
}

// answer reads the campaign the way the legacy API answers wallet ("" for
// anonymous), before or after activation alike.
func (f fixture) answer(t *testing.T, id, wallet string) string {
	t.Helper()
	value, err := campaign.CompatibleAnswer(context.Background(), f.pool, f.queries, time.Now(), id, wallet)
	if errors.Is(err, campaign.ErrNotFound) {
		return "absent"
	}
	if err != nil {
		t.Fatalf("legacy answer for %s: %v", id, err)
	}
	raw, _ := json.Marshal(value)
	return string(raw)
}

func (f fixture) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// 1, 2: an import-public activation keeps the legacy answer, anonymous and
// per wallet; the versioned model then owns the campaign with legacyUnknown
// states that can be cancelled but not joined anew.
func TestImportPublicKeepsTheLegacyAnswer(t *testing.T) {
	f := seed(t)
	ctx := context.Background()
	if decision, _, _ := f.decision(t, f.public); decision != "import-public" {
		t.Fatalf("public decision %s", decision)
	}
	before := map[string]string{}
	for _, wallet := range []string{"", walletA, walletB, outsider} {
		before[wallet] = f.answer(t, f.public, wallet)
	}
	legacyOnly, err := campaign.NewService(campaign.NewRepository(f.pool)).FindByID(ctx, f.public)
	if err != nil {
		t.Fatal(err)
	}
	if raw, _ := json.Marshal(legacyOnly); string(raw) != before[""] {
		t.Fatal("before activation the compatible API must answer from the legacy row")
	}
	activation, err := f.engine.Activate(ctx, f.input(t, f.public))
	if err != nil {
		t.Fatal(err)
	}
	logActivation(t, "import-public", activation)
	if activation.Decision != "import-public" || activation.PublicationVersion != 1 || activation.InteractionVersion != 3 || activation.ImportedInteractions != 3 ||
		len(activation.Comparison.Differences) != 0 || activation.Comparison.Wallets != 3 {
		t.Fatalf("activation: %+v", activation)
	}
	for wallet, want := range before {
		if got := f.answer(t, f.public, wallet); got != want {
			t.Fatalf("legacy answer for %q changed:\nbefore %s\nafter  %s", wallet, want, got)
		}
	}
	if !strings.Contains(before[walletA], `"isParticipating":true,"hasReminder":true`) || !strings.Contains(before[outsider], `"isParticipating":false,"hasReminder":false`) {
		t.Fatalf("wallet answers: %s / %s", before[walletA], before[outsider])
	}
	listed, err := f.compat.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, row := range listed {
		if row.ID == f.public {
			seen++
		}
	}
	if seen != 1 {
		t.Fatalf("the activated campaign must stay listed exactly once, seen %d", seen)
	}
	active, err := f.compat.List(ctx, "active")
	if err != nil || !containsCampaign(active, f.public) {
		t.Fatalf("the phase-filtered legacy list keeps the activated campaign: %v", err)
	}
	detail, err := f.queries.Detail(ctx, f.public, v2.Variants{})
	if err != nil {
		t.Fatal(err)
	}
	item := detail.Item
	if item.EffectivePhase != "active" || item.StatisticsState != "ready" || item.ParticipantCount == nil || *item.ParticipantCount != "2" || item.ContentState != "unsupported" || item.Content != nil || item.Baseline != nil {
		t.Fatalf("versioned view: %+v", item)
	}
	// Only a client that negotiated the read-only variant sees the original
	// values; the shape stays refused for every other client.
	negotiated, err := f.queries.Detail(ctx, f.public, v2.Variants{LegacyBaseline: true})
	if err != nil {
		t.Fatal(err)
	}
	shown, baseline := negotiated.Item, negotiated.Item.Baseline
	if shown.ContentState != "legacyBaseline" || shown.Content != nil || baseline == nil || baseline.TemplateID != "legacyBaselineV1" || baseline.Title != "Public legacy" ||
		baseline.Description == nil || *baseline.Description != "Read-only description" || baseline.Reward != nil || shown.EffectivePhase != "active" ||
		shown.ParticipantCount == nil || *shown.ParticipantCount != "2" || v2.CheckPublicCampaignView(shown, v2.Variants{LegacyBaseline: true}) != nil {
		t.Fatalf("negotiated baseline view: %+v %+v", shown, baseline)
	}
	if v2.CheckPublicCampaignView(shown, v2.Variants{}) == nil {
		t.Fatal("the baseline shape must be refused without the negotiated variant")
	}
	var source, actor string
	if err = f.pool.QueryRow(ctx, `SELECT d.source, h.actor FROM campaign_definitions d JOIN campaign_publication_history h ON h.campaign_id=d.id AND h.publication_version=1
		JOIN campaign_commands c ON c.id=h.command_id AND c.operation='publish' WHERE d.id=$1`, f.public).Scan(&source, &actor); err != nil || source != "legacy-unversioned" || actor != admin {
		t.Fatalf("definition and present publish: %s %s %v", source, actor, err)
	}
	if n := f.count(t, `SELECT count(*) FROM campaign_interaction_states WHERE campaign_id=$1 AND acceptance_provenance='legacyUnknown' AND row_version=1`, f.public); n != 3 {
		t.Fatalf("imported states %d", n)
	}
	var accepted time.Time
	if err = f.pool.QueryRow(ctx, `SELECT accepted_at FROM campaign_interaction_states WHERE campaign_id=$1 AND wallet_address=$2 AND kind='join'`, f.public, walletB).Scan(&accepted); err != nil ||
		!accepted.Equal(time.Date(2020, 1, 16, 12, 34, 56, 789000000, time.UTC)) {
		t.Fatalf("the legacy join time is kept as the accepted-at fact: %v %v", accepted, err)
	}
	if n := f.count(t, `SELECT count(*) FROM durable_outbox WHERE aggregate_id LIKE $1`, `["campaign","`+f.public+`",%`); n != 7 {
		t.Fatalf("outbox events %d, want 1 publication + 3 public + 3 personal", n)
	}
	if n := f.count(t, `SELECT count(*) FROM durable_outbox WHERE topic='campaign.personal.changed' AND aggregate_id LIKE $1 AND owner_scope=$2`, `["campaign","`+f.public+`",%`, walletA); n != 2 {
		t.Fatalf("personal events go to the participant's scope, got %d for wallet A", n)
	}
	me, err := f.queries.Me(ctx, outsider, []string{f.public})
	if err != nil || me.Items[0].Participation.State != "absent" {
		t.Fatalf("an activated import proves absence: %+v %v", me, err)
	}
	if items, err := f.historyOf(ctx, walletA, f.public, v2.Variants{}); err != nil || len(items) != 0 {
		t.Fatalf("the base representation never presents an import as the wallet's own command: %+v %v", items, err)
	}
	imported, err := f.historyOf(ctx, walletA, f.public, importHistory)
	if err != nil || len(imported) != 2 {
		t.Fatalf("the negotiated representation lists both imported states: %+v %v", imported, err)
	}
	recorded := map[string]string{"join": "2020-01-15T00:00:00Z", "reminder": "2020-01-10T00:00:00Z"}
	for i, kind := range []string{"join", "reminder"} {
		entry := imported[i]
		if entry.Kind != kind || deref(entry.Entry) != "legacyImport" || entry.Operation != nil || entry.ClientCommandID != nil || entry.BeforeStatus != nil ||
			entry.LegacyImport == nil || entry.LegacyImport.ImportedAt != entry.OccurredAt || deref(entry.LegacyImport.LegacyRecordedAt) != recorded[kind] ||
			entry.SnapshotState != "legacyUnknown" || entry.AcceptedTerms != nil || entry.State.AcceptedRevisionID != nil {
			t.Fatalf("imported entry %d must state the import, the legacy record time and unknown terms: %+v", i, entry)
		}
	}
	if raw, _ := json.Marshal(imported); strings.Contains(string(raw), admin[2:]) || strings.Contains(string(raw), f.run) || strings.Contains(string(raw), `"`+f.prefix+`-p1"`) || strings.Contains(string(raw), `"`+f.prefix+`-r1"`) {
		t.Fatalf("imported entries expose the administrator, the run or archived rows: %s", raw)
	}
	one := uint64(1)
	leave := application.Command{Operation: "leave", CampaignID: f.public, ClientCommandID: commandID(t), Reason: "leaving", RequestID: "lg02b-test", ExpectedState: &one}
	left, err := f.commands.Execute(ctx, walletA, leave)
	if err != nil || left.Original.State == nil || left.Original.State.Status != "left" || left.Original.State.AcceptanceProvenance != "legacyUnknown" {
		t.Fatalf("an imported participation can be cancelled: %+v %v", left, err)
	}
	if got := f.answer(t, f.public, ""); !strings.Contains(got, `"participantCount":1`) || !strings.Contains(got, `"participants":2`) {
		t.Fatalf("the live count follows the cancellation, the legacy counter stays: %s", got)
	}
	if items, err := f.historyOf(ctx, walletA, f.public, v2.Variants{}); err != nil || len(items) != 1 || deref(items[0].Operation) != "leave" || items[0].SnapshotState != "legacyUnknown" {
		t.Fatalf("the cancellation is the wallet's history: %+v %v", items, err)
	}
	// One stream: the later cancellation first, then the imports at the
	// activation instant in key order.
	if items, err := f.historyOf(ctx, walletA, f.public, importHistory); err != nil || len(items) != 3 || deref(items[0].Entry) != "command" || deref(items[0].Operation) != "leave" ||
		deref(items[1].Entry) != "legacyImport" || items[1].Kind != "join" || deref(items[2].Entry) != "legacyImport" || items[2].Kind != "reminder" {
		t.Fatalf("negotiated history after cancellation: %+v %v", items, err)
	}
	zero, pub := uint64(0), uint64(1)
	join := application.Command{Operation: "join", CampaignID: f.public, ClientCommandID: commandID(t), Reason: "joining", RequestID: "lg02b-test", ExpectedState: &zero, ExpectedPublication: &pub}
	// A definite refusal, not a dependency failure the client would have to
	// reconcile as an unknown result.
	var refused *v2.DomainError
	if _, err = f.commands.Execute(ctx, outsider, join); !errors.As(err, &refused) || refused.Code != "CAMPAIGN_UNSUPPORTED" || refused.Status != 422 {
		t.Fatalf("nobody can join a legacy baseline: %v", err)
	}
	// Containment works on a baseline; reopening it as executable content does
	// not, and is refused just as definitely.
	publication := func(operation string, expected uint64) error {
		_, err := f.commands.Execute(ctx, admin, application.Command{Operation: operation, CampaignID: f.public, ClientCommandID: commandID(t), Reason: "baseline publication",
			RequestID: "lg02b-test", ExpectedPublication: &expected})
		return err
	}
	if err = publication("pause", 1); err != nil {
		t.Fatalf("pause a baseline: %v", err)
	}
	if err = publication("resume", 2); !errors.As(err, &refused) || refused.Code != "CAMPAIGN_UNSUPPORTED" {
		t.Fatalf("resume of a read-only baseline: %v", err)
	}
	if err = publication("archive", 2); err != nil {
		t.Fatalf("archive a baseline: %v", err)
	}
	if left, err := f.commands.Execute(ctx, walletB, application.Command{Operation: "leave", CampaignID: f.public, ClientCommandID: commandID(t), Reason: "leaving",
		RequestID: "lg02b-test", ExpectedState: &one}); err != nil || left.Original.State == nil || left.Original.State.Status != "left" {
		t.Fatalf("an archived baseline keeps cancellation: %+v %v", left, err)
	}
}

var importHistory = v2.Variants{LegacyImportHistory: true}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// historyOf is the wallet's history for one campaign; the wallets are shared
// by every run of these tests on the same database.
func (f fixture) historyOf(ctx context.Context, wallet, id string, variants v2.Variants) ([]application.HistoryView, error) {
	var out []application.HistoryView
	query := application.HistoryQuery{Limit: 50, Variants: variants}
	for {
		page, err := f.queries.History(ctx, wallet, query)
		if err != nil {
			return nil, err
		}
		if err = v2.CheckHistoryResponse(page, variants); err != nil {
			return nil, err
		}
		for _, item := range page.Items {
			if item.CampaignID == id {
				out = append(out, item)
			}
		}
		if page.NextCursor == nil {
			return out, nil
		}
		query.Cursor = *page.NextCursor
	}
}

func containsCampaign(rows []campaign.Campaign, id string) bool {
	for _, row := range rows {
		if row.ID == id {
			return true
		}
	}
	return false
}

// The baseline phase follows the legacy rule at every point of the window:
// the legacy answer stays the same for an upcoming and an ended campaign, and
// each stays in its phase-filtered legacy list.
func TestBaselinePhasesFollowTheLegacyRule(t *testing.T) {
	pool := legacyPool(t)
	ctx := context.Background()
	random := make([]byte, 6)
	_, _ = rand.Read(random)
	p := "lg02b-phase-" + hex.EncodeToString(random)
	upcoming, ended := p+"-upcoming", p+"-ended"
	exec(t, pool, `INSERT INTO launchpad_campaigns(id,title,start_date,end_date,status,participants,updated_at) VALUES
		($1,'Upcoming',(now() AT TIME ZONE 'UTC') + interval '10 days',(now() AT TIME ZONE 'UTC') + interval '20 days','upcoming',0,now()),
		($2,'Ended',(now() AT TIME ZONE 'UTC') - interval '20 days',(now() AT TIME ZONE 'UTC') - interval '10 days','ended',0,now())`, upcoming, ended)
	captured, err := legacyimport.Capture(ctx, pool, legacyimport.CaptureOptions{Operator: operator})
	if err != nil {
		t.Fatal(err)
	}
	if err = legacyimport.Fence(ctx, pool, captured.RunID, []string{upcoming, ended}, operator); err != nil {
		t.Fatal(err)
	}
	store := postgres.New(pool)
	queries := application.NewQueryService(store, allowed{}, caps)
	f := fixture{pool: pool, run: captured.RunID, engine: legacyactivation.New(pool, caps), queries: queries, compat: campaign.NewCompatibleService(campaign.NewRepository(pool), queries)}
	for id, phase := range map[string]string{upcoming: "upcoming", ended: "ended"} {
		before := f.answer(t, id, "")
		if _, err = f.engine.Activate(ctx, f.input(t, id)); err != nil {
			t.Fatal(err)
		}
		if after := f.answer(t, id, ""); after != before || !strings.Contains(after, `"status":"`+phase+`"`) {
			t.Fatalf("%s answer changed:\nbefore %s\nafter  %s", id, before, after)
		}
		detail, err := queries.Detail(ctx, id, v2.Variants{})
		if err != nil || detail.Item.EffectivePhase != phase {
			t.Fatalf("%s versioned phase: %+v %v", id, detail.Item, err)
		}
		listed, err := f.compat.List(ctx, phase)
		if err != nil || !containsCampaign(listed, id) {
			t.Fatalf("%s must stay in the %s legacy list: %v", id, phase, err)
		}
	}
}

// 3: drift since the capture and a changed plan write nothing.
func TestDriftAndPlanChangeWriteNothing(t *testing.T) {
	pool := legacyPool(t)
	ctx := context.Background()
	random := make([]byte, 6)
	_, _ = rand.Read(random)
	id := "lg02b-drift-" + hex.EncodeToString(random)
	exec(t, pool, `INSERT INTO launchpad_campaigns(id,title,start_date,end_date,status,participants,updated_at) VALUES ($1,'Drift','2020-01-01','2999-01-01','active',0,now())`, id)
	captured, err := legacyimport.Capture(ctx, pool, legacyimport.CaptureOptions{Operator: operator})
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{pool: pool, run: captured.RunID, engine: legacyactivation.New(pool, caps)}
	input := f.input(t, id)
	exec(t, pool, `UPDATE launchpad_campaigns SET title='Drifted' WHERE id=$1`, id)
	if err = legacyimport.Fence(ctx, pool, captured.RunID, []string{id}, operator); err != nil {
		t.Fatal(err)
	}
	_, err = f.engine.Activate(ctx, input)
	var refusal *legacyactivation.Error
	if !errors.As(err, &refusal) || refusal.Code != "CAMPAIGN_LEGACY_DRIFT" || len(refusal.Drift) != 1 || refusal.Drift[0].Change != "changed" {
		t.Fatalf("drift must be refused with its report: %v", err)
	}
	stale := input
	stale.LegacyRowSHA256 = strings.Repeat("0", 64)
	if _, err = f.engine.Activate(ctx, stale); code(err) != "CAMPAIGN_LEGACY_DRIFT" {
		t.Fatalf("drift is checked first: %v", err)
	}
	if n := f.count(t, `SELECT count(*) FROM campaign_definitions WHERE id=$1`, id); n != 0 {
		t.Fatal("a refused activation must write nothing")
	}
}

// 6, 10: review rules; a mixed-case wallet needs a review that accepts the
// wallet-state change; an unpublished import hides the campaign; a review
// accepting less than the comparison finds rolls everything back.
func TestReviewDecisionsGovernActivation(t *testing.T) {
	f := seed(t)
	ctx := context.Background()
	for _, id := range []string{f.mixed, f.dup, f.hidden, f.draft} {
		if decision, _, _ := f.decision(t, id); decision != "review-required" {
			t.Fatalf("%s decision %s", id, decision)
		}
	}
	if _, err := f.engine.Activate(ctx, f.input(t, f.mixed)); code(err) != "CAMPAIGN_LEGACY_REVIEW_REQUIRED" {
		t.Fatalf("a review-required item needs a review: %v", err)
	}
	if _, err := f.review(t, f.mixed, "import-public", nil, []string{"walletState"}); code(err) != "CAMPAIGN_LEGACY_REVIEW_INCOMPLETE" {
		t.Fatalf("a review must acknowledge every review finding: %v", err)
	}
	if _, err := f.review(t, f.hidden, "import-unpublished", []string{"REWARD_TEXT"}, []string{"walletState"}); code(err) != "CAMPAIGN_LEGACY_REVIEW_UNEXPLAINED" {
		t.Fatalf("an accepted difference needs a finding that explains it: %v", err)
	}
	if _, err := f.review(t, f.draft, "import-public", []string{"STATUS_UNKNOWN"}, nil); code(err) != "CAMPAIGN_LEGACY_NOT_PUBLIC" {
		t.Fatalf("only a campaign the old API listed may become public: %v", err)
	}
	input := legacyactivation.ReviewInput{RunID: f.run, CampaignID: f.mixed, Decision: "import-public", AcknowledgedFindings: []string{"PARTICIPANT_WALLET_CASE_LOOKUP"},
		AcceptedDifferences: []string{"walletState"}, Reason: "checksum wallet becomes visible", Reviewer: admin, ClientCommandID: commandID(t)}
	review, err := f.engine.RecordReview(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := f.engine.RecordReview(ctx, input); err != nil || !again.Replayed || again.ID != review.ID {
		t.Fatalf("the same review command replays: %+v %v", again, err)
	}
	changed := input
	changed.Reason = "a different reason"
	if _, err = f.engine.RecordReview(ctx, changed); code(err) != "CAMPAIGN_LEGACY_IDEMPOTENCY_CONFLICT" {
		t.Fatalf("one command ID records one review: %v", err)
	}
	other := input
	other.ClientCommandID = commandID(t)
	if _, err = f.engine.RecordReview(ctx, other); code(err) != "CAMPAIGN_LEGACY_ALREADY_REVIEWED" {
		t.Fatalf("one item admits one review: %v", err)
	}
	lower := strings.ToLower(mixedC)
	beforeWallet := f.answer(t, f.mixed, lower)
	activation, err := f.engine.Activate(ctx, f.input(t, f.mixed))
	if err != nil {
		t.Fatal(err)
	}
	logActivation(t, "checksum wallet", activation)
	if strings.Join(activation.Comparison.Classes, ",") != "walletState" || activation.ReviewID != review.ID {
		t.Fatalf("the accepted wallet-state change is recorded: %+v", activation.Comparison)
	}
	if !strings.Contains(beforeWallet, `"isParticipating":false`) || !strings.Contains(f.answer(t, f.mixed, lower), `"isParticipating":true`) {
		t.Fatal("the legacy API never matched the checksum wallet; the import shows its participation")
	}

	if _, err = f.review(t, f.hidden, "import-unpublished", []string{"REWARD_TEXT"}, []string{"visibility"}); err != nil {
		t.Fatal(err)
	}
	if activation, err = f.engine.Activate(ctx, f.input(t, f.hidden)); err != nil || strings.Join(activation.Comparison.Classes, ",") != "visibility" || activation.PublicationVersion != 0 {
		t.Fatalf("unpublished import: %+v %v", activation, err)
	}
	logActivation(t, "unpublished", activation)
	if got := f.answer(t, f.hidden, ""); got != "absent" {
		t.Fatalf("an unpublished import leaves the legacy API: %s", got)
	}
	if me, err := f.queries.Me(ctx, walletE, []string{f.hidden}); err != nil || me.Items[0].Participation.Status == nil || *me.Items[0].Participation.Status != "joined" {
		t.Fatalf("the participant still sees the imported state: %+v %v", me, err)
	}

	if _, err = f.review(t, f.draft, "import-unpublished", []string{"STATUS_UNKNOWN"}, nil); err != nil {
		t.Fatal(err)
	}
	if activation, err = f.engine.Activate(ctx, f.input(t, f.draft)); err != nil || len(activation.Comparison.Differences) != 0 {
		t.Fatalf("a draft stays hidden before and after: %+v %v", activation, err)
	}

	if _, err = f.review(t, f.dup, "import-public", []string{"PARTICIPANT_CASE_DUPLICATE", "PARTICIPANT_WALLET_CASE_LOOKUP"}, nil); err != nil {
		t.Fatal(err)
	}
	beforeDup := f.answer(t, f.dup, "")
	_, err = f.engine.Activate(ctx, f.input(t, f.dup))
	var refusal *legacyactivation.Error
	if !errors.As(err, &refusal) || refusal.Code != "CAMPAIGN_LEGACY_COMPARISON_FAILED" || refusal.Comparison == nil || strings.Join(refusal.Comparison.Classes, ",") != "participantCount" {
		t.Fatalf("an unaccepted difference fails the comparison: %v", err)
	}
	raw, _ := json.Marshal(refusal.Comparison)
	t.Logf("refused case duplicate: %s %s", refusal.Code, raw)
	if f.answer(t, f.dup, "") != beforeDup || f.count(t, `SELECT count(*) FROM campaign_definitions WHERE id=$1`, f.dup) != 0 ||
		f.count(t, `SELECT count(*) FROM durable_outbox WHERE aggregate_id LIKE $1`, `["campaign","`+f.dup+`",%`) != 0 ||
		f.count(t, `SELECT count(*) FROM campaign_commands WHERE target_id=$1`, f.dup) != 0 {
		t.Fatal("a failed comparison rolls every write back")
	}
}

// 5, 7: replays return the recorded activation; the campaign stays fenced.
func TestActivationIsIdempotentAndPermanent(t *testing.T) {
	f := seed(t)
	ctx := context.Background()
	input := f.input(t, f.public)
	first, err := f.engine.Activate(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.engine.Activate(ctx, input)
	if err != nil || !again.Replayed || again.ID != first.ID || again.CommandID != first.CommandID || again.InteractionVersion != first.InteractionVersion {
		t.Fatalf("replay: %+v %v", again, err)
	}
	other := f.input(t, f.public)
	if _, err = f.engine.Activate(ctx, other); code(err) != "CAMPAIGN_LEGACY_ALREADY_ACTIVATED" {
		t.Fatalf("another command meets an activated campaign: %v", err)
	}
	changed := input
	changed.Reason = "another reason"
	if _, err = f.engine.Activate(ctx, changed); code(err) != "CAMPAIGN_LEGACY_IDEMPOTENCY_CONFLICT" {
		t.Fatalf("one command ID activates one request: %v", err)
	}
	if missing, err := legacyimport.Lift(ctx, f.pool, f.run, []string{f.public}, operator); err == nil || len(missing) != 0 {
		t.Fatalf("an activated campaign stays fenced: %v %v", missing, err)
	}
	if err = legacyimport.Abandon(ctx, f.pool, f.run, operator); err == nil {
		t.Fatal("a run with an activated campaign cannot be abandoned")
	}
	_, err = f.pool.Exec(ctx, `UPDATE launchpad_campaigns SET title='after activation' WHERE id=$1`, f.public)
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != "55006" {
		t.Fatalf("legacy writes stay refused: %v", err)
	}
}

// 9: a second activation and a lift wait for an activation in progress and
// then fail.
func TestConcurrentActivationAndLiftWait(t *testing.T) {
	f := seed(t)
	ctx := context.Background()
	second := make(chan error, 1)
	lifted := make(chan error, 1)
	secondInput := f.input(t, f.public)
	restore := legacyactivation.SetActivationHookForTest(func() {
		go func() {
			_, err := f.engine.Activate(ctx, secondInput)
			second <- err
		}()
		go func() {
			_, err := legacyimport.Lift(ctx, f.pool, f.run, []string{f.public}, operator)
			lifted <- err
		}()
		deadline := time.Now().Add(10 * time.Second)
		for f.count(t, `SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND NOT granted`) < 2 {
			if time.Now().After(deadline) {
				t.Error("the second activation and the lift must wait for the campaign lock")
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	})
	_, err := f.engine.Activate(ctx, f.input(t, f.public))
	restore()
	if err != nil {
		t.Fatal(err)
	}
	if err = <-second; code(err) != "CAMPAIGN_LEGACY_ALREADY_ACTIVATED" {
		t.Fatalf("the waiting activation meets the activated campaign: %v", err)
	}
	if err = <-lifted; err == nil || !strings.Contains(err.Error(), "stays fenced") {
		t.Fatalf("the waiting lift is refused: %v", err)
	}
}

// 8: the database refuses legacy-import facts that do not match an
// activation's shape.
func TestLegacyImportAttributionGuards(t *testing.T) {
	f := seed(t)
	ctx := context.Background()
	activation, err := f.engine.Activate(ctx, f.input(t, f.public))
	if err != nil {
		t.Fatal(err)
	}
	attempt := func(name, want string, statements ...string) {
		t.Helper()
		tx, err := f.pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		// {next} is the next interaction version as this transaction sees it,
		// so a forgery that a weakened guard commits does not collide with the
		// following ones.
		var current int64
		if err = tx.QueryRow(ctx, `SELECT interaction_version FROM campaign_publications WHERE campaign_id=$1`, f.public).Scan(&current); err != nil {
			t.Fatal(err)
		}
		for _, sql := range statements {
			if _, err = tx.Exec(ctx, strings.ReplaceAll(sql, "{next}", strconv.FormatInt(current+1, 10))); err != nil {
				break
			}
		}
		if err == nil {
			err = tx.Commit(ctx)
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s must be refused with %q, got %v", name, want, err)
		}
	}
	command := func(id, result string) string {
		return `INSERT INTO campaign_commands(id,actor,operation,target_id,client_command_id,payload_hash,expected_publication_version,expected_state_version,reason,request_id,result,occurred_at)
			VALUES ('` + id + `','` + admin + `','legacy-import','` + f.public + `','` + commandID(t) + `','` + strings.Repeat("a", 64) + `',NULL,0,'forged','forged','` + result + `','2026-01-01T00:00:00Z')`
	}
	result := func(wallet string) string {
		return `{"activationId":"` + activation.ID + `","walletAddress":"` + wallet + `","kind":"join"}`
	}
	ids := []string{"00000000-0000-4000-8000-0000000000a1", "00000000-0000-4000-8000-0000000000a2", "00000000-0000-4000-8000-0000000000a3", "00000000-0000-4000-8000-0000000000a4", "00000000-0000-4000-8000-0000000000a5"}
	// Each forged import below satisfies every other constraint: the real
	// activation and its time, matching state rows and the next interaction
	// version. Only the legacy-import attribution branch refuses them.
	at := activation.ActivatedAt.Format(time.RFC3339Nano)
	const next = "{next}"
	var termsHash string
	if err = f.pool.QueryRow(ctx, `SELECT material_terms_hash FROM campaign_revisions WHERE id=$1`, activation.RevisionID).Scan(&termsHash); err != nil {
		t.Fatal(err)
	}
	forgedCommand := func(id, wallet string) string {
		return `INSERT INTO campaign_commands(id,actor,operation,target_id,client_command_id,payload_hash,expected_publication_version,expected_state_version,reason,request_id,result,occurred_at)
			VALUES ('` + id + `','` + admin + `','legacy-import','` + f.public + `','` + commandID(t) + `','` + strings.Repeat("a", 64) + `',NULL,0,'forged','forged','` + result(wallet) + `','` + at + `')`
	}
	bump := `UPDATE campaign_publications SET interaction_version=` + next + ` WHERE campaign_id='` + f.public + `'`
	accepted := `'` + activation.RevisionID + `',1,'` + termsHash + `','2020-01-01T00:00:00Z'`
	attempt("verified import", "legacy import attribution mismatch", forgedCommand(ids[0], outsider),
		`INSERT INTO campaign_interaction_states(campaign_id,wallet_address,kind,status,row_version,acceptance_provenance,accepted_revision_id,accepted_publication_version,accepted_terms_hash,accepted_at,changed_at)
			VALUES ('`+f.public+`','`+outsider+`','join','joined',1,'verified',`+accepted+`,'`+at+`')`,
		`INSERT INTO campaign_interaction_history(campaign_id,wallet_address,kind,row_version,interaction_version,command_id,before_status,after_status,acceptance_provenance,accepted_revision_id,accepted_publication_version,accepted_terms_hash,accepted_at,occurred_at)
			VALUES ('`+f.public+`','`+outsider+`','join',1,`+next+`,'`+ids[0]+`',NULL,'joined','verified',`+accepted+`,'`+at+`')`, bump)
	attempt("import with a predecessor", "legacy import attribution mismatch", forgedCommand(ids[1], walletA),
		`UPDATE campaign_interaction_states SET row_version=2,changed_at='`+at+`' WHERE campaign_id='`+f.public+`' AND wallet_address='`+walletA+`' AND kind='join'`,
		`INSERT INTO campaign_interaction_history(campaign_id,wallet_address,kind,row_version,interaction_version,command_id,before_status,after_status,acceptance_provenance,accepted_at,occurred_at)
			VALUES ('`+f.public+`','`+walletA+`','join',2,`+next+`,'`+ids[1]+`','joined','joined','legacyUnknown','2020-01-15T00:00:00Z','`+at+`')`, bump)
	const forgedWallet = "0x7777777777777777777777777777777777777777"
	attempt("import for another wallet", "legacy import attribution mismatch", forgedCommand(ids[2], walletB),
		`INSERT INTO campaign_interaction_states(campaign_id,wallet_address,kind,status,row_version,acceptance_provenance,accepted_at,changed_at)
			VALUES ('`+f.public+`','`+forgedWallet+`','join','joined',1,'legacyUnknown','2020-01-01T00:00:00Z','`+at+`')`,
		`INSERT INTO campaign_interaction_history(campaign_id,wallet_address,kind,row_version,interaction_version,command_id,before_status,after_status,acceptance_provenance,accepted_at,occurred_at)
			VALUES ('`+f.public+`','`+forgedWallet+`','join',1,`+next+`,'`+ids[2]+`',NULL,'joined','legacyUnknown','2020-01-01T00:00:00Z','`+at+`')`,
		bump)
	attempt("import command without its activation", "legacy import command without its activation",
		command(ids[3], `{"activationId":"00000000-0000-4000-8000-00000000ffff","walletAddress":"`+outsider+`","kind":"join"}`))
	publishing := `INSERT INTO campaign_commands(id,actor,operation,target_id,client_command_id,payload_hash,expected_publication_version,expected_state_version,reason,request_id,result,occurred_at)
		VALUES ('` + ids[4] + `','` + admin + `','legacy-import','` + f.public + `','` + commandID(t) + `','` + strings.Repeat("a", 64) + `',1,NULL,'forged','forged','{"activationId":"` + activation.ID + `"}','2026-01-01T00:00:00Z')`
	attempt("definition naming a missing activation", "legacy definition without its activation",
		`INSERT INTO campaign_definitions(id,source,legacy_snapshot,created_by) VALUES ('`+f.mixed+`','legacy-unversioned','{"activationId":"00000000-0000-4000-8000-00000000eeee"}','`+admin+`')`)
	attempt("import publishing a campaign", "command cannot publish a campaign", publishing,
		`INSERT INTO campaign_publication_history(campaign_id,publication_version,command_id,actor,before_mode,after_mode,before_revision_id,after_revision_id,occurred_at)
			VALUES ('`+f.public+`',2,'`+ids[4]+`','`+admin+`','enabled','enabled','`+activation.RevisionID+`','`+activation.RevisionID+`','2026-01-01T00:00:00Z')`)
}
