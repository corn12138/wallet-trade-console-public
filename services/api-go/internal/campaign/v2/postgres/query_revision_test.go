package postgres

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
)

func rawReadFixture(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile("../../../../../../packages/shared/contracts/fixtures/campaign-v2-revision.json")
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]json.RawMessage
	if err = json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}
func decodeReadFixture(t *testing.T, raw map[string]json.RawMessage, schema, canonical int64) (readRevision, error) {
	t.Helper()
	encoded, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	contentHash, _ := v2.CanonicalHash(json.RawMessage(encoded))
	termsHash, _ := v2.CanonicalHash(raw["materialTerms"])
	return decodeReadRevision("11111111-1111-4111-8111-111111111111", "campaign-1", encoded, schema, canonical, termsHash, contentHash)
}
func TestReadRevisionRetainsUnsupportedMetadata(t *testing.T) {
	for _, kind := range []string{"schema", "canonical", "template"} {
		t.Run(kind, func(t *testing.T) {
			raw := rawReadFixture(t)
			schema, canonical := int64(1), int64(1)
			switch kind {
			case "schema":
				schema = 2
				raw["schemaVersion"] = json.RawMessage(`2`)
			case "canonical":
				canonical = 2
				raw["termsCanonicalVersion"] = json.RawMessage(`2`)
			case "template":
				raw["templateId"] = json.RawMessage(`"futureCard"`)
			}
			result, err := decodeReadFixture(t, raw, schema, canonical)
			if err != nil || result.supported || result.revision.ContentHash == "" || result.revision.ID == "" {
				t.Fatalf("unsupported metadata lost: %v", err)
			}
		})
	}
}
func TestReadRevisionRejectsHashMismatchAndColumnDrift(t *testing.T) {
	raw := rawReadFixture(t)
	result, err := decodeReadFixture(t, raw, 1, 1)
	if err != nil || !result.supported {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(raw)
	if _, err = decodeReadRevision(result.revision.ID, result.revision.CampaignID, encoded, 1, 1, result.revision.MaterialTermsHash, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err == nil {
		t.Fatal("content hash mismatch accepted")
	}
	if _, err = decodeReadFixture(t, raw, 2, 1); err == nil {
		t.Fatal("duplicated schema column drift accepted")
	}
	raw["extra"] = json.RawMessage(`true`)
	if _, err = decodeReadFixture(t, raw, 1, 1); err == nil {
		t.Fatal("known schema extra property accepted")
	}
}
func TestPersonalReadAbsentUnknownAndCorruptRemainDistinct(t *testing.T) {
	absent := absentState()
	if absent.State != "absent" || absent.StateVersion == nil || *absent.StateVersion != "0" || absent.Status != nil {
		t.Fatal("absence not explicit")
	}
	unknown, err := (personalRow{kind: "join", status: "future"}).view()
	if err != nil || unknown.State != "unknown" || unknown.StateVersion != nil {
		t.Fatal("future state guessed zero")
	}
	if _, err = (personalRow{kind: "join", status: "joined", version: 1, provenance: "neverAccepted", changedAt: time.Now()}).view(); err == nil {
		t.Fatal("positive neverAccepted accepted")
	}
	tombstone, err := (personalRow{kind: "join", status: "left", version: 1, provenance: "neverAccepted", changedAt: time.Now()}).view()
	if err != nil || tombstone.State != "known" || *tombstone.StateVersion != "1" || tombstone.AcceptedAt != nil || tombstone.ChangedAt == nil {
		t.Fatal("cancellation fabricated acceptance")
	}
}

func TestReadUnknownSchemaDoesNotInterpretFutureFieldTypes(t *testing.T) {
	raw := rawReadFixture(t)
	raw["schemaVersion"] = json.RawMessage(`2`)
	raw["displayContent"] = json.RawMessage(`{"layout":"future","sortOrder":{"rank":"new-shape"}}`)
	for index := 0; index < 30; index++ {
		value, err := decodeReadFixture(t, raw, 2, 1)
		if err != nil || value.supported {
			t.Fatalf("future schema interpreted in property iteration order: %v", err)
		}
	}
}

func TestReadUnknownTemplateDoesNotInterpretFutureFieldTypes(t *testing.T) {
	raw := rawReadFixture(t)
	raw["templateId"] = json.RawMessage(`"futureCard"`)
	raw["displayContent"] = json.RawMessage(`{"layout":"future","sortOrder":{"rank":"new-shape"}}`)
	for index := 0; index < 30; index++ {
		value, err := decodeReadFixture(t, raw, 1, 1)
		if err != nil || value.supported {
			t.Fatalf("future template interpreted in property iteration order: %v", err)
		}
	}
}

func TestReadFutureDiscriminatorsPrecedeUnrecognizedChildShapes(t *testing.T) {
	cases := []struct {
		name  string
		path  []string
		value any
	}{
		{"joinKind", []string{"materialTerms", "rules", "join", "kind"}, "futureJoin"},
		{"joinVersion", []string{"materialTerms", "rules", "join", "version"}, 2},
		{"reminderKind", []string{"materialTerms", "rules", "reminder", "kind"}, "futureReminder"},
		{"reminderVersion", []string{"materialTerms", "rules", "reminder", "version"}, 2},
		{"layout", []string{"displayContent", "layout"}, "futureLayout"},
		{"cta", []string{"cta", "actionKind"}, "futureCTA"},
		{"reward", []string{"materialTerms", "rewardDelivery"}, "futureReward"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			fixture := rawReadFixture(t)
			encoded, _ := json.Marshal(fixture)
			var document map[string]any
			if err := json.Unmarshal(encoded, &document); err != nil {
				t.Fatal(err)
			}
			current := document
			for _, name := range test.path[:len(test.path)-1] {
				current = current[name].(map[string]any)
			}
			current[test.path[len(test.path)-1]] = test.value
			document["displayContent"].(map[string]any)["sortOrder"] = map[string]any{"futureRank": true}
			document["materialTerms"].(map[string]any)["rules"].(map[string]any)["join"].(map[string]any)["params"] = []any{"future-params"}
			encoded, _ = json.Marshal(document)
			var raw map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &raw); err != nil {
				t.Fatal(err)
			}
			for index := 0; index < 10; index++ {
				value, err := decodeReadFixture(t, raw, 1, 1)
				if err != nil || value.supported {
					t.Fatalf("future discriminator inspected unknown child: %v", err)
				}
			}
		})
	}
}
