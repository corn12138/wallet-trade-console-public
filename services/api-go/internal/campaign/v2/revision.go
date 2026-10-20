package v2

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

type Capabilities struct {
	Templates    map[string]bool
	MediaAssets  map[string]bool
	TradeMarkets map[int64]map[string]bool
}

var htmlTag = regexp.MustCompile(`<[!/?a-zA-Z]`)
var termsHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func ValidateRevision(input RevisionInput, capabilities Capabilities) error {
	encoded, err := json.Marshal(input)
	if err != nil {
		return InvalidInput("revision encoding")
	}
	if _, err = DecodeRevision(encoded); err != nil {
		return err
	}
	if !capabilities.Templates[input.TemplateID] {
		return &DomainError{"CAMPAIGN_UNSUPPORTED", 422, "template is not deployed"}
	}
	start, err := time.Parse("2006-01-02T15:04:05Z", input.MaterialTerms.StartAt)
	if err != nil {
		return InvalidInput("invalid UTC startAt")
	}
	end, err := time.Parse("2006-01-02T15:04:05Z", input.MaterialTerms.EndAt)
	if err != nil || !end.After(start) {
		return InvalidInput("endAt must follow startAt")
	}
	for _, copy := range []LocalizedCopy{input.MaterialTerms.Copy.En, input.MaterialTerms.Copy.Zh} {
		for _, text := range []string{copy.Title, copy.Description, copy.Rules, copy.RewardDescription} {
			if !utf8.ValidString(text) || strings.TrimSpace(text) == "" || htmlTag.MatchString(text) {
				return InvalidInput("copy must be nonblank plain text")
			}
		}
	}
	if media := input.DisplayContent.MediaAssetID; media != nil && !capabilities.MediaAssets[*media] {
		return &DomainError{"CAMPAIGN_UNSUPPORTED", 422, "media asset is not approved"}
	}
	cta := input.Cta
	switch cta.ActionKind {
	case "none":
		if cta.ChainID != nil || cta.MarketID != nil {
			return InvalidInput("none CTA cannot carry a target")
		}
	case "openTrade":
		if cta.ChainID == nil || cta.MarketID == nil {
			return InvalidInput("trade CTA requires chain and market")
		}
		if !capabilities.TradeMarkets[*cta.ChainID][*cta.MarketID] {
			return &DomainError{"CAMPAIGN_UNSUPPORTED", 422, "trade target is not deployed"}
		}
	default:
		return &DomainError{"CAMPAIGN_UNSUPPORTED", 422, "CTA is not deployed"}
	}
	return nil
}

// Canonical version 1 uses UTF-8 JSON with lexically sorted object keys, Go JSON
// string escaping without HTML escaping, and UTC-second date strings. All free
// copy is inside materialTerms; layout changes therefore cannot hide new terms.
func CanonicalHash(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	var normalized any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err = decoder.Decode(&normalized); err != nil {
		return "", err
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err = encoder.Encode(normalized); err != nil {
		return "", err
	}
	digest := sha256.Sum256(bytes.TrimSuffix(buffer.Bytes(), []byte("\n")))
	return hex.EncodeToString(digest[:]), nil
}

func NewRevision(id, campaignID string, input RevisionInput, capabilities Capabilities) (Revision, error) {
	if err := ValidateRevision(input, capabilities); err != nil {
		return Revision{}, err
	}
	termsHash, err := CanonicalHash(input.MaterialTerms)
	if err != nil {
		return Revision{}, err
	}
	contentHash, err := CanonicalHash(input)
	if err != nil {
		return Revision{}, err
	}
	return Revision{ID: id, CampaignID: campaignID, Input: input, MaterialTermsHash: termsHash, ContentHash: contentHash}, nil
}
