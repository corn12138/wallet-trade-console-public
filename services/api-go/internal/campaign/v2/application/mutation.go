package application

import (
	"math"
	"time"

	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
)

func (s *Service) prepareMutation(tx Transaction, command Command, pub v2.Publication, state *v2.Interaction, now time.Time) (Mutation, Result, error) {
	mutation := Mutation{BasePublication: pub}
	var result Result
	if err := checkExpectedVersions(command, pub, state); err != nil {
		return mutation, result, err
	}
	alreadyAtTarget := interactionAlreadyAtTarget(command.Operation, state)
	var capabilities v2.Capabilities
	// Cancellation and containment do not reopen admission; neither requires a
	// withdrawn capability registry to become available again.
	if needsCapabilities(command.Operation) && !alreadyAtTarget {
		if s.capabilities == nil {
			return mutation, result, unavailable("campaign capabilities unavailable")
		}
		capabilities = s.capabilities()
	}
	switch command.Operation {
	case "create":
		id, err := NewID()
		if err != nil {
			return mutation, result, err
		}
		revisionID, err := NewID()
		if err != nil {
			return mutation, result, err
		}
		revision, err := v2.NewRevision(revisionID, id, *command.Revision, capabilities)
		if err != nil {
			return mutation, result, err
		}
		mutation.Definition = &Definition{ID: id, Slug: command.Slug}
		mutation.Revision = &revision
		pub = v2.Publication{CampaignID: id, Mode: "enabled"}
		mutation.BasePublication = pub
		result = snapshot(pub, nil)
		result.RevisionID = revisionID
		result.Changed = true
	case "revision":
		if pub.Mode == "archived" {
			return mutation, result, &v2.DomainError{Code: "CAMPAIGN_ARCHIVED", Status: 409, Detail: "archived campaign"}
		}
		id, err := NewID()
		if err != nil {
			return mutation, result, err
		}
		revision, err := v2.NewRevision(id, pub.CampaignID, *command.Revision, capabilities)
		if err != nil {
			return mutation, result, err
		}
		mutation.Revision = &revision
		result = snapshot(pub, nil)
		result.RevisionID = id
		result.Changed = true
	case "publish", "rollback", "pause", "resume", "archive":
		// Pause/archive remain possible even when historical content is no longer
		// understood. Publishing or resuming still requires trustworthy content.
		if pub.Active != nil && command.Operation != "pause" && command.Operation != "archive" {
			current, err := loadActive(tx, pub)
			if err != nil {
				return mutation, result, err
			}
			pub.Active = &current
			if err = v2.ValidateStoredPublicationIntegrity(pub); err != nil {
				return mutation, result, err
			}
		}
		var candidate *v2.Revision
		published := false
		if command.CandidateRevisionID != "" {
			value, err := tx.Revision(command.CandidateRevisionID)
			if err != nil {
				return mutation, result, err
			}
			if value.ID != command.CandidateRevisionID || value.CampaignID != pub.CampaignID {
				return mutation, result, v2.InvalidInput("revision does not belong to campaign")
			}
			if err = v2.ValidateStoredRevision(value, capabilities); err != nil {
				return mutation, result, err
			}
			candidate = &value
			published, err = tx.WasPublished(value.ID)
			if err != nil {
				return mutation, result, err
			}
		}
		if command.Operation == "resume" {
			if err := v2.ValidateStoredPublication(pub, capabilities); err != nil {
				return mutation, result, err
			}
		}
		next, err := v2.DecidePublication(pub, command.Operation, *command.ExpectedPublication, candidate, published, now)
		if err != nil {
			return mutation, result, err
		}
		mutation.Publication = &PublicationChange{Before: pub, After: next}
		pub = next
		result = snapshot(pub, nil)
		result.Changed = true
	default:
		enable := command.Operation == "join" || command.Operation == "set-reminder"
		if enable && pub.Active != nil {
			revision, err := loadActive(tx, pub)
			if err != nil {
				return mutation, result, err
			}
			pub.Active = &revision
			if err = v2.ValidateStoredPublicationIntegrity(pub); err != nil {
				return mutation, result, err
			}
			if !alreadyAtTarget {
				if err = v2.ValidateStoredPublication(pub, capabilities); err != nil {
					return mutation, result, err
				}
			}
		}
		change, err := v2.DecideInteraction(pub, state, interactionKind(command.Operation), enable, *command.ExpectedState, command.ExpectedPublication, now)
		if err != nil {
			return mutation, result, err
		}
		mutation.Interaction = &change
		mutation.BasePublication = pub
		if change.Changed {
			if pub.InteractionVersion >= math.MaxInt64 {
				return mutation, result, v2.InvalidInput("interaction version exhausted")
			}
			pub.InteractionVersion++
		}
		result = snapshot(pub, &change.After)
		result.Changed = change.Changed
	}
	return mutation, result, nil
}

func needsCapabilities(operation string) bool {
	switch operation {
	case "leave", "remove-reminder", "pause", "archive":
		return false
	default:
		return true
	}
}

func loadActive(tx Transaction, pub v2.Publication) (v2.Revision, error) {
	revision, err := tx.Revision(pub.Active.ID)
	if err != nil {
		return revision, err
	}
	if revision.ID != pub.Active.ID || revision.CampaignID != pub.CampaignID {
		return revision, unavailable("active revision identity mismatch")
	}
	return revision, nil
}

// A fresh command still performs personal CAS before any policy/configuration
// lookup. A matched no-op confirms stored state and never grants new admission.
func checkExpectedVersions(command Command, pub v2.Publication, state *v2.Interaction) error {
	if interactionKind(command.Operation) != "" {
		version := uint64(0)
		if state != nil {
			version = state.Version
		}
		if version != *command.ExpectedState {
			return &v2.DomainError{Code: "CAMPAIGN_STATE_CONFLICT", Status: 409, Detail: "personal state changed"}
		}
	}
	if command.ExpectedPublication != nil && *command.ExpectedPublication != pub.Version {
		return &v2.DomainError{Code: "CAMPAIGN_VERSION_CONFLICT", Status: 409, Detail: "publication changed"}
	}
	return nil
}

func interactionAlreadyAtTarget(operation string, state *v2.Interaction) bool {
	if state == nil {
		return false
	}
	switch operation {
	case "join":
		return state.Status == "joined"
	case "leave":
		return state.Status == "left"
	case "set-reminder":
		return state.Status == "set"
	case "remove-reminder":
		return state.Status == "removed"
	default:
		return false
	}
}
