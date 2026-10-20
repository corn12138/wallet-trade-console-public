package v2

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

const fixtures = "../../../../../packages/shared/contracts/fixtures/"

type variantCase struct {
	Name, Contract, Raw, Current, Semantic, StrictClient string
	Variants                                             []string
}

// strictClientSchema is the schema every client built before variant
// negotiation decodes with; the base representation must stay acceptable to it.
func strictClientSchema(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(fixtures + "campaign-v2-schema-strict-client-2026-10-09.json")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func decodeOutcome(err error) string {
	var domain *DomainError
	switch {
	case err == nil:
		return "valid"
	case errors.As(err, &domain) && domain.Code == "CAMPAIGN_UNSUPPORTED":
		return "unsupported"
	default:
		return "invalid"
	}
}

func semanticOutcome(t *testing.T, contract, raw string, allowed Variants) string {
	t.Helper()
	var err error
	switch contract {
	case "QueryDetailResponse":
		var value QueryDetailResponse
		if decodeErr := DecodeContractResponse(contract, []byte(raw), &value); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		err = CheckPublicCampaignView(value.Item, allowed)
	case "QueryListResponse":
		var value QueryListResponse
		if decodeErr := DecodeContractResponse(contract, []byte(raw), &value); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		for _, item := range value.Items {
			if err = CheckPublicCampaignView(item, allowed); err != nil {
				break
			}
		}
	case "QueryHistoryResponse":
		var value QueryHistoryResponse
		if decodeErr := DecodeContractResponse(contract, []byte(raw), &value); decodeErr != nil {
			t.Fatal(decodeErr)
		}
		err = CheckHistoryResponse(value, allowed)
	default:
		t.Fatalf("no semantic check for %s", contract)
	}
	if err != nil {
		return "invalid"
	}
	return "valid"
}

// The same corpus runs in the Web (features/campaign/model/variants.spec.ts).
func TestSharedCampaignVariantCorpus(t *testing.T) {
	raw, err := os.ReadFile(fixtures + "campaign-v2-variant-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []variantCase
	if err = json.Unmarshal(raw, &cases); err != nil || len(cases) == 0 {
		t.Fatalf("variant corpus: %v", err)
	}
	strict := strictClientSchema(t)
	for _, item := range cases {
		t.Run(item.Name, func(t *testing.T) {
			var output any
			if got := decodeOutcome(DecodeContractResponse(item.Contract, []byte(item.Raw), &output)); got != item.Current {
				t.Fatalf("current schema: got %s want %s", got, item.Current)
			}
			if item.Current == "valid" {
				if got := semanticOutcome(t, item.Contract, item.Raw, ParseVariants([]string{strings.Join(item.Variants, ",")})); got != item.Semantic {
					t.Fatalf("cross-field rules: got %s want %s", got, item.Semantic)
				}
			}
			// The strict client's reason depends on which key it meets first;
			// only acceptance matters to it.
			got := "rejected"
			if DecodeContractResponseUnder(strict, item.Contract, []byte(item.Raw), &output) == nil {
				got = "valid"
			}
			if got != item.StrictClient {
				t.Fatalf("strict 2026-10-09 client: got %s want %s", got, item.StrictClient)
			}
		})
	}
}

func TestParseVariantsIgnoresUnknownAndMalformedHeaders(t *testing.T) {
	both := Variants{LegacyBaseline: true, LegacyImportHistory: true}
	for _, c := range []struct {
		name   string
		values []string
		want   Variants
	}{
		{"absent", nil, Variants{}},
		{"empty", []string{""}, Variants{}},
		{"one", []string{"legacyBaselineV1"}, Variants{LegacyBaseline: true}},
		{"both with spaces", []string{" legacyBaselineV1 ,\tlegacyImportHistoryV1 "}, both},
		{"repeated header lines", []string{"legacyBaselineV1", "legacyImportHistoryV1"}, both},
		{"unknown tokens are ignored", []string{"legacyBaselineV2, futureThing, legacyImportHistoryV1"}, Variants{LegacyImportHistory: true}},
		{"content capabilities", []string{"legacyBaselineV1, contentCapabilitiesV1"}, Variants{LegacyBaseline: true, ContentCapabilities: true}},
		{"empty items are ignored", []string{",,legacyBaselineV1,"}, Variants{LegacyBaseline: true}},
		{"case matters", []string{"LegacyBaselineV1"}, Variants{}},
		{"malformed token drops the header", []string{"legacyBaselineV1, legacy-import"}, Variants{}},
		{"quoted token drops the header", []string{`"legacyBaselineV1"`}, Variants{}},
		{"parameters drop the header", []string{"legacyBaselineV1;q=1"}, Variants{}},
		{"too many tokens", []string{strings.Repeat("x,", 16) + "legacyBaselineV1"}, Variants{}},
		{"too long", []string{"legacyBaselineV1," + strings.Repeat("a", 512)}, Variants{}},
		{"overlong token", []string{"a" + strings.Repeat("b", 64)}, Variants{}},
	} {
		if got := ParseVariants(c.values); got != c.want {
			t.Errorf("%s: got %+v want %+v", c.name, got, c.want)
		}
	}
}

func TestBaselinePublicViewCarriesOnlyDisplayFields(t *testing.T) {
	description, banner, metadata := "Read-only", "https://legacy.invalid/banner.png", `{"secret":1}`
	baseline := LegacyBaseline{Input: LegacyBaselineInput{
		MaterialTerms: LegacyMaterialTerms{Legacy: LegacyTerms{Title: "Legacy", Description: &description, StartAt: "2020-01-01T00:00:00.000Z", EndAt: "2020-02-01T00:00:00.000Z"}},
		LegacyRecord:  LegacyRecord{Banner: &banner, Status: "active", Participants: 77, MetadataJSON: &metadata, CreatedAt: "2020-01-01T00:00:00.000Z", UpdatedAt: "2020-01-01T00:00:00.000Z"},
		Source:        LegacySource{RunID: "0199c182-9d88-7000-8000-000000000001", CapturedAt: "2026-10-07T20:00:00.5+08:00", LegacyRowSHA256: strings.Repeat("d", 64), InteractionsSHA256: strings.Repeat("e", 64)},
	}}
	view, err := baseline.PublicView()
	if err != nil {
		t.Fatal(err)
	}
	if view.CapturedAt != "2026-10-07T12:00:00.5Z" || view.TemplateID != LegacyBaselineTemplate || view.Title != "Legacy" || view.Reward != nil {
		t.Fatalf("view: %+v", view)
	}
	raw, _ := json.Marshal(view)
	for _, private := range []string{banner, "secret", "77", "dddd", "eeee", "0199c182"} {
		if strings.Contains(string(raw), private) {
			t.Fatalf("baseline view exposes %q: %s", private, raw)
		}
	}
	baseline.Input.Source.CapturedAt = "not a time"
	if _, err = baseline.PublicView(); err == nil {
		t.Fatal("an invalid capture time must fail the view")
	}
}
