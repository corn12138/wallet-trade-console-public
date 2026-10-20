package legacyactivation

import (
	"encoding/json"
	"strings"
	"testing"
)

func found(fields map[string]string) answer {
	out := answer{found: true, fields: map[string]json.RawMessage{}}
	for key, value := range fields {
		out.fields[key] = json.RawMessage(value)
	}
	return out
}

func TestComparatorClassifiesEveryDifference(t *testing.T) {
	base := map[string]string{"id": `"c"`, "title": `"T"`, "participantCount": `2`, "isParticipating": `false`, "hasReminder": `false`}
	with := func(key, value string) answer {
		changed := map[string]string{}
		for k, v := range base {
			changed[k] = v
		}
		changed[key] = value
		return found(changed)
	}
	const wallet = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	for name, c := range map[string]struct {
		before, after answer
		wallet        string
		classes       string
	}{
		"identical":              {found(base), found(base), "", ""},
		"live count":             {found(base), with("participantCount", "1"), "", "participantCount"},
		"wallet state":           {found(base), with("isParticipating", "true"), wallet, "walletState"},
		"wallet field anonymous": {found(base), with("isParticipating", "true"), "", "field"},
		"original value":         {found(base), with("title", `"Changed"`), "", "field"},
		"visibility":             {found(base), answer{}, "", "visibility"},
		"refusal":                {found(base), answer{failure: "CAMPAIGN_CLIENT_UPGRADE_REQUIRED"}, "", "field"},
		"absent on both sides":   {answer{}, answer{}, "", ""},
	} {
		compare := &comparator{seen: map[string]bool{}}
		compare.compare(c.before, c.after, c.wallet)
		result := compare.result("2026-10-09T00:00:00Z", 1, nil)
		if got := strings.Join(result.Classes, ","); got != c.classes {
			t.Fatalf("%s: classes %q, want %q", name, got, c.classes)
		}
		for _, d := range result.Differences {
			if strings.Contains(d.Before+d.After, "Changed") || strings.Contains(d.WalletRef, wallet) {
				t.Fatalf("%s: differences carry digests and wallet references only: %+v", name, d)
			}
		}
	}
}

func TestComparisonPassesOnlyAcceptedClasses(t *testing.T) {
	c := Comparison{Classes: []string{"participantCount", "walletState"}}
	if c.passes() {
		t.Fatal("unaccepted differences must fail")
	}
	c.Accepted = []string{"participantCount", "walletState"}
	if !c.passes() {
		t.Fatal("accepted differences pass")
	}
	c.Classes, c.Accepted = []string{"field"}, []string{"field", "visibility", "participantCount", "walletState"}
	if c.passes() {
		t.Fatal("a field difference can never be accepted")
	}
	if !(Comparison{}).passes() {
		t.Fatal("no difference passes without a review")
	}
}

func TestReviewInputIsNormalized(t *testing.T) {
	valid := ReviewInput{RunID: "11111111-1111-4111-8111-111111111111", CampaignID: "c-1", Decision: "import-unpublished", Reason: "reviewed",
		Reviewer: "0xAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", ClientCommandID: "01a12127-44d1-7977-8c4b-ed8edf7ab819",
		AcceptedDifferences: []string{"visibility", "participantCount"}, AcknowledgedFindings: []string{"REWARD_TEXT", "COUNTER_MISMATCH"}}
	got, err := valid.normalized()
	if err != nil || got.Reviewer != strings.ToLower(valid.Reviewer) || strings.Join(got.AcceptedDifferences, ",") != "participantCount,visibility" ||
		strings.Join(got.AcknowledgedFindings, ",") != "COUNTER_MISMATCH,REWARD_TEXT" {
		t.Fatalf("normalized: %+v %v", got, err)
	}
	for name, change := range map[string]func(*ReviewInput){
		"v4 client ID":         func(r *ReviewInput) { r.ClientCommandID = "11111111-1111-4111-8111-111111111111" },
		"unknown decision":     func(r *ReviewInput) { r.Decision = "import" },
		"field difference":     func(r *ReviewInput) { r.AcceptedDifferences = []string{"field"} },
		"duplicate difference": func(r *ReviewInput) { r.AcceptedDifferences = []string{"visibility", "visibility"} },
		"blank reason":         func(r *ReviewInput) { r.Reason = "  " },
		"campaign identity":    func(r *ReviewInput) { r.CampaignID = "has space" },
	} {
		input := valid
		change(&input)
		if _, err := input.normalized(); err == nil {
			t.Fatalf("%s must be refused", name)
		}
	}
}
