package campaignhttp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/application"
)

// variantReader answers with the view a test chooses and records the
// representation each handler asked for.
type variantReader struct {
	Reader
	item     application.PublicCampaignView
	history  []application.HistoryView
	received []v2.Variants
}

func (r *variantReader) List(_ context.Context, q application.ListQuery) (application.ListResponse, error) {
	r.received = append(r.received, q.Variants)
	return application.ListResponse{SchemaVersion: 1, ServerNow: "2026-10-08T00:00:00Z", AsOf: "2026-10-08T00:00:00Z", Items: []application.PublicCampaignView{r.item}}, nil
}
func (r *variantReader) Detail(_ context.Context, _ string, variants v2.Variants) (application.DetailResponse, error) {
	r.received = append(r.received, variants)
	return application.DetailResponse{SchemaVersion: 1, ServerNow: "2026-10-08T00:00:00Z", AsOf: "2026-10-08T00:00:00Z", Item: r.item}, nil
}
func (r *variantReader) History(_ context.Context, _ string, q application.HistoryQuery) (application.HistoryResponse, error) {
	r.received = append(r.received, q.Variants)
	return application.HistoryResponse{SchemaVersion: 1, ServerNow: "2026-10-08T00:00:00Z", AsOf: "2026-10-08T00:00:00Z", Items: r.history}, nil
}

func baselineItem(negotiated bool) application.PublicCampaignView {
	item := application.PublicCampaignView{ID: "legacy-1", RevisionID: "0199c182-9d88-7000-8000-000000000003", PublicationVersion: "1", InteractionVersion: "2",
		Mode: "enabled", EffectivePhase: "active", ContentState: "unsupported", MaterialTermsHash: strings.Repeat("b", 64), ContentHash: strings.Repeat("c", 64), StatisticsState: "unavailable"}
	if negotiated {
		item.ContentState = v2.ContentStateLegacyBaseline
		item.Baseline = &v2.QueryLegacyBaselineView{TemplateID: v2.LegacyBaselineTemplate, Title: "Legacy", StartAt: "2026-10-01T00:00:00.000Z", EndAt: "2026-11-01T00:00:00.000Z", CapturedAt: "2026-10-07T00:00:00Z"}
	}
	return item
}

func importEntry() application.HistoryView {
	at, recorded, entry := "2026-10-07T12:30:00Z", "2020-01-15T00:00:00Z", v2.HistoryEntryLegacyImport
	version, status, provenance := "1", "joined", "legacyUnknown"
	return application.HistoryView{Entry: &entry, CampaignID: "legacy-1", Kind: "join", InteractionVersion: "1", OccurredAt: at, SnapshotState: "legacyUnknown",
		State:        application.PersonalStateView{State: "known", StateVersion: &version, Status: &status, AcceptanceProvenance: &provenance, AcceptedAt: &recorded, ChangedAt: &at},
		LegacyImport: &v2.QueryHistoryImportView{ImportedAt: at, LegacyRecordedAt: &recorded}}
}

func variantRequest(t *testing.T, router http.Handler, path, header, authorization string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if header != "" {
		r.Header.Set(v2.VariantsHeader, header)
	}
	if authorization != "" {
		r.Header.Set("Authorization", authorization)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	return w
}

func TestReadsPassTheNegotiatedRepresentationAndVary(t *testing.T) {
	reader := &variantReader{item: baselineItem(false)}
	router := Router(Config{Queries: reader, Verifier: testAuth.Verifier, Enabled: true})
	for _, path := range []string{"/campaigns", "/campaigns/legacy-1"} {
		for header, want := range map[string]v2.Variants{
			"":                                       {},
			"legacyBaselineV1":                       {LegacyBaseline: true},
			"legacyImportHistoryV1, unknownFutureV9": {LegacyImportHistory: true},
			"legacyBaselineV1; q=1":                  {},
		} {
			reader.received = nil
			w := variantRequest(t, router, path, header, "")
			if w.Code != http.StatusOK || len(reader.received) != 1 || reader.received[0] != want {
				t.Fatalf("%s %q: status %d received %+v", path, header, w.Code, reader.received)
			}
			if !strings.Contains(strings.Join(w.Header().Values("Vary"), ","), v2.VariantsHeader) {
				t.Fatalf("%s %q: missing Vary", path, header)
			}
		}
	}
	// A header never loosens the closed query: unknown parameters still fail.
	if w := variantRequest(t, router, "/campaigns?variants=legacyBaselineV1", "legacyBaselineV1", ""); w.Code != http.StatusBadRequest {
		t.Fatalf("query parameter: %d", w.Code)
	}
}

func TestReadsNeverSendAShapeTheClientDidNotNegotiate(t *testing.T) {
	reader := &variantReader{item: baselineItem(true)}
	router := Router(Config{Queries: reader, Verifier: testAuth.Verifier, Enabled: true})
	for _, path := range []string{"/campaigns", "/campaigns/legacy-1"} {
		if w := variantRequest(t, router, path, "", ""); w.Code != http.StatusServiceUnavailable || code(t, w) != "CAMPAIGN_RESPONSE_INVALID" || strings.Contains(w.Body.String(), "baseline") {
			t.Fatalf("%s leaked a baseline to a base client: %d %s", path, w.Code, w.Body.String())
		}
		if w := variantRequest(t, router, path, "legacyBaselineV1", ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"contentState":"legacyBaseline"`) {
			t.Fatalf("%s negotiated: %d %s", path, w.Code, w.Body.String())
		}
	}
	reader.history = []application.HistoryView{importEntry()}
	authorization := bearer(t)
	if w := variantRequest(t, router, "/campaigns/me/history", "", authorization); w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), "legacyImport") {
		t.Fatalf("history leaked an import to a base client: %d %s", w.Code, w.Body.String())
	}
	w := variantRequest(t, router, "/campaigns/me/history", "legacyImportHistoryV1", authorization)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"entry":"legacyImport"`) || !strings.Contains(w.Body.String(), `"operation":null`) {
		t.Fatalf("negotiated history: %d %s", w.Code, w.Body.String())
	}
	if last := reader.received[len(reader.received)-1]; last != (v2.Variants{LegacyImportHistory: true}) {
		t.Fatalf("history representation: %+v", last)
	}
}
