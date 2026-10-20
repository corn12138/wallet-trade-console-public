package eventfeed

import (
	"encoding/json"
	"regexp"
	"strconv"
	"time"

	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/durableoutbox"
)

var eventIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// Only invalidation metadata is decoded and copied. Personal state, unknown
// fields, and stored payload bytes can never become a response implicitly.
type payloadMetadata struct {
	EventID            string `json:"eventId"`
	Source             string `json:"source"`
	CampaignID         string `json:"campaignId"`
	AggregateID        string `json:"aggregateId"`
	AggregateVersion   string `json:"aggregateVersion"`
	PublicationVersion string `json:"publicationVersion"`
	InteractionVersion string `json:"interactionVersion"`
	EventSchema        int    `json:"eventSchema"`
	OccurredAt         string `json:"occurredAt"`
}

func validVersion(raw string) bool {
	if !decimalPattern.MatchString(raw) {
		return false
	}
	_, err := strconv.ParseInt(raw, 10, 64)
	return err == nil
}
func invalidation(event durableoutbox.Event, id, family, stream, owner string) (v2.EventFeedHint, bool) {
	var empty v2.EventFeedHint
	if event.Source != "campaign" || event.PayloadVersion != 1 || event.Stream != stream || event.Topic != "campaign."+family+".changed" ||
		!eventIDPattern.MatchString(event.ID) || event.ID == "00000000-0000-0000-0000-000000000000" || len(event.Payload) > 64*1024 {
		return empty, false
	}
	if (owner == "" && event.OwnerScope != nil) || (owner != "" && (event.OwnerScope == nil || *event.OwnerScope != owner)) {
		return empty, false
	}
	var body payloadMetadata
	if json.Unmarshal(event.Payload, &body) != nil || body.EventID != event.ID || body.Source != "campaign" || body.CampaignID != id || body.AggregateID != stream ||
		body.AggregateVersion != strconv.FormatInt(event.Version, 10) || body.EventSchema != 1 || !validVersion(body.PublicationVersion) || !validVersion(body.InteractionVersion) {
		return empty, false
	}
	at, err := time.Parse(time.RFC3339Nano, body.OccurredAt)
	if err != nil || !at.Equal(event.OccurredAt) {
		return empty, false
	}
	return v2.EventFeedHint{EventID: event.ID, Source: "campaign", StreamVersion: body.AggregateVersion, PublicationVersion: body.PublicationVersion,
		InteractionVersion: body.InteractionVersion, EventSchema: 1, OccurredAt: event.OccurredAt.UTC().Format(time.RFC3339Nano)}, true
}
