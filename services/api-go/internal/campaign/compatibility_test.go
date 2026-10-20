package campaign

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/application"
)

const compatibilityNow = "2026-10-08T00:00:00Z"

type publishedProbe struct {
	listFn                          func(context.Context, application.ListQuery) (application.ListResponse, error)
	detailFn                        func(context.Context, string) (application.DetailResponse, error)
	meFn                            func(context.Context, string, []string) (application.MeResponse, error)
	baselinesFn                     func(context.Context, []application.RevisionRef) (map[application.RevisionRef]v2.LegacyBaseline, error)
	listCalls, detailCalls, meCalls int
	baselineCalls                   int
}

func (p *publishedProbe) List(ctx context.Context, query application.ListQuery) (application.ListResponse, error) {
	p.listCalls++
	return p.listFn(ctx, query)
}
func (p *publishedProbe) Detail(ctx context.Context, id string, _ v2.Variants) (application.DetailResponse, error) {
	p.detailCalls++
	return p.detailFn(ctx, id)
}
func (p *publishedProbe) Me(ctx context.Context, actor string, ids []string) (application.MeResponse, error) {
	p.meCalls++
	return p.meFn(ctx, actor, ids)
}
func (p *publishedProbe) Baselines(ctx context.Context, refs []application.RevisionRef) (map[application.RevisionRef]v2.LegacyBaseline, error) {
	p.baselineCalls++
	if p.baselinesFn == nil {
		return map[application.RevisionRef]v2.LegacyBaseline{}, nil
	}
	return p.baselinesFn(ctx, refs)
}
func compatibilityView(t *testing.T, id string) application.PublicCampaignView {
	t.Helper()
	raw, err := os.ReadFile("../../../../packages/shared/contracts/fixtures/campaign-v2-revision.json")
	if err != nil {
		t.Fatal(err)
	}
	input, err := v2.DecodeRevision(raw)
	if err != nil {
		t.Fatal(err)
	}
	count := "3"
	return application.PublicCampaignView{ID: id, RevisionID: "11111111-1111-4111-8111-111111111111", PublicationVersion: "1", InteractionVersion: "2", Mode: "enabled", EffectivePhase: "active", ContentState: "ready", Content: &input, MaterialTermsHash: strings.Repeat("a", 64), ContentHash: strings.Repeat("b", 64), ParticipantCount: &count, StatisticsState: "ready"}
}
func compatibilityPage(rows ...application.PublicCampaignView) application.ListResponse {
	if rows == nil {
		rows = []application.PublicCampaignView{}
	}
	return application.ListResponse{SchemaVersion: 1, ServerNow: compatibilityNow, AsOf: compatibilityNow, Items: rows}
}
func compatibilityReader(view application.PublicCampaignView) *publishedProbe {
	return &publishedProbe{
		listFn: func(context.Context, application.ListQuery) (application.ListResponse, error) {
			return compatibilityPage(view), nil
		},
		detailFn: func(context.Context, string) (application.DetailResponse, error) {
			return application.DetailResponse{SchemaVersion: 1, ServerNow: compatibilityNow, AsOf: compatibilityNow, Item: view}, nil
		},
		meFn: func(context.Context, string, []string) (application.MeResponse, error) {
			return application.MeResponse{}, errors.New("unexpected private read")
		},
	}
}
func compatibilityService(repo *stubCampaignReader, p PublishedReader) *Service {
	return &Service{repo: repo, compat: &publishedCompatibility{reader: p}}
}
func compatibilityError(t *testing.T, err error, status int) {
	t.Helper()
	var domain *v2.DomainError
	if !errors.As(err, &domain) || domain.Status != status {
		t.Fatalf("expected %d, got %v", status, err)
	}
}

func TestCompatibleListUsesPublishedPrefixThenLegacyWithoutPrivateReads(t *testing.T) {
	p := compatibilityReader(compatibilityView(t, "v2-z"))
	first, second := compatibilityView(t, "v2-z"), compatibilityView(t, "v2-a")
	p.listFn = func(ctx context.Context, q application.ListQuery) (application.ListResponse, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 5*time.Second {
			t.Fatal("missing read budget")
		}
		if q.Cursor == "" {
			if q.Limit != 3 {
				t.Fatal(q)
			}
			page := compatibilityPage(first)
			next := "page2"
			page.NextCursor = &next
			return page, nil
		}
		if q.Cursor != "page2" || q.Limit != 2 {
			t.Fatal(q)
		}
		return compatibilityPage(second), nil
	}
	repo := &stubCampaignReader{rows: []Campaign{{ID: "a-legacy", Status: "active"}}, counts: map[string]int{"a-legacy": 5}}
	rows, err := compatibilityService(repo, p).list(t.Context(), "", 3, "")
	if err != nil || len(rows) != 3 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	if rows[0].ID != "v2-z" || rows[1].ID != "v2-a" || rows[2].ID != "a-legacy" || repo.limit != 1 || p.meCalls != 0 {
		t.Fatal(rows)
	}
	if rows[0].ParticipantCount != 3 || rows[0].Participants != 3 || rows[2].ParticipantCount != 5 {
		t.Fatal("wrong count authority")
	}
	raw, _ := json.Marshal(rows[0])
	for _, key := range []string{"createdAt", "updatedAt", "isParticipating", "hasReminder", "acceptedRevision"} {
		if strings.Contains(string(raw), key) {
			t.Fatalf("invented or private field: %s", raw)
		}
	}
	if rows[0].Banner != nil || rows[0].Metadata != nil {
		t.Fatal("versioned content borrowed legacy free fields")
	}
}

func TestCompatibleReadRefusesUnrepresentableVersionedData(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*application.PublicCampaignView)
		status int
	}{
		{"unsupported", func(v *application.PublicCampaignView) { v.ContentState = "unsupported"; v.Content = nil }, 422},
		{"paused", func(v *application.PublicCampaignView) { v.Mode = "paused" }, 422},
		{"archived", func(v *application.PublicCampaignView) { v.Mode = "archived" }, 422},
		{"unknown count", func(v *application.PublicCampaignView) { v.StatisticsState = "unavailable"; v.ParticipantCount = nil }, 503},
		{"unsafe numeric count", func(v *application.PublicCampaignView) { n := "9007199254740992"; v.ParticipantCount = &n }, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			view := compatibilityView(t, "v2-id")
			tc.change(&view)
			p := compatibilityReader(view)
			repo := &stubCampaignReader{rows: []Campaign{{ID: "v2-id", Title: "PRIVATE SHADOW"}}}
			svc := compatibilityService(repo, p)
			_, err := svc.FindByID(t.Context(), "v2-id")
			compatibilityError(t, err, tc.status)
			if repo.calls != 0 {
				t.Fatal("unrepresentable content fell through to raw shadow")
			}
		})
	}
}

// The list must survive an administrator pausing or archiving one campaign:
// unrepresentable states are omitted (detail still refuses them explicitly),
// while integrity failures keep failing the whole read instead of hiding data.
func TestCompatibleListOmitsUnrepresentableButFailsOnIntegrity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*application.PublicCampaignView)
		status int
	}{
		{"unsupported", func(v *application.PublicCampaignView) { v.ContentState = "unsupported"; v.Content = nil }, 0},
		{"paused", func(v *application.PublicCampaignView) { v.Mode = "paused" }, 0},
		{"archived", func(v *application.PublicCampaignView) { v.Mode = "archived" }, 0},
		{"unknown count", func(v *application.PublicCampaignView) { v.StatisticsState = "unavailable"; v.ParticipantCount = nil }, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hidden, shown := compatibilityView(t, "v2-hidden"), compatibilityView(t, "v2-shown")
			tc.change(&hidden)
			p := compatibilityReader(shown)
			p.listFn = func(context.Context, application.ListQuery) (application.ListResponse, error) {
				return compatibilityPage(hidden, shown), nil
			}
			repo := &stubCampaignReader{rows: []Campaign{{ID: "a-legacy", Status: "active"}}, counts: map[string]int{"a-legacy": 1}}
			rows, err := compatibilityService(repo, p).list(t.Context(), "", 3, "")
			if tc.status != 0 {
				compatibilityError(t, err, tc.status)
				return
			}
			if err != nil || len(rows) != 2 || rows[0].ID != "v2-shown" || rows[1].ID != "a-legacy" {
				t.Fatalf("rows=%v err=%v", rows, err)
			}
		})
	}
}

func TestCompatibleDetailFallsBackOnlyOnPublishedNotFound(t *testing.T) {
	p := compatibilityReader(compatibilityView(t, "v2-id"))
	repo := &stubCampaignReader{rows: []Campaign{{ID: "legacy-id", Status: "active"}}, counts: map[string]int{"legacy-id": 7}}
	svc := compatibilityService(repo, p)
	p.detailFn = func(context.Context, string) (application.DetailResponse, error) {
		return application.DetailResponse{}, errors.New("database down")
	}
	if _, err := svc.FindByID(t.Context(), "legacy-id"); err == nil || repo.calls != 0 {
		t.Fatal("dependency fault used legacy fallback")
	}
	p.detailFn = func(context.Context, string) (application.DetailResponse, error) {
		return application.DetailResponse{}, &v2.DomainError{Code: "CAMPAIGN_NOT_FOUND", Status: 404}
	}
	row, err := svc.FindByID(t.Context(), "legacy-id")
	if err != nil || row.ParticipantCount != 7 || repo.calls != 1 {
		t.Fatalf("%+v %v", row, err)
	}
	_, err = NewCompatibleService(NewRepository(nil), nil).FindByID(t.Context(), "legacy-id")
	compatibilityError(t, err, 503)
}

func TestCompatiblePrivateReadUsesStrictCredentialAndRejectsUnknownOrMixedSnapshot(t *testing.T) {
	v := campaignAuth.Verifier
	view := compatibilityView(t, "v2-id")
	p := compatibilityReader(view)
	zero := "0"
	state := application.MyCampaignView{ID: view.ID, PublicationVersion: view.PublicationVersion, InteractionVersion: view.InteractionVersion, Participation: application.PersonalStateView{State: "absent", StateVersion: &zero}, Reminder: application.PersonalStateView{State: "absent", StateVersion: &zero}}
	p.meFn = func(ctx context.Context, actor string, ids []string) (application.MeResponse, error) {
		claims, err := auth.RevalidateStrict(ctx, v)
		if err != nil || claims.Sub != actor || actor != testWallet || len(ids) != 1 || ids[0] != view.ID {
			t.Fatalf("private credential lost: %v", err)
		}
		return application.MeResponse{SchemaVersion: 1, ServerNow: compatibilityNow, AsOf: compatibilityNow, Items: []application.MyCampaignView{state}}, nil
	}
	h := Router(compatibilityService(&stubCampaignReader{}, p), strictResolver(v), auth.StrictMiddleware(v))
	request := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/v2-id", nil)
		r.Header.Set("Authorization", "Bearer "+campaignToken(t))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	w := request()
	if w.Code != 200 || w.Header().Get("Cache-Control") != "private, no-store" || !strings.Contains(w.Body.String(), `"isParticipating":false`) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	state.Participation = application.PersonalStateView{State: "unknown"}
	w = request()
	if w.Code != 503 {
		t.Fatalf("unknown became false: %d", w.Code)
	}
	state.Participation = application.PersonalStateView{State: "absent", StateVersion: &zero}
	state.InteractionVersion = "3"
	w = request()
	if w.Code != 503 {
		t.Fatal("mixed public/private versions succeeded")
	}
	if p.meCalls != 3 {
		t.Fatal(p.meCalls)
	}
}

func TestCompatiblePaginationRejectsDuplicatesNonprogressAndDeadline(t *testing.T) {
	for _, kind := range []string{"cursor", "id", "empty", "overflow"} {
		t.Run(kind, func(t *testing.T) {
			view := compatibilityView(t, "v2-a")
			p := compatibilityReader(view)
			p.listFn = func(ctx context.Context, q application.ListQuery) (application.ListResponse, error) {
				page := compatibilityPage(view)
				next := "same-cursor"
				page.NextCursor = &next
				if q.Cursor != "" && kind != "id" {
					row := view
					row.ID = "v2-b"
					page.Items = []application.PublicCampaignView{row}
				}
				if kind == "empty" {
					page.Items = []application.PublicCampaignView{}
				}
				if kind == "overflow" {
					page.Items = []application.PublicCampaignView{view, view, view, view}
				}
				return page, nil
			}
			_, err := compatibilityService(&stubCampaignReader{}, p).list(t.Context(), "", 3, "")
			compatibilityError(t, err, 503)
		})
	}
	p := compatibilityReader(compatibilityView(t, "v2-a"))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := compatibilityService(&stubCampaignReader{}, p).List(ctx, "")
	compatibilityError(t, err, 503)
	if p.listCalls != 0 {
		t.Fatal("expired budget reached source")
	}
}

func TestAuthenticatedLegacyReadRequiresGuard(t *testing.T) {
	v := campaignAuth.Verifier
	p := compatibilityReader(compatibilityView(t, "v2-a"))
	r := httptest.NewRequest(http.MethodGet, "/v2-a", nil)
	r.Header.Set("Authorization", "Bearer "+campaignToken(t))
	r = r.WithContext(auth.WithAddress(r.Context(), testWallet))
	w := httptest.NewRecorder()
	Router(compatibilityService(&stubCampaignReader{}, p), strictResolver(v), nil).ServeHTTP(w, r)
	if w.Code != 401 || p.detailCalls != 0 || p.meCalls != 0 {
		t.Fatal("WithAddress forged personal read authority")
	}
}
