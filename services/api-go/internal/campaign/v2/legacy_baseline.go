package v2

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"
	"unicode/utf8"
)

// LegacyBaselineTemplate identifies the read-only revision that keeps an
// imported legacy campaign's original values
// (docs/campaign-legacy-activation-protocol-2026-10-09.md §1.1). It is never
// executable v1 content: nothing can join or set a reminder against it.
const LegacyBaselineTemplate = "legacyBaselineV1"

// LegacyTimeLayout is the fixed millisecond UTC form of a legacy TIMESTAMP(3)
// value. Strings of this form compare in time order.
const LegacyTimeLayout = "2006-01-02T15:04:05.000Z"

// legacyBaselineMaxBytes keeps a baseline within one revision request body, so
// every read page can still carry it.
const legacyBaselineMaxBytes = 64 * 1024

var (
	legacyTimePattern = regexp.MustCompile(`^[1-9][0-9]{3}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]{3}Z$`)
	legacyHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	legacyUUIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
)

// LegacyTerms are the values a legacy participant saw.
type LegacyTerms struct {
	Title       string  `json:"title"`
	Description *string `json:"description"`
	Reward      *string `json:"reward"`
	StartAt     string  `json:"startAt"`
	EndAt       string  `json:"endAt"`
}

type LegacyMaterialTerms struct {
	Legacy LegacyTerms `json:"legacy"`
}

// LegacyRecord keeps the rest of the legacy row verbatim. MetadataJSON is the
// stored JSON text, so v2 never re-encodes its numbers.
type LegacyRecord struct {
	Banner       *string `json:"banner"`
	Status       string  `json:"status"`
	Participants int64   `json:"participants"`
	MetadataJSON *string `json:"metadataJson"`
	CreatedAt    string  `json:"createdAt"`
	UpdatedAt    string  `json:"updatedAt"`
}

// LegacySource binds the baseline to the capture it was verified against.
type LegacySource struct {
	RunID              string `json:"runId"`
	CapturedAt         string `json:"capturedAt"`
	Algorithm          string `json:"algorithm"`
	LegacyRowSHA256    string `json:"legacyRowSha256"`
	InteractionsSHA256 string `json:"interactionsSha256"`
}

type LegacyBaselineInput struct {
	SchemaVersion         int                 `json:"schemaVersion"`
	TermsCanonicalVersion int                 `json:"termsCanonicalVersion"`
	TemplateID            string              `json:"templateId"`
	MaterialTerms         LegacyMaterialTerms `json:"materialTerms"`
	LegacyRecord          LegacyRecord        `json:"legacyRecord"`
	Source                LegacySource        `json:"source"`
}

// LegacyBaseline is a stored baseline revision whose shape and hashes were
// verified.
type LegacyBaseline struct {
	RevisionID        string
	CampaignID        string
	Input             LegacyBaselineInput
	Start, End        time.Time
	MaterialTermsHash string
	ContentHash       string
}

func ParseLegacyTime(value string) (time.Time, error) {
	if !legacyTimePattern.MatchString(value) {
		return time.Time{}, fmt.Errorf("legacy time %q is not %s", value, LegacyTimeLayout)
	}
	return time.Parse(LegacyTimeLayout, value)
}

func FormatLegacyTime(value time.Time) string { return value.UTC().Format(LegacyTimeLayout) }

// Phase applies the legacy API's rule to the baseline window: an empty window
// is unsupported, otherwise upcoming before the start, ended from the end on.
func (b LegacyBaseline) Phase(now time.Time) string {
	now = now.UTC()
	switch {
	case !b.End.After(b.Start):
		return "unsupported"
	case now.Before(b.Start):
		return "upcoming"
	case !now.Before(b.End):
		return "ended"
	}
	return "active"
}

func (b LegacyBaseline) NextTransition(now time.Time) *time.Time {
	switch b.Phase(now) {
	case "upcoming":
		start := b.Start
		return &start
	case "active":
		end := b.End
		return &end
	}
	return nil
}

// PublicView is the read-only display of a baseline for clients that
// negotiated it. It carries the verbatim terms and when they were captured,
// never the banner URL, raw metadata, the legacy counter or capture digests.
func (b LegacyBaseline) PublicView() (QueryLegacyBaselineView, error) {
	captured, err := time.Parse(time.RFC3339Nano, b.Input.Source.CapturedAt)
	if err != nil {
		return QueryLegacyBaselineView{}, InvalidInput("legacy baseline capture time")
	}
	terms := b.Input.MaterialTerms.Legacy
	return QueryLegacyBaselineView{TemplateID: LegacyBaselineTemplate, Title: terms.Title, Description: terms.Description, Reward: terms.Reward,
		StartAt: terms.StartAt, EndAt: terms.EndAt, CapturedAt: captured.UTC().Format(time.RFC3339Nano)}, nil
}

// Hashes returns the material-terms and content hashes stored with the
// revision, computed exactly like those of v1 revisions.
func (in LegacyBaselineInput) Hashes() (terms, content string, err error) {
	if terms, err = CanonicalHash(in.MaterialTerms); err != nil {
		return "", "", err
	}
	content, err = CanonicalHash(in)
	return terms, content, err
}

// Validate checks every field against what the legacy columns can hold and
// what the capture recorded.
func (in LegacyBaselineInput) Validate() error {
	invalid := func(detail string) error { return InvalidInput("legacy baseline: " + detail) }
	if in.SchemaVersion != 1 || in.TermsCanonicalVersion != 1 || in.TemplateID != LegacyBaselineTemplate {
		return invalid("version or template")
	}
	terms, record, source := in.MaterialTerms.Legacy, in.LegacyRecord, in.Source
	if !utf8.ValidString(terms.Title) || utf8.RuneCountInString(terms.Title) > 200 {
		return invalid("title")
	}
	for _, text := range []*string{terms.Description, terms.Reward, record.Banner} {
		if text != nil && !utf8.ValidString(*text) {
			return invalid("text is not UTF-8")
		}
	}
	start, err := ParseLegacyTime(terms.StartAt)
	if err != nil {
		return invalid("start")
	}
	end, err := ParseLegacyTime(terms.EndAt)
	if err != nil || !end.After(start) {
		return invalid("end")
	}
	if record.Status == "" || !utf8.ValidString(record.Status) || len(record.Status) > 100 {
		return invalid("status")
	}
	if record.Participants < -1<<31 || record.Participants > 1<<31-1 {
		return invalid("participants")
	}
	if record.MetadataJSON != nil && !json.Valid([]byte(*record.MetadataJSON)) {
		return invalid("metadata")
	}
	for _, value := range []string{record.CreatedAt, record.UpdatedAt} {
		if _, err := ParseLegacyTime(value); err != nil {
			return invalid("record time")
		}
	}
	if !legacyUUIDPattern.MatchString(source.RunID) || source.Algorithm != "legacy-row-json/v2" ||
		!legacyHashPattern.MatchString(source.LegacyRowSHA256) || !legacyHashPattern.MatchString(source.InteractionsSHA256) {
		return invalid("source")
	}
	if _, err := time.Parse(time.RFC3339Nano, source.CapturedAt); err != nil {
		return invalid("capture time")
	}
	raw, err := json.Marshal(in)
	if err != nil || len(raw) > legacyBaselineMaxBytes {
		return invalid("size")
	}
	return nil
}

// legacyBaselineKeys lists the exact keys of every object level; nullable keys
// may hold null, every other key must hold a value.
var legacyBaselineKeys = map[string]map[string]bool{
	"":              {"schemaVersion": false, "termsCanonicalVersion": false, "templateId": false, "materialTerms": false, "legacyRecord": false, "source": false},
	"materialTerms": {"legacy": false},
	"legacy":        {"title": false, "description": true, "reward": true, "startAt": false, "endAt": false},
	"legacyRecord":  {"banner": true, "status": false, "participants": false, "metadataJson": true, "createdAt": false, "updatedAt": false},
	"source":        {"runId": false, "capturedAt": false, "algorithm": false, "legacyRowSha256": false, "interactionsSha256": false},
}

func exactLegacyKeys(raw json.RawMessage, level string) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, fmt.Errorf("%s is not an object", level)
	}
	want := legacyBaselineKeys[level]
	if len(object) != len(want) {
		return nil, fmt.Errorf("%s has unexpected keys", level)
	}
	for key, nullable := range want {
		value, ok := object[key]
		if !ok || (!nullable && bytes.Equal(bytes.TrimSpace(value), []byte("null"))) {
			return nil, fmt.Errorf("%s.%s missing", level, key)
		}
	}
	return object, nil
}

// DecodeLegacyBaseline decodes a stored baseline strictly: exact key sets at
// every level, no missing or null required value, integer participants, then
// the same validation as at write time.
func DecodeLegacyBaseline(raw []byte) (LegacyBaselineInput, error) {
	var input LegacyBaselineInput
	root, err := exactLegacyKeys(raw, "")
	if err == nil {
		var terms map[string]json.RawMessage
		if terms, err = exactLegacyKeys(root["materialTerms"], "materialTerms"); err == nil {
			_, err = exactLegacyKeys(terms["legacy"], "legacy")
		}
	}
	if err == nil {
		_, err = exactLegacyKeys(root["legacyRecord"], "legacyRecord")
	}
	if err == nil {
		_, err = exactLegacyKeys(root["source"], "source")
	}
	if err != nil {
		return input, InvalidInput("legacy baseline: " + err.Error())
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&input); err != nil {
		return LegacyBaselineInput{}, InvalidInput("legacy baseline: " + err.Error())
	}
	if decoder.More() {
		return LegacyBaselineInput{}, errors.New("legacy baseline: trailing data")
	}
	if err = input.Validate(); err != nil {
		return LegacyBaselineInput{}, err
	}
	return input, nil
}
