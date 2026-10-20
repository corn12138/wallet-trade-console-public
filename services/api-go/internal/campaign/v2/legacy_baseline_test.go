package v2

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func baselineFixture() LegacyBaselineInput {
	description, metadata := "Original description", `{"k": 9007199254740993}`
	return LegacyBaselineInput{SchemaVersion: 1, TermsCanonicalVersion: 1, TemplateID: LegacyBaselineTemplate,
		MaterialTerms: LegacyMaterialTerms{Legacy: LegacyTerms{Title: "Legacy", Description: &description, StartAt: "2020-01-01T00:00:00.000Z", EndAt: "2020-02-01T00:00:00.500Z"}},
		LegacyRecord:  LegacyRecord{Status: "active", Participants: 3, MetadataJSON: &metadata, CreatedAt: "2019-12-31T23:59:59.999Z", UpdatedAt: "2020-01-02T03:04:05.678Z"},
		Source: LegacySource{RunID: "11111111-1111-4111-8111-111111111111", CapturedAt: "2026-10-09T00:00:00.123456Z", Algorithm: "legacy-row-json/v2",
			LegacyRowSHA256: strings.Repeat("a", 64), InteractionsSHA256: strings.Repeat("b", 64)}}
}

func TestLegacyBaselineRoundTripsExactly(t *testing.T) {
	input := baselineFixture()
	if err := input.Validate(); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeLegacyBaseline(raw)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := json.Marshal(decoded)
	if string(again) != string(raw) {
		t.Fatalf("round trip changed the baseline:\n%s\n%s", raw, again)
	}
	terms, content, err := input.Hashes()
	if err != nil {
		t.Fatal(err)
	}
	rawTerms, _ := CanonicalHash(json.RawMessage(`{"legacy":{"title":"Legacy","description":"Original description","reward":null,"startAt":"2020-01-01T00:00:00.000Z","endAt":"2020-02-01T00:00:00.500Z"}}`))
	rawContent, _ := CanonicalHash(json.RawMessage(raw))
	if terms != rawTerms || content != rawContent {
		t.Fatal("hashes of the struct and of its stored JSON must agree")
	}
	if *decoded.LegacyRecord.MetadataJSON != `{"k": 9007199254740993}` {
		t.Fatal("metadata is kept as its exact text")
	}
}

func TestLegacyBaselineDecodingIsStrict(t *testing.T) {
	raw, _ := json.Marshal(baselineFixture())
	var object map[string]any
	_ = json.Unmarshal(raw, &object)
	mutate := func(change func(map[string]any)) []byte {
		var copy map[string]any
		_ = json.Unmarshal(raw, &copy)
		change(copy)
		out, _ := json.Marshal(copy)
		return out
	}
	nested := func(object map[string]any, path ...string) map[string]any {
		for _, key := range path {
			object = object[key].(map[string]any)
		}
		return object
	}
	for name, body := range map[string][]byte{
		"unknown root key":        mutate(func(m map[string]any) { m["extra"] = true }),
		"unknown nested key":      mutate(func(m map[string]any) { nested(m, "materialTerms", "legacy")["cta"] = "x" }),
		"missing key":             mutate(func(m map[string]any) { delete(nested(m, "legacyRecord"), "banner") }),
		"null required value":     mutate(func(m map[string]any) { nested(m, "legacyRecord")["status"] = nil }),
		"fractional participants": mutate(func(m map[string]any) { nested(m, "legacyRecord")["participants"] = 1.5 }),
		"second-precision time":   mutate(func(m map[string]any) { nested(m, "materialTerms", "legacy")["startAt"] = "2020-01-01T00:00:00Z" }),
		"empty window":            mutate(func(m map[string]any) { nested(m, "materialTerms", "legacy")["endAt"] = "2020-01-01T00:00:00.000Z" }),
		"other algorithm":         mutate(func(m map[string]any) { nested(m, "source")["algorithm"] = "legacy-row-json/v1" }),
		"invalid metadata":        mutate(func(m map[string]any) { nested(m, "legacyRecord")["metadataJson"] = "{" }),
		"other template":          mutate(func(m map[string]any) { m["templateId"] = "campaignCardV1" }),
		"trailing data":           append(append([]byte{}, raw...), []byte(` {}`)...),
	} {
		if _, err := DecodeLegacyBaseline(body); err == nil {
			t.Fatalf("%s must be refused", name)
		}
	}
	long := baselineFixture()
	long.MaterialTerms.Legacy.Title = strings.Repeat("长", 201)
	if long.Validate() == nil {
		t.Fatal("a title longer than the legacy column must be refused")
	}
	large := baselineFixture()
	text := strings.Repeat("x", 70*1024)
	large.MaterialTerms.Legacy.Description = &text
	if large.Validate() == nil {
		t.Fatal("a baseline larger than one revision body must be refused")
	}
}

func TestLegacyBaselinePhaseFollowsTheLegacyRule(t *testing.T) {
	start := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour + 500*time.Millisecond)
	b := LegacyBaseline{Start: start, End: end}
	for _, c := range []struct {
		now  time.Time
		want string
	}{
		{start.Add(-time.Nanosecond), "upcoming"}, {start, "active"}, {end.Add(-time.Microsecond), "active"}, {end, "ended"}, {end.Add(time.Hour), "ended"},
	} {
		if got := b.Phase(c.now); got != c.want {
			t.Fatalf("phase at %s = %s, want %s", c.now, got, c.want)
		}
	}
	if next := b.NextTransition(start.Add(-time.Second)); next == nil || !next.Equal(start) {
		t.Fatal("an upcoming baseline transitions at its start")
	}
	if next := b.NextTransition(start); next == nil || !next.Equal(end) {
		t.Fatal("an active baseline transitions at its end")
	}
	if b.NextTransition(end) != nil {
		t.Fatal("an ended baseline has no transition")
	}
	if (LegacyBaseline{Start: start, End: start}).Phase(start) != "unsupported" {
		t.Fatal("an empty window is unsupported")
	}
	if FormatLegacyTime(time.Date(2020, 1, 1, 0, 0, 0, 999999999, time.UTC)) != "2020-01-01T00:00:00.999Z" {
		t.Fatal("formatting truncates to milliseconds")
	}
}
