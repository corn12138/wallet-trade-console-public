package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strconv"
	"time"

	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/application"
	"github.com/jackc/pgx/v5"
)

// LegacyActivation is everything activating one legacy campaign writes into
// the versioned model, inside the caller's transaction
// (docs/campaign-legacy-activation-protocol-2026-10-09.md §2 step 4).
type LegacyActivation struct {
	ActivationID, CampaignID, Actor string
	At                              time.Time
	// The main legacy-import command.
	ClientCommandID, PayloadHash, Reason, RequestID string
	RunID, Decision                                 string
	// Snapshot is the definition's legacy_snapshot: the archived row and its
	// capture binding.
	Snapshot     json.RawMessage
	Baseline     v2.LegacyBaselineInput
	Publish      bool
	Interactions []LegacyInteraction
}

// LegacyInteraction is one merged legacy state: a lowercase wallet and kind,
// the earliest legacy time and the archived rows it came from.
type LegacyInteraction struct {
	Wallet, Kind string
	AcceptedAt   time.Time
	SourceRows   []string
}

type LegacyActivationResult struct {
	CommandID, PublishCommandID, RevisionID string
	PublicationVersion, InteractionVersion  uint64
}

var activationWalletPattern = regexp.MustCompile(`^0x[0-9a-f]{40}$`)

// WriteLegacyActivation writes the definition, baseline revision and
// publication row, the main legacy-import command with its audit, the present
// publish (when public) and one legacy-import command, state, history, audit
// and outbox pair per imported interaction. The caller holds the campaign's
// fence lock, has verified the legacy rows and writes the activation record
// after comparing the result; the database guards check every binding again.
func WriteLegacyActivation(ctx context.Context, tx pgx.Tx, a LegacyActivation) (LegacyActivationResult, error) {
	var result LegacyActivationResult
	if !activationWalletPattern.MatchString(a.Actor) || !readIDPattern.MatchString(a.CampaignID) || !readUUIDPattern.MatchString(a.ActivationID) {
		return result, v2.InvalidInput("legacy activation identity")
	}
	if err := a.Baseline.Validate(); err != nil {
		return result, err
	}
	for i, item := range a.Interactions {
		if !activationWalletPattern.MatchString(item.Wallet) || (item.Kind != "join" && item.Kind != "reminder") {
			return result, v2.InvalidInput("legacy interaction")
		}
		if i > 0 && (a.Interactions[i-1].Wallet+a.Interactions[i-1].Kind) >= (item.Wallet+item.Kind) {
			return result, v2.InvalidInput("legacy interactions must be distinct and ordered")
		}
	}
	termsHash, contentHash, err := a.Baseline.Hashes()
	if err != nil {
		return result, err
	}
	if result.RevisionID, err = application.NewID(); err != nil {
		return result, err
	}
	input, err := json.Marshal(a.Baseline)
	if err != nil {
		return result, err
	}
	t := &transaction{ctx: ctx, tx: tx, campaignID: a.CampaignID}
	if _, err = tx.Exec(ctx, `INSERT INTO campaign_definitions(id,slug,source,legacy_snapshot,created_by,created_at) VALUES ($1,NULL,'legacy-unversioned',$2,$3,$4)`,
		a.CampaignID, a.Snapshot, a.Actor, a.At); err != nil {
		return result, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO campaign_revisions(id,campaign_id,schema_version,terms_canonical_version,input,material_terms_hash,content_hash,created_by,created_at)
		VALUES ($1,$2,1,1,$3,$4,$5,$6,$7)`, result.RevisionID, a.CampaignID, input, termsHash, contentHash, a.Actor, a.At); err != nil {
		return result, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO campaign_publications(campaign_id,updated_by,updated_at) VALUES ($1,$2,$3)`, a.CampaignID, a.Actor, a.At); err != nil {
		return result, err
	}
	if a.Publish {
		result.PublicationVersion = 1
	}
	result.InteractionVersion = uint64(len(a.Interactions))
	main := map[string]any{"campaignId": a.CampaignID, "activationId": a.ActivationID, "runId": a.RunID, "decision": a.Decision, "revisionId": result.RevisionID,
		"publicationVersion": strconv.FormatUint(result.PublicationVersion, 10), "interactionVersion": strconv.FormatUint(result.InteractionVersion, 10),
		"importedInteractions": len(a.Interactions)}
	if result.CommandID, err = t.legacyCommand(a, "legacy-import", a.ClientCommandID, a.PayloadHash, nil, nil, main); err != nil {
		return result, err
	}
	if a.Publish {
		if result.PublishCommandID, err = t.legacyPublish(a, result.RevisionID); err != nil {
			return result, err
		}
	}
	for i, item := range a.Interactions {
		if err = t.legacyInteraction(a, item, uint64(i+1), result.PublicationVersion); err != nil {
			return result, err
		}
	}
	return result, nil
}

// legacyCommand records a command and its audit event at the activation time.
func (t *transaction) legacyCommand(a LegacyActivation, operation, clientID, payloadHash string, expectedPublication, expectedState *uint64, result map[string]any) (string, error) {
	id, err := application.NewID()
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	if _, err = t.tx.Exec(t.ctx, `INSERT INTO campaign_commands(id,actor,operation,target_id,client_command_id,payload_hash,
		expected_publication_version,expected_state_version,reason,request_id,result,occurred_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, id, a.Actor, operation, a.CampaignID, clientID, payloadHash,
		integer(expectedPublication), integer(expectedState), a.Reason, a.RequestID, raw, a.At); err != nil {
		return "", err
	}
	auditID, err := application.NewID()
	if err != nil {
		return "", err
	}
	_, err = t.tx.Exec(t.ctx, `INSERT INTO campaign_audit_events(id,campaign_id,command_id,actor,operation,changes,occurred_at) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		auditID, a.CampaignID, id, a.Actor, operation, raw, a.At)
	return id, err
}

// legacyPublish is the present, real publish of the baseline (D2): the same
// administrator, expected version 0, publication history version 1.
func (t *transaction) legacyPublish(a LegacyActivation, revisionID string) (string, error) {
	clientID, err := newCommandID(a.At)
	if err != nil {
		return "", err
	}
	zero := uint64(0)
	payloadHash, err := v2.CanonicalHash(map[string]any{"clientCommandId": clientID, "reason": a.Reason, "expectedPublicationVersion": "0",
		"operation": "publish", "candidateRevisionId": revisionID})
	if err != nil {
		return "", err
	}
	result := application.Result{CampaignID: a.CampaignID, RevisionID: revisionID, PublicationVersion: 1, Changed: true}
	raw, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	var fields map[string]any
	if err = json.Unmarshal(raw, &fields); err != nil {
		return "", err
	}
	id, err := t.legacyCommand(a, "publish", clientID, payloadHash, &zero, nil, fields)
	if err != nil {
		return "", err
	}
	tag, err := t.tx.Exec(t.ctx, `UPDATE campaign_publications SET publication_version=1,active_revision_id=$2,mode='enabled',updated_by=$3,updated_at=$4
		WHERE campaign_id=$1 AND publication_version=0`, a.CampaignID, revisionID, a.Actor, a.At)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() != 1 {
		return "", unavailable("publication lock invariant")
	}
	if _, err = t.tx.Exec(t.ctx, `INSERT INTO campaign_publication_history(campaign_id,publication_version,command_id,actor,before_mode,after_mode,before_revision_id,after_revision_id,occurred_at)
		VALUES ($1,1,$2,$3,'enabled','enabled',NULL,$4,$5)`, a.CampaignID, id, a.Actor, revisionID, a.At); err != nil {
		return "", err
	}
	record := application.Record{At: a.At}
	event := eventPayload{Source: "campaign", CampaignID: a.CampaignID, PublicationVersion: "1", InteractionVersion: "0", EventSchema: 1, OccurredAt: a.At.UTC().Format("2006-01-02T15:04:05.999999Z")}
	return id, t.saveEvent(record, pendingEvent{stream: streamKey(a.CampaignID, "publication", "public"), topic: "campaign.publication.changed", payload: event})
}

// legacyInteraction imports one merged legacy state as its own legacy-import
// command: legacyUnknown, row version 1, the earliest legacy time kept as the
// accepted-at fact and the activation time as the change time.
func (t *transaction) legacyInteraction(a LegacyActivation, item LegacyInteraction, version, publication uint64) error {
	status := "joined"
	if item.Kind == "reminder" {
		status = "set"
	}
	clientID, err := newCommandID(a.At)
	if err != nil {
		return err
	}
	sources := item.SourceRows
	if sources == nil {
		sources = []string{}
	}
	acceptedAt := item.AcceptedAt.UTC()
	payloadHash, err := v2.CanonicalHash(map[string]any{"activationId": a.ActivationID, "walletAddress": item.Wallet, "kind": item.Kind, "sourceRows": sources})
	if err != nil {
		return err
	}
	zero := uint64(0)
	result := map[string]any{"activationId": a.ActivationID, "campaignId": a.CampaignID, "walletAddress": item.Wallet, "kind": item.Kind, "status": status,
		"acceptedAt": acceptedAt.Format(time.RFC3339Nano), "sourceRows": sources, "interactionVersion": strconv.FormatUint(version, 10)}
	id, err := t.legacyCommand(a, "legacy-import", clientID, payloadHash, nil, &zero, result)
	if err != nil {
		return err
	}
	if _, err = t.tx.Exec(t.ctx, `INSERT INTO campaign_interaction_states(campaign_id,wallet_address,kind,status,row_version,acceptance_provenance,
		accepted_revision_id,accepted_publication_version,accepted_terms_hash,accepted_at,changed_at)
		VALUES ($1,$2,$3,$4,1,'legacyUnknown',NULL,NULL,NULL,$5,$6)`, a.CampaignID, item.Wallet, item.Kind, status, acceptedAt, a.At); err != nil {
		return err
	}
	if _, err = t.tx.Exec(t.ctx, `INSERT INTO campaign_interaction_history(campaign_id,wallet_address,kind,row_version,interaction_version,command_id,before_status,after_status,
		acceptance_provenance,accepted_revision_id,accepted_publication_version,accepted_terms_hash,accepted_at,occurred_at)
		VALUES ($1,$2,$3,1,$4,$5,NULL,$6,'legacyUnknown',NULL,NULL,NULL,$7,$8)`, a.CampaignID, item.Wallet, item.Kind, int64(version), id, status, acceptedAt, a.At); err != nil {
		return err
	}
	tag, err := t.tx.Exec(t.ctx, `UPDATE campaign_publications SET interaction_version=$2,updated_by=$3,updated_at=$4 WHERE campaign_id=$1 AND interaction_version=$5`,
		a.CampaignID, int64(version), a.Actor, a.At, int64(version-1))
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return unavailable("interaction lock invariant")
	}
	record := application.Record{At: a.At}
	base := eventPayload{Source: "campaign", CampaignID: a.CampaignID, PublicationVersion: strconv.FormatUint(publication, 10), InteractionVersion: strconv.FormatUint(version, 10),
		EventSchema: 1, OccurredAt: a.At.UTC().Format("2006-01-02T15:04:05.999999Z")}
	scope, err := t.privateScope(item.Wallet)
	if err != nil {
		return err
	}
	personal := base
	personal.State = &application.StateView{Kind: item.Kind, Status: status, StateVersion: "1", AcceptanceProvenance: "legacyUnknown", AcceptedAt: &acceptedAt}
	pending := []pendingEvent{
		{stream: streamKey(a.CampaignID, "interaction", "public"), topic: "campaign.interaction.changed", payload: base},
		{stream: streamKey(a.CampaignID, "interaction", scope), topic: "campaign.personal.changed", owner: item.Wallet, payload: personal},
	}
	if pending[1].stream < pending[0].stream {
		pending[0], pending[1] = pending[1], pending[0]
	}
	for _, event := range pending {
		if err = t.saveEvent(record, event); err != nil {
			return err
		}
	}
	return nil
}

// newCommandID returns a UUIDv7 for an internal command, like the ones clients
// generate.
func newCommandID(at time.Time) (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	millis := at.UnixMilli()
	for i := 5; i >= 0; i-- {
		raw[i] = byte(millis)
		millis >>= 8
	}
	raw[6] = (raw[6] & 0x0f) | 0x70
	raw[8] = (raw[8] & 0x3f) | 0x80
	value := hex.EncodeToString(raw[:])
	return value[:8] + "-" + value[8:12] + "-" + value[12:16] + "-" + value[16:20] + "-" + value[20:], nil
}
