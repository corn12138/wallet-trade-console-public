package v2

import (
	"math"
	"time"
)

// DecidePublication is pure. The caller must hold this campaign's publication
// row lock and supply clock_timestamp() read after acquiring that lock.
func DecidePublication(current Publication, operation string, expected uint64, candidate *Revision, previouslyPublished bool, now time.Time) (Publication, error) {
	if expected != current.Version {
		return Publication{}, conflict("CAMPAIGN_VERSION_CONFLICT")
	}
	if current.Version >= math.MaxInt64 {
		return Publication{}, InvalidInput("publication version exhausted")
	}
	if current.Mode == "archived" {
		return Publication{}, conflict("CAMPAIGN_ARCHIVED")
	}
	if current.Mode != "enabled" && current.Mode != "paused" {
		return Publication{}, &DomainError{"CAMPAIGN_UNSUPPORTED", 422, "publication mode"}
	}
	next := current
	switch operation {
	case "publish", "rollback":
		if candidate == nil || candidate.CampaignID != current.CampaignID {
			return Publication{}, InvalidInput("revision does not belong to campaign")
		}
		if operation == "rollback" && !previouslyPublished {
			return Publication{}, InvalidInput("rollback requires published history")
		}
		lock, err := TermsLock(current, now)
		if err != nil {
			return Publication{}, err
		}
		switch lock {
		case TermsLegacyUnverified:
			return Publication{}, conflict("CAMPAIGN_LEGACY_TERMS_UNVERIFIED")
		case TermsLocked:
			if candidate.MaterialTermsHash != current.Active.MaterialTermsHash {
				return Publication{}, conflict("CAMPAIGN_TERMS_LOCKED")
			}
		}
		next.Active = candidate
	case "pause":
		if current.Active == nil || current.Mode != "enabled" {
			return Publication{}, conflict("CAMPAIGN_INVALID_TRANSITION")
		}
		next.Mode = "paused"
	case "resume":
		if current.Active == nil || current.Mode != "paused" {
			return Publication{}, conflict("CAMPAIGN_INVALID_TRANSITION")
		}
		next.Mode = "enabled"
	case "archive":
		next.Mode = "archived"
	default:
		return Publication{}, InvalidInput("unsupported publication operation")
	}
	next.Version++
	return next, nil
}

const (
	TermsOpen             = "open"
	TermsLocked           = "locked"
	TermsLegacyUnverified = "legacyUnverified"
)

// TermsLock is the single material-terms rule shared by DecidePublication and
// the administrator read view, so a preview cannot predict a different lock than
// the locked write applies. The current active terms decide it: a candidate with
// a later start cannot reopen rules, and left/removed acceptance still binds.
// Without an active revision, unknown legacy acceptance blocks any publication
// because no verified original terms exist to compare a candidate against.
func TermsLock(current Publication, now time.Time) (string, error) {
	if current.Active == nil {
		if current.HasAcceptedHistory {
			return TermsLegacyUnverified, nil
		}
		return TermsOpen, nil
	}
	start, err := time.Parse(time.RFC3339, current.Active.Input.MaterialTerms.StartAt)
	if err != nil {
		return "", &DomainError{"CAMPAIGN_UNSUPPORTED", 422, "current terms time"}
	}
	if current.HasAcceptedHistory || !now.Before(start) {
		return TermsLocked, nil
	}
	return TermsOpen, nil
}
