package v2

import (
	"math"
	"time"
)

func DecideInteraction(publication Publication, previous *Interaction, kind string, enable bool, expectedState uint64, expectedPublication *uint64, now time.Time) (InteractionChange, error) {
	positive, negative, phase := "joined", "left", "active"
	if kind == "reminder" {
		positive, negative, phase = "set", "removed", "upcoming"
	} else if kind != "join" {
		return InteractionChange{}, InvalidInput("interaction kind")
	}
	current := Interaction{Kind: kind, Acceptance: Acceptance{Provenance: "neverAccepted"}}
	if previous != nil {
		current = *previous
		if current.Kind != kind || (current.Status != positive && current.Status != negative) || !validStoredInteraction(current, positive) {
			return InteractionChange{}, &DomainError{"CAMPAIGN_UNSUPPORTED", 422, "interaction state"}
		}
	}
	if current.Version != expectedState {
		return InteractionChange{}, conflict("CAMPAIGN_STATE_CONFLICT")
	}
	if enable && expectedPublication == nil {
		return InteractionChange{}, InvalidInput("expectedPublicationVersion required")
	}
	if enable && *expectedPublication != publication.Version {
		return InteractionChange{}, conflict("CAMPAIGN_VERSION_CONFLICT")
	}
	if enable && Phase(publication, now) == "unsupported" {
		return InteractionChange{}, &DomainError{"CAMPAIGN_UNSUPPORTED", 422, "current publication rule or schema"}
	}
	if publication.Active == nil && previous == nil {
		return InteractionChange{}, &DomainError{"CAMPAIGN_NOT_FOUND", 404, "campaign not published"}
	}
	target := negative
	if enable {
		target = positive
	}
	if current.Status == target {
		return InteractionChange{Before: previous, After: current, Changed: false}, nil
	}
	if current.Version >= math.MaxInt64 {
		return InteractionChange{}, InvalidInput("state version exhausted")
	}
	if enable {
		if publication.Mode != "enabled" || Phase(publication, now) != phase {
			return InteractionChange{}, conflict("CAMPAIGN_NOT_OPEN")
		}
		revisionID, version, hash := publication.Active.ID, publication.Version, publication.Active.MaterialTermsHash
		current.Acceptance = Acceptance{Provenance: "verified", RevisionID: &revisionID, PublicationVersion: &version, TermsHash: &hash, AcceptedAt: &now}
	}
	// A first cancellation creates version 1 without pretending acceptance. A
	// delayed version-0 join then conflicts instead of resurrecting participation.
	current.Status = target
	current.Version++
	return InteractionChange{Before: previous, After: current, Changed: true}, nil
}

func validStoredInteraction(value Interaction, positive string) bool {
	if value.Version == 0 || value.Version > math.MaxInt64 {
		return false
	}
	accepted := value.Acceptance
	emptyReference := accepted.RevisionID == nil && accepted.PublicationVersion == nil && accepted.TermsHash == nil
	switch accepted.Provenance {
	case "neverAccepted":
		return emptyReference && accepted.AcceptedAt == nil && value.Status != positive
	case "legacyUnknown":
		return emptyReference
	case "verified":
		return accepted.RevisionID != nil && *accepted.RevisionID != "" && accepted.PublicationVersion != nil && *accepted.PublicationVersion > 0 && *accepted.PublicationVersion <= math.MaxInt64 && accepted.TermsHash != nil && termsHashPattern.MatchString(*accepted.TermsHash) && accepted.AcceptedAt != nil
	default:
		return false
	}
}
