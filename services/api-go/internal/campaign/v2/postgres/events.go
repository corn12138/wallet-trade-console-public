package postgres

import (
	"encoding/json"
	"sort"
	"strconv"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/application"
)

type pendingEvent struct {
	stream, topic string
	owner         any
	payload       eventPayload
}
type eventPayload struct {
	EventID            string                 `json:"eventId"`
	Source             string                 `json:"source"`
	CampaignID         string                 `json:"campaignId"`
	AggregateID        string                 `json:"aggregateId"`
	AggregateVersion   string                 `json:"aggregateVersion"`
	PublicationVersion string                 `json:"publicationVersion"`
	InteractionVersion string                 `json:"interactionVersion"`
	EventSchema        int                    `json:"eventSchema"`
	OccurredAt         string                 `json:"occurredAt"`
	State              *application.StateView `json:"state,omitempty"`
}

func (t *transaction) saveEvents(record application.Record, mutation application.Mutation) error {
	base := eventPayload{Source: "campaign", CampaignID: t.campaignID, PublicationVersion: strconv.FormatUint(record.Result.PublicationVersion, 10), InteractionVersion: strconv.FormatUint(record.Result.InteractionVersion, 10), EventSchema: 1, OccurredAt: record.At.UTC().Format("2006-01-02T15:04:05.999999Z")}
	var pending []pendingEvent
	if mutation.Publication != nil && (mutation.Publication.Before.Active != nil || mutation.Publication.After.Active != nil) {
		pending = append(pending, pendingEvent{stream: streamKey(t.campaignID, "publication", "public"), topic: "campaign.publication.changed", payload: base})
	}
	if mutation.Interaction != nil && mutation.Interaction.Changed {
		pending = append(pending, pendingEvent{stream: streamKey(t.campaignID, "interaction", "public"), topic: "campaign.interaction.changed", payload: base})
		scope, err := t.privateScope(record.Key.Actor)
		if err != nil {
			return err
		}
		personal := base
		personal.State = record.Result.State
		pending = append(pending, pendingEvent{stream: streamKey(t.campaignID, "interaction", scope), topic: "campaign.personal.changed", owner: record.Key.Actor, payload: personal})
	}
	// Multiple stream counters can share a transaction; their locking order is
	// independent of event kind and of the order commands were constructed.
	sort.Slice(pending, func(i, j int) bool { return pending[i].stream < pending[j].stream })
	for _, event := range pending {
		if err := t.saveEvent(record, event); err != nil {
			return err
		}
	}
	return nil
}

func streamKey(campaign, family, scope string) string {
	encoded, _ := json.Marshal([]string{"campaign", campaign, family, scope})
	return string(encoded)
}

func (t *transaction) privateScope(wallet string) (string, error) {
	id, err := application.NewID()
	if err != nil {
		return "", err
	}
	_, err = t.tx.Exec(t.ctx, `INSERT INTO campaign_private_scopes(wallet_address,scope_id) VALUES ($1,$2) ON CONFLICT(wallet_address) DO NOTHING`, wallet, id)
	if err != nil {
		return "", err
	}
	err = t.tx.QueryRow(t.ctx, `SELECT scope_id::text FROM campaign_private_scopes WHERE wallet_address=$1`, wallet).Scan(&id)
	return id, err
}

func (t *transaction) saveEvent(record application.Record, event pendingEvent) error {
	id, err := application.NewID()
	if err != nil {
		return err
	}
	event.payload.EventID = id
	var version int64
	err = t.tx.QueryRow(t.ctx, `INSERT INTO outbox_streams(stream_key,source,last_stream_version) VALUES ($1,'campaign',1)
		ON CONFLICT(stream_key) DO UPDATE SET last_stream_version=outbox_streams.last_stream_version+1 RETURNING last_stream_version`, event.stream).Scan(&version)
	if err != nil {
		return err
	}
	event.payload.AggregateID = event.stream
	event.payload.AggregateVersion = strconv.FormatInt(version, 10)
	raw, err := json.Marshal(event.payload)
	if err != nil {
		return err
	}
	_, err = t.tx.Exec(t.ctx, `INSERT INTO durable_outbox(id,source,aggregate_id,aggregate_version,topic,owner_scope,payload_version,payload,occurred_at,available_at)
		VALUES ($1,'campaign',$2,$3,$4,$5,1,$6,$7,$7)`, id, event.stream, version, event.topic, event.owner, raw, record.At)
	return err
}
