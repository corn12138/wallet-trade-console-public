package v2

import (
	"fmt"
	"time"
)

type DomainError struct {
	Code   string
	Status int
	Detail string
}

func (e *DomainError) Error() string   { return fmt.Sprintf("%s: %s", e.Code, e.Detail) }
func InvalidInput(detail string) error { return &DomainError{"CAMPAIGN_INVALID_INPUT", 400, detail} }
func conflict(code string) error {
	return &DomainError{code, 409, "refresh authoritative state before confirming again"}
}

type Revision struct {
	ID                string
	CampaignID        string
	Input             RevisionInput
	MaterialTermsHash string
	ContentHash       string
}

type Publication struct {
	CampaignID         string
	Version            uint64
	InteractionVersion uint64
	Mode               string
	Active             *Revision
	HasAcceptedHistory bool
}

type Acceptance struct {
	Provenance         string
	RevisionID         *string
	PublicationVersion *uint64
	TermsHash          *string
	AcceptedAt         *time.Time
}

type Interaction struct {
	Kind       string
	Status     string
	Version    uint64
	Acceptance Acceptance
}

type InteractionChange struct {
	Before  *Interaction
	After   Interaction
	Changed bool
}

func Phase(publication Publication, now time.Time) string {
	if publication.Mode != "enabled" && publication.Mode != "paused" && publication.Mode != "archived" {
		return "unsupported"
	}
	if publication.Active == nil {
		return "unpublished"
	}
	input := publication.Active.Input
	rules := input.MaterialTerms.Rules
	if input.SchemaVersion != 1 || input.TermsCanonicalVersion != 1 || input.TemplateID != "campaignCardV1" || rules.Join.Kind != "walletJoinV1" || rules.Join.Version != 1 || rules.Reminder.Kind != "reminderIntentV1" || rules.Reminder.Version != 1 {
		return "unsupported"
	}
	terms := publication.Active.Input.MaterialTerms
	start, startErr := time.Parse(time.RFC3339, terms.StartAt)
	end, endErr := time.Parse(time.RFC3339, terms.EndAt)
	if startErr != nil || endErr != nil || !end.After(start) {
		return "unsupported"
	}
	if now.Before(start) {
		return "upcoming"
	}
	if !now.Before(end) {
		return "ended"
	}
	return "active"
}

func NextTransition(publication Publication, now time.Time) *time.Time {
	if publication.Active == nil || Phase(publication, now) == "unsupported" {
		return nil
	}
	for _, value := range []string{publication.Active.Input.MaterialTerms.StartAt, publication.Active.Input.MaterialTerms.EndAt} {
		at, err := time.Parse(time.RFC3339, value)
		if err == nil && now.Before(at) {
			return &at
		}
	}
	return nil
}
