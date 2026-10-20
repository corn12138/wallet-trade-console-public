package application

import (
	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
	"strconv"
	"time"
)

type StateView struct {
	Kind                       string     `json:"kind"`
	Status                     string     `json:"status"`
	StateVersion               string     `json:"stateVersion"`
	AcceptanceProvenance       string     `json:"acceptanceProvenance"`
	AcceptedRevisionID         *string    `json:"acceptedRevisionId"`
	AcceptedPublicationVersion *string    `json:"acceptedPublicationVersion"`
	AcceptedTermsHash          *string    `json:"acceptedTermsHash"`
	AcceptedAt                 *time.Time `json:"acceptedAt"`
}

func stateView(value *v2.Interaction) *StateView {
	if value == nil {
		return nil
	}
	result := &StateView{Kind: value.Kind, Status: value.Status, StateVersion: strconv.FormatUint(value.Version, 10), AcceptanceProvenance: value.Acceptance.Provenance, AcceptedRevisionID: value.Acceptance.RevisionID, AcceptedTermsHash: value.Acceptance.TermsHash, AcceptedAt: value.Acceptance.AcceptedAt}
	if version := value.Acceptance.PublicationVersion; version != nil {
		text := strconv.FormatUint(*version, 10)
		result.AcceptedPublicationVersion = &text
	}
	return result
}
func snapshot(pub v2.Publication, state *v2.Interaction) Result {
	result := Result{CampaignID: pub.CampaignID, PublicationVersion: pub.Version, InteractionVersion: pub.InteractionVersion, State: stateView(state)}
	if pub.Active != nil {
		result.RevisionID = pub.Active.ID
	}
	return result
}
func interactionKind(operation string) string {
	switch operation {
	case "join", "leave":
		return "join"
	case "set-reminder", "remove-reminder":
		return "reminder"
	default:
		return ""
	}
}
