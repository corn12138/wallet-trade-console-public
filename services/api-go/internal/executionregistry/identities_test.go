package executionregistry

import (
	"encoding/json"
	"testing"
)

// The Web asserts the same fixture (features/execution-config).
func TestCrossLanguageIdentities(t *testing.T) {
	var fixture struct {
		ManifestDigest string            `json:"manifestDigest"`
		Selectors      map[string]string `json:"selectors"`
	}
	if err := json.Unmarshal(readFile(t, repoRoot+"packages/shared/contracts/fixtures/execution-registry-identities.json"), &fixture); err != nil {
		t.Fatal(err)
	}
	m := BuiltinManifest()
	if m.Digest != fixture.ManifestDigest {
		t.Errorf("manifest digest %s, fixture %s", m.Digest, fixture.ManifestDigest)
	}
	if len(m.Interfaces) != len(fixture.Selectors) {
		t.Errorf("interfaces %d, fixture %d", len(m.Interfaces), len(fixture.Selectors))
	}
	for id, selector := range fixture.Selectors {
		if m.Interfaces[id].Selector != selector {
			t.Errorf("%s selector %s, fixture %s", id, m.Interfaces[id].Selector, selector)
		}
	}
}
