package v2

import (
	"errors"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"
)

func storedStateInvalid(detail string) error {
	return &DomainError{"CAMPAIGN_STORED_STATE_INVALID", 503, detail}
}

func storedIDValid(id string) bool {
	if id == "" || !utf8.ValidString(id) || strings.TrimSpace(id) != id {
		return false
	}
	return strings.IndexFunc(id, unicode.IsControl) < 0
}

// ValidateStoredRevision is the publication/new-admission boundary. Creation
// validation is insufficient because deployed capabilities can later be removed.
func ValidateStoredRevision(revision Revision, capabilities Capabilities) error {
	if err := ValidateStoredRevisionIntegrity(revision); err != nil {
		return err
	}
	return storedRevisionError(ValidateRevision(revision.Input, capabilities))
}

// ValidateStoredRevisionIntegrity permits historical inspection after a current
// capability is removed. It does not authorize publishing, joining, reminder
// creation, media loading, or execution of the historical CTA.
func ValidateStoredRevisionIntegrity(revision Revision) error {
	if !storedIDValid(revision.ID) || !storedIDValid(revision.CampaignID) {
		return storedStateInvalid("stored revision identity is invalid")
	}
	// The synthetic capability set bypasses only the live registry. The closed
	// wire schema, known canonical version, dates, copy and CTA shape are still
	// validated by the same rules used when the immutable revision was created.
	structural := Capabilities{
		Templates:    map[string]bool{revision.Input.TemplateID: true},
		MediaAssets:  map[string]bool{},
		TradeMarkets: map[int64]map[string]bool{},
	}
	if asset := revision.Input.DisplayContent.MediaAssetID; asset != nil {
		structural.MediaAssets[*asset] = true
	}
	if cta := revision.Input.Cta; cta.ChainID != nil && cta.MarketID != nil {
		structural.TradeMarkets[*cta.ChainID] = map[string]bool{*cta.MarketID: true}
	}
	if err := storedRevisionError(ValidateRevision(revision.Input, structural)); err != nil {
		return err
	}
	if !termsHashPattern.MatchString(revision.MaterialTermsHash) || !termsHashPattern.MatchString(revision.ContentHash) {
		return storedStateInvalid("stored revision hash encoding is invalid")
	}
	terms, err := CanonicalHash(revision.Input.MaterialTerms)
	if err != nil || terms != revision.MaterialTermsHash {
		return storedStateInvalid("stored revision material terms hash does not match content")
	}
	content, err := CanonicalHash(revision.Input)
	if err != nil || content != revision.ContentHash {
		return storedStateInvalid("stored revision content hash does not match content")
	}
	return nil
}

// ValidateStoredPublication must run under the publication lock before new
// admission. Eligibility, expected versions and the DB clock remain separate
// DecideInteraction/DecidePublication checks; validation alone grants nothing.
func ValidateStoredPublication(pub Publication, capabilities Capabilities) error {
	if err := ValidateStoredPublicationEnvelope(pub); err != nil {
		return err
	}
	if pub.Active != nil {
		return ValidateStoredRevision(*pub.Active, capabilities)
	}
	return nil
}

// ValidateStoredPublicationIntegrity is for historical reads. Consumers must
// keep historical CTA/media inert when the current registry no longer allows it.
func ValidateStoredPublicationIntegrity(pub Publication) error {
	if err := ValidateStoredPublicationEnvelope(pub); err != nil {
		return err
	}
	if pub.Active != nil {
		return ValidateStoredRevisionIntegrity(*pub.Active)
	}
	return nil
}

// ValidateStoredPublicationEnvelope is sufficient for cancellation followed by
// DecideInteraction(enable=false): leaving a valid personal record does not
// require interpreting or reauthorizing the historical revision's capabilities.
// It never proves content integrity and must not be used for new admission.
func ValidateStoredPublicationEnvelope(pub Publication) error {
	if !storedIDValid(pub.CampaignID) || pub.Version > math.MaxInt64 || pub.InteractionVersion > math.MaxInt64 {
		return storedStateInvalid("stored publication identity or version is invalid")
	}
	if pub.Mode != "enabled" && pub.Mode != "paused" && pub.Mode != "archived" {
		return &DomainError{"CAMPAIGN_UNSUPPORTED", 422, "stored publication mode"}
	}
	if pub.Active != nil {
		if pub.Version == 0 || !storedIDValid(pub.Active.ID) || pub.Active.CampaignID != pub.CampaignID {
			return storedStateInvalid("stored active revision identity, ownership or publication version is invalid")
		}
		return nil
	}
	// An unpublished aggregate starts enabled at version zero. Archiving that
	// draft creates a positive version without inventing an active revision.
	if (pub.Mode == "enabled" && pub.Version == 0) || (pub.Mode == "archived" && pub.Version > 0) {
		return nil
	}
	return storedStateInvalid("stored publication mode and active revision are inconsistent")
}

func storedRevisionError(err error) error {
	if err == nil {
		return nil
	}
	var domain *DomainError
	if errors.As(err, &domain) && domain.Code == "CAMPAIGN_UNSUPPORTED" {
		return &DomainError{"CAMPAIGN_UNSUPPORTED", 422, domain.Detail}
	}
	return storedStateInvalid("stored revision input is invalid")
}
