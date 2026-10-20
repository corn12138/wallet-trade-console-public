package v2

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestSharedRevisionCorpus(t *testing.T) {
	var cases []struct {
		Name   string   `json:"name"`
		Path   []string `json:"path"`
		Value  any      `json:"value"`
		Remove bool     `json:"remove"`
		Reason string   `json:"reason"`
	}
	raw, err := os.ReadFile("../../../../../packages/shared/contracts/fixtures/campaign-v2-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, item := range cases {
		t.Run(item.Name, func(t *testing.T) {
			raw, _ := json.Marshal(revisionFixture(t))
			var input map[string]any
			_ = json.Unmarshal(raw, &input)
			target := input
			for _, part := range item.Path[:len(item.Path)-1] {
				target = target[part].(map[string]any)
			}
			key := item.Path[len(item.Path)-1]
			if item.Remove {
				delete(target, key)
			} else {
				target[key] = item.Value
			}
			raw, _ = json.Marshal(input)
			revision, err := DecodeRevision(raw)
			if err == nil {
				err = ValidateRevision(revision, capabilities)
			}
			code := "CAMPAIGN_INVALID_INPUT"
			if item.Reason == "unsupported" {
				code = "CAMPAIGN_UNSUPPORTED"
			}
			errorCode(t, err, code)
		})
	}
}

func TestAmbiguousWireEncodings(t *testing.T) {
	raw, _ := json.Marshal(revisionFixture(t))
	source := string(raw)
	for name, mutation := range map[string]string{
		"duplicate-key":   strings.Replace(source, `"schemaVersion":1`, `"schemaVersion":1,"schemaVersion":1`, 1),
		"rounded-version": strings.Replace(source, `"schemaVersion":1`, `"schemaVersion":1.00000000000000001`, 1),
		"huge-exponent":   strings.Replace(source, `"schemaVersion":1`, `"schemaVersion":1e999999999`, 1),
		"lone-surrogate":  strings.Replace(source, `"title":"`, `"title":"\ud800`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if source == mutation {
				t.Fatal("fixture mutation did not apply")
			}
			_, err := DecodeRevision([]byte(mutation))
			errorCode(t, err, "CAMPAIGN_INVALID_INPUT")
		})
	}
	valid := strings.Replace(source, `"schemaVersion":1`, `"schemaVersion":1.0e0`, 1)
	if _, err := DecodeRevision([]byte(valid)); err != nil {
		t.Fatal(err)
	}
	input := revisionFixture(t)
	input.MaterialTerms.Copy.En.Title = strings.Repeat("🙂", 120)
	raw, _ = json.Marshal(input)
	if _, err := DecodeRevision(raw); err != nil {
		t.Fatal(err)
	}
	input.MaterialTerms.Copy.En.Title += "x"
	raw, _ = json.Marshal(input)
	_, err := DecodeRevision(raw)
	errorCode(t, err, "CAMPAIGN_INVALID_INPUT")
}
