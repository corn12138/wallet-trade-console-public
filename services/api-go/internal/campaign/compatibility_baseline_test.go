package campaign

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/application"
)

func baselineView(t *testing.T, id string) (application.PublicCampaignView, v2.LegacyBaseline) {
	t.Helper()
	description, banner, reward, metadata := "Original", "https://cdn.example/b.png", "100 USDC", `{"tier": 9007199254740993}`
	input := v2.LegacyBaselineInput{SchemaVersion: 1, TermsCanonicalVersion: 1, TemplateID: v2.LegacyBaselineTemplate,
		MaterialTerms: v2.LegacyMaterialTerms{Legacy: v2.LegacyTerms{Title: "Legacy title", Description: &description, Reward: &reward,
			StartAt: "2020-01-01T00:00:00.123Z", EndAt: "2999-01-01T00:00:00.000Z"}},
		LegacyRecord: v2.LegacyRecord{Banner: &banner, Status: "active", Participants: 7, MetadataJSON: &metadata, CreatedAt: "2019-12-01T00:00:00.000Z", UpdatedAt: "2019-12-02T00:00:00.456Z"},
		Source: v2.LegacySource{RunID: "11111111-1111-4111-8111-111111111111", CapturedAt: "2026-10-09T00:00:00Z", Algorithm: "legacy-row-json/v2",
			LegacyRowSHA256: strings.Repeat("a", 64), InteractionsSHA256: strings.Repeat("b", 64)}}
	terms, content, err := input.Hashes()
	if err != nil {
		t.Fatal(err)
	}
	start, _ := v2.ParseLegacyTime(input.MaterialTerms.Legacy.StartAt)
	end, _ := v2.ParseLegacyTime(input.MaterialTerms.Legacy.EndAt)
	revision := "22222222-2222-4222-8222-222222222222"
	baseline := v2.LegacyBaseline{RevisionID: revision, CampaignID: id, Input: input, Start: start, End: end, MaterialTermsHash: terms, ContentHash: content}
	count := "2"
	view := application.PublicCampaignView{ID: id, RevisionID: revision, PublicationVersion: "1", InteractionVersion: "3", Mode: "enabled", EffectivePhase: "active",
		ContentState: "unsupported", MaterialTermsHash: terms, ContentHash: content, ParticipantCount: &count, StatisticsState: "ready"}
	return view, baseline
}

// An imported campaign answers the legacy DTO with its original values: the
// legacy counter, banner, metadata (same decoder as the legacy repository)
// and lifecycle times, with the live count and phase from the versioned model.
func TestBaselineCompatibilityKeepsTheLegacyValues(t *testing.T) {
	view, baseline := baselineView(t, "legacy-1")
	row, err := publicCompatibilityView(view, &baseline)
	if err != nil {
		t.Fatal(err)
	}
	created := time.Date(2019, 12, 1, 0, 0, 0, 0, time.UTC)
	if row.Title != "Legacy title" || *row.Description != "Original" || *row.Banner != "https://cdn.example/b.png" || *row.Reward != "100 USDC" ||
		row.Participants != 7 || row.ParticipantCount != 2 || row.Status != "active" || !row.CreatedAt.Equal(created) ||
		!row.StartDate.Equal(time.Date(2020, 1, 1, 0, 0, 0, 123000000, time.UTC)) || !row.UpdatedAt.Equal(time.Date(2019, 12, 2, 0, 0, 0, 456000000, time.UTC)) {
		t.Fatalf("legacy values lost: %+v", row)
	}
	raw, _ := json.Marshal(row.Metadata)
	if want, _ := json.Marshal(decodeMetadata([]byte(*baseline.Input.LegacyRecord.MetadataJSON))); string(raw) != string(want) {
		t.Fatalf("metadata must decode exactly as the legacy repository decodes it: %s", raw)
	}
	if _, err = publicCompatibilityView(view, nil); err == nil {
		t.Fatal("without its baseline an unsupported view stays unrepresentable")
	}
	tampered := baseline
	tampered.ContentHash = strings.Repeat("c", 64)
	_, err = publicCompatibilityView(view, &tampered)
	compatibilityError(t, err, 503)
	paused := view
	paused.Mode = "paused"
	_, err = publicCompatibilityView(paused, &baseline)
	compatibilityError(t, err, 422)
	unknown := view
	unknown.StatisticsState, unknown.ParticipantCount = "unavailable", nil
	_, err = publicCompatibilityView(unknown, &baseline)
	compatibilityError(t, err, 503)
}

func TestLegacyUnknownStatesMapToTheLegacyFlags(t *testing.T) {
	version, joined, left, provenance, at := "1", "joined", "left", "legacyUnknown", "2020-01-15T00:00:00Z"
	state := application.PersonalStateView{State: "known", StateVersion: &version, Status: &joined, AcceptanceProvenance: &provenance, AcceptedAt: &at, ChangedAt: &at}
	if value, err := compatiblePersonal(state, "joined", "left"); err != nil || !value {
		t.Fatalf("an imported participation is participating: %v %v", value, err)
	}
	state.Status = &left
	if value, err := compatiblePersonal(state, "joined", "left"); err != nil || value {
		t.Fatalf("a cancelled import is not participating: %v %v", value, err)
	}
	revision := "22222222-2222-4222-8222-222222222222"
	state.AcceptedRevisionID = &revision
	if _, err := compatiblePersonal(state, "joined", "left"); err == nil {
		t.Fatal("a legacyUnknown state never names accepted terms")
	}
}

// The list fetches baselines once per page and refuses one that answers for
// another revision.
func TestCompatibleListMapsBaselinesAndRefusesMismatches(t *testing.T) {
	view, baseline := baselineView(t, "legacy-1")
	probe := compatibilityReader(view)
	probe.baselinesFn = func(_ context.Context, refs []application.RevisionRef) (map[application.RevisionRef]v2.LegacyBaseline, error) {
		if len(refs) != 1 || refs[0].CampaignID != view.ID || refs[0].RevisionID != view.RevisionID {
			t.Fatalf("baseline refs %+v", refs)
		}
		return map[application.RevisionRef]v2.LegacyBaseline{refs[0]: baseline}, nil
	}
	svc := compatibilityService(&stubCampaignReader{}, probe)
	rows, err := svc.List(t.Context(), "")
	if err != nil || len(rows) != 1 || rows[0].ID != view.ID || rows[0].Participants != 7 || probe.baselineCalls != 1 {
		t.Fatalf("baseline list: %+v %v calls=%d", rows, err, probe.baselineCalls)
	}
	other := baseline
	other.RevisionID = "33333333-3333-4333-8333-333333333333"
	probe.baselinesFn = func(_ context.Context, refs []application.RevisionRef) (map[application.RevisionRef]v2.LegacyBaseline, error) {
		return map[application.RevisionRef]v2.LegacyBaseline{refs[0]: other}, nil
	}
	_, err = svc.List(t.Context(), "")
	compatibilityError(t, err, 503)
	_, err = svc.FindByID(t.Context(), view.ID)
	compatibilityError(t, err, 503)
}
