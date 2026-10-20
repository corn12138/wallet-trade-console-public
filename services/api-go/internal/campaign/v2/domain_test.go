package v2

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

var capabilities = Capabilities{Templates: map[string]bool{"campaignCardV1": true}, MediaAssets: map[string]bool{"approved-banner": true}}

func revisionFixture(t *testing.T) RevisionInput {
	t.Helper()
	raw, err := os.ReadFile("../../../../../packages/shared/contracts/fixtures/campaign-v2-revision.json")
	if err != nil {
		t.Fatal(err)
	}
	input, err := DecodeRevision(raw)
	if err != nil {
		t.Fatal(err)
	}
	return input
}

func publicationFixture(t *testing.T) Publication {
	t.Helper()
	revision, err := NewRevision("revision-1", "campaign-1", revisionFixture(t), capabilities)
	if err != nil {
		t.Fatal(err)
	}
	return Publication{CampaignID: "campaign-1", Version: 1, Mode: "enabled", Active: &revision}
}

func errorCode(t *testing.T, err error, code string) {
	t.Helper()
	var domain *DomainError
	if !errors.As(err, &domain) || domain.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}

func TestRevisionContractClosesExecutableAndFreeCopyBackdoors(t *testing.T) {
	for _, tc := range []struct {
		name, path, field string
		value             any
		code              string
	}{
		{"free-display-text", "displayContent", "title", "different promise", "CAMPAIGN_INVALID_INPUT"},
		{"html", "displayContent", "html", "<script>x</script>", "CAMPAIGN_INVALID_INPUT"},
		{"remote-module", "displayContent", "moduleUrl", "https://attacker.example", "CAMPAIGN_INVALID_INPUT"},
		{"calldata", "cta", "calldata", "0x1234", "CAMPAIGN_INVALID_INPUT"},
		{"RPC", "cta", "rpcUrl", "https://attacker.example", "CAMPAIGN_INVALID_INPUT"},
		{"unknown-schema", "", "schemaVersion", 2, "CAMPAIGN_UNSUPPORTED"},
		{"future-template", "", "templateId", "futureV9", "CAMPAIGN_UNSUPPORTED"},
		{"fractional-order", "displayContent", "sortOrder", 1.5, "CAMPAIGN_INVALID_INPUT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(revisionFixture(t))
			var input map[string]any
			_ = json.Unmarshal(raw, &input)
			target := input
			if tc.path != "" {
				target = input[tc.path].(map[string]any)
			}
			target[tc.field] = tc.value
			raw, _ = json.Marshal(input)
			_, err := DecodeRevision(raw)
			errorCode(t, err, tc.code)
		})
	}
	input := revisionFixture(t)
	input.MaterialTerms.Copy.Zh.Title = " "
	errorCode(t, ValidateRevision(input, capabilities), "CAMPAIGN_INVALID_INPUT")
	input = revisionFixture(t)
	input.MaterialTerms.StartAt = "2026-02-31T00:00:00Z"
	errorCode(t, ValidateRevision(input, capabilities), "CAMPAIGN_INVALID_INPUT")
	input = revisionFixture(t)
	input.MaterialTerms.Copy.En.Description = "<img src=x>"
	errorCode(t, ValidateRevision(input, capabilities), "CAMPAIGN_INVALID_INPUT")
	raw, _ := json.Marshal(revisionFixture(t))
	_, err := DecodeRevision(append(raw, []byte("{}")...))
	errorCode(t, err, "CAMPAIGN_INVALID_INPUT")
}

func TestMaterialHashSeparatesPresentationAndLocksEveryLocale(t *testing.T) {
	base := publicationFixture(t)
	input := revisionFixture(t)
	input.DisplayContent.Layout = "featured"
	display, err := NewRevision("display", base.CampaignID, input, capabilities)
	if err != nil {
		t.Fatal(err)
	}
	if display.MaterialTermsHash != base.Active.MaterialTermsHash || display.ContentHash == base.Active.ContentHash {
		t.Fatal("presentation and terms hashes conflated")
	}
	input.MaterialTerms.Copy.Zh.Title = "新的奖励承诺"
	changed, err := NewRevision("changed", base.CampaignID, input, capabilities)
	if err != nil {
		t.Fatal(err)
	}
	if changed.MaterialTermsHash == base.Active.MaterialTermsHash {
		t.Fatal("localized title escaped terms hash")
	}
	for _, now := range []time.Time{time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)} {
		if _, err = DecidePublication(base, "publish", 1, &display, false, now); err != nil {
			t.Fatal(err)
		}
		_, err = DecidePublication(base, "publish", 1, &changed, false, now)
		errorCode(t, err, "CAMPAIGN_TERMS_LOCKED")
	}
	base.HasAcceptedHistory = true
	_, err = DecidePublication(base, "publish", 1, &changed, false, time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC))
	errorCode(t, err, "CAMPAIGN_TERMS_LOCKED")
}

func TestCandidateCannotMoveTheStartToEscapeTermsLock(t *testing.T) {
	base := publicationFixture(t)
	input := revisionFixture(t)
	input.MaterialTerms.StartAt = "2026-10-10T00:00:00Z"
	input.MaterialTerms.EndAt = "2026-10-11T00:00:00Z"
	later, err := NewRevision("later", base.CampaignID, input, capabilities)
	if err != nil {
		t.Fatal(err)
	}
	_, err = DecidePublication(base, "publish", 1, &later, false, time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC))
	errorCode(t, err, "CAMPAIGN_TERMS_LOCKED")
	_, err = DecidePublication(base, "pause", 0, nil, false, time.Now())
	errorCode(t, err, "CAMPAIGN_VERSION_CONFLICT")
	archived, err := DecidePublication(base, "archive", 1, nil, false, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	_, err = DecidePublication(archived, "rollback", 2, base.Active, true, time.Now())
	errorCode(t, err, "CAMPAIGN_ARCHIVED")
}

func TestCancellationTombstoneAndAcceptanceHistory(t *testing.T) {
	pub := publicationFixture(t)
	now := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	cancelled, err := DecideInteraction(pub, nil, "join", false, 0, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.After.Version != 1 || cancelled.After.Acceptance.Provenance != "neverAccepted" || cancelled.After.Acceptance.RevisionID != nil {
		t.Fatal("cancellation invented acceptance")
	}
	_, err = DecideInteraction(pub, &cancelled.After, "join", true, 0, &pub.Version, now)
	errorCode(t, err, "CAMPAIGN_STATE_CONFLICT")
	joined, err := DecideInteraction(pub, &cancelled.After, "join", true, 1, &pub.Version, now)
	if err != nil {
		t.Fatal(err)
	}
	pub.Mode = "archived"
	left, err := DecideInteraction(pub, &joined.After, "join", false, 2, nil, now.Add(48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if left.After.Acceptance.Provenance != "verified" || *left.After.Acceptance.RevisionID != "revision-1" {
		t.Fatal("cancel erased accepted terms")
	}
	retry, err := DecideInteraction(pub, &left.After, "join", false, 3, nil, now)
	if err != nil || retry.Changed || retry.After.Version != 3 {
		t.Fatal("same-state cancellation changed version")
	}
}

func TestAdmissionUsesHalfOpenWindow(t *testing.T) {
	pub := publicationFixture(t)
	start := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	for _, tc := range []struct {
		kind    string
		now     time.Time
		allowed bool
	}{{"join", start.Add(-time.Nanosecond), false}, {"join", start, true}, {"join", end, false}, {"reminder", start.Add(-time.Nanosecond), true}, {"reminder", start, false}} {
		_, err := DecideInteraction(pub, nil, tc.kind, true, 0, &pub.Version, tc.now)
		if tc.allowed && err != nil {
			t.Fatal(err)
		}
		if !tc.allowed {
			errorCode(t, err, "CAMPAIGN_NOT_OPEN")
		}
	}
}

func TestNewCommandUUIDv7AcceptanceWindow(t *testing.T) {
	now := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	idAt := func(at time.Time) string {
		hex := fmt.Sprintf("%012x", at.UnixMilli())
		return hex[:8] + "-" + hex[8:] + "-7000-8000-000000000000"
	}
	for _, at := range []time.Time{now, now.Add(-7 * 24 * time.Hour), now.Add(5 * time.Minute)} {
		if err := ValidateNewCommandID(idAt(at), now); err != nil {
			t.Fatal(err)
		}
	}
	errorCode(t, ValidateNewCommandID(idAt(now.Add(-7*24*time.Hour-time.Millisecond)), now), "CAMPAIGN_COMMAND_EXPIRED")
	errorCode(t, ValidateNewCommandID(idAt(now.Add(5*time.Minute+time.Millisecond)), now), "CAMPAIGN_INVALID_INPUT")
	errorCode(t, ValidateNewCommandID(strings.Replace(idAt(now), "-7000-", "-4000-", 1), now), "CAMPAIGN_INVALID_INPUT")
}
