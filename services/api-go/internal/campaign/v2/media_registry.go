package v2

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"regexp"
)

// The trusted media registry is generated from
// packages/shared/contracts/campaign-media-registry.json by
// scripts/strict/generate-campaign-media-registry.py, which also checks every
// file's digest and dimensions. Campaign content may only name these IDs
// (docs/campaign-content-capabilities-protocol-2026-10-13.md §2).
//
//go:embed media_registry.generated.json
var mediaRegistryJSON []byte

type MediaAlt struct {
	En string `json:"en"`
	Zh string `json:"zh"`
}

type MediaAsset struct {
	ID          string   `json:"id"`
	Path        string   `json:"path"`
	ContentType string   `json:"contentType"`
	Width       int      `json:"width"`
	Height      int      `json:"height"`
	SHA256      string   `json:"sha256"`
	Alt         MediaAlt `json:"alt"`
}

type MediaRegistry struct {
	SchemaVersion int          `json:"schemaVersion"`
	Revision      int          `json:"revision"`
	Assets        []MediaAsset `json:"assets"`
}

var (
	mediaIDPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,79}$`)
	mediaPathPattern = regexp.MustCompile(`^/campaign-media/[a-z0-9][a-z0-9-]{0,79}\.png$`)
)

// ParseMediaRegistry decodes a registry strictly and checks the rules the
// server relies on; file digests are checked by the generator.
func ParseMediaRegistry(raw []byte) (MediaRegistry, error) {
	var registry MediaRegistry
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&registry); err != nil {
		return MediaRegistry{}, errors.New("media registry: " + err.Error())
	}
	if decoder.Decode(new(any)) != io.EOF {
		return MediaRegistry{}, errors.New("media registry: trailing data")
	}
	if registry.SchemaVersion != 1 || registry.Revision < 1 || len(registry.Assets) < 1 || len(registry.Assets) > 50 {
		return MediaRegistry{}, errors.New("media registry: version or size")
	}
	seen := map[string]bool{}
	for _, asset := range registry.Assets {
		if !mediaIDPattern.MatchString(asset.ID) || seen[asset.ID] || asset.Path != "/campaign-media/"+asset.ID+".png" || !mediaPathPattern.MatchString(asset.Path) ||
			asset.ContentType != "image/png" || asset.Width < 1 || asset.Width > 4096 || asset.Height < 1 || asset.Height > 4096 ||
			!termsHashPattern.MatchString(asset.SHA256) || asset.Alt.En == "" || asset.Alt.Zh == "" {
			return MediaRegistry{}, errors.New("media registry: invalid asset " + asset.ID)
		}
		seen[asset.ID] = true
	}
	return registry, nil
}

// TrustedMedia is the registry this binary was built with.
func TrustedMedia() (MediaRegistry, error) { return ParseMediaRegistry(mediaRegistryJSON) }

// AssetIDs is the capability set of the registry.
func (r MediaRegistry) AssetIDs() map[string]bool {
	ids := make(map[string]bool, len(r.Assets))
	for _, asset := range r.Assets {
		ids[asset.ID] = true
	}
	return ids
}
