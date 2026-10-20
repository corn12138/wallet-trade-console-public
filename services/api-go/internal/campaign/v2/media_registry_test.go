package v2

import (
	"strings"
	"testing"
)

func TestEmbeddedMediaRegistryIsTrusted(t *testing.T) {
	registry, err := TrustedMedia()
	if err != nil {
		t.Fatal(err)
	}
	ids := registry.AssetIDs()
	if registry.Revision < 1 || !ids["trading-week-banner-v1"] || len(ids) != len(registry.Assets) {
		t.Fatalf("registry: %+v", registry)
	}
}

func TestMediaRegistryRefusesUntrustedEntries(t *testing.T) {
	valid := `{"schemaVersion":1,"revision":1,"assets":[{"id":"a-1","path":"/campaign-media/a-1.png","contentType":"image/png","width":10,"height":10,"sha256":"` + strings.Repeat("a", 64) + `","alt":{"en":"A","zh":"甲"}}]}`
	if _, err := ParseMediaRegistry([]byte(valid)); err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]string{
		"remote URL":     strings.Replace(valid, `"/campaign-media/a-1.png"`, `"https://cdn.invalid/a-1.png"`, 1),
		"other path":     strings.Replace(valid, `/campaign-media/a-1.png`, `/campaign-media/b-1.png`, 1),
		"svg":            strings.Replace(valid, `image/png`, `image/svg+xml`, 1),
		"unknown key":    strings.Replace(valid, `"width":10`, `"href":"/x","width":10`, 1),
		"no alt":         strings.Replace(valid, `"en":"A"`, `"en":""`, 1),
		"zero revision":  strings.Replace(valid, `"revision":1`, `"revision":0`, 1),
		"huge":           strings.Replace(valid, `"width":10`, `"width":5000`, 1),
		"duplicate":      strings.Replace(valid, `]}`, `,`+valid[strings.Index(valid, `{"id"`):len(valid)-2]+`]}`, 1),
		"trailing value": valid + `{}`,
	} {
		if _, err := ParseMediaRegistry([]byte(raw)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
