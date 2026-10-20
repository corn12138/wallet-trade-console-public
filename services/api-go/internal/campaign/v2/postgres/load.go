package postgres

import (
	"encoding/json"
	"errors"

	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/application"
	"github.com/jackc/pgx/v5"
)

func (t *transaction) Publication(id string) (v2.Publication, error) {
	var pub v2.Publication
	if t.campaignID != "" && t.campaignID != id {
		return pub, v2.InvalidInput("cross-campaign transaction")
	}
	var active *string
	var version, interactionVersion int64
	err := t.tx.QueryRow(t.ctx, `SELECT campaign_id, publication_version, interaction_version, mode, active_revision_id::text
		FROM campaign_publications WHERE campaign_id=$1 FOR UPDATE`, id).Scan(&pub.CampaignID, &version, &interactionVersion, &pub.Mode, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		return pub, notFound()
	}
	if err != nil {
		return pub, err
	}
	t.campaignID = id
	pub.Version = uint64(version)
	pub.InteractionVersion = uint64(interactionVersion)
	if active != nil {
		pub.Active = &v2.Revision{ID: *active, CampaignID: id}
	}
	err = t.tx.QueryRow(t.ctx, acceptedHistorySQL, id).Scan(&pub.HasAcceptedHistory)
	return pub, err
}

// Unknown legacy history conservatively locks terms; it is not evidence of a
// participant and must never be reused for counts or verified provenance.
// Cancelled accepted rows remain binding; a neverAccepted tombstone does not.
// The administrator view reads the same predicate so its lock prediction and
// the locked publication decision cannot drift apart.
const acceptedHistorySQL = `SELECT EXISTS (SELECT 1 FROM campaign_interaction_history
		WHERE campaign_id=$1 AND acceptance_provenance IN ('verified','legacyUnknown'))
		OR EXISTS (SELECT 1 FROM campaign_definitions WHERE id=$1 AND source='legacy-unversioned')`

func (t *transaction) Revision(id string) (v2.Revision, error) {
	var value v2.Revision
	var raw []byte
	var schemaVersion, canonicalVersion int64
	err := t.tx.QueryRow(t.ctx, `SELECT id::text,campaign_id,input,schema_version,terms_canonical_version,material_terms_hash,content_hash
		FROM campaign_revisions WHERE campaign_id=$1 AND id=$2`, t.campaignID, id).Scan(&value.ID, &value.CampaignID, &raw, &schemaVersion, &canonicalVersion, &value.MaterialTermsHash, &value.ContentHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return value, notFound()
	}
	if err != nil {
		return value, err
	}
	// A verified legacy baseline is read-only content, not a damaged v1
	// revision: commands that need executable content get a definite refusal
	// rather than a dependency failure a client would have to reconcile.
	if baseline, err := storedBaseline(value, raw, schemaVersion, canonicalVersion); baseline {
		if err != nil {
			return value, err
		}
		return value, &v2.DomainError{Code: "CAMPAIGN_UNSUPPORTED", Status: 422, Detail: "a legacy baseline is read-only"}
	}
	// Decode the raw JSONB before binding a Go struct so unknown fields cannot
	// disappear during hydration and evade the closed-schema contract.
	value.Input, err = v2.DecodeRevision(raw)
	if err != nil {
		var domain *v2.DomainError
		if errors.As(err, &domain) && domain.Status == 422 {
			return value, err
		}
		return value, unavailable("stored revision schema invalid")
	}
	if value.Input.SchemaVersion != schemaVersion || value.Input.TermsCanonicalVersion != canonicalVersion {
		return value, unavailable("stored revision version mismatch")
	}
	return value, v2.ValidateStoredRevisionIntegrity(value)
}

// storedBaseline reports whether raw is a legacyBaselineV1 revision and, if
// so, verifies it the way the read side does.
func storedBaseline(value v2.Revision, raw []byte, schema, canonical int64) (bool, error) {
	var envelope struct {
		TemplateID string `json:"templateId"`
	}
	if schema != 1 || canonical != 1 || json.Unmarshal(raw, &envelope) != nil || envelope.TemplateID != v2.LegacyBaselineTemplate {
		return false, nil
	}
	read, err := decodeReadRevision(value.ID, value.CampaignID, raw, schema, canonical, value.MaterialTermsHash, value.ContentHash)
	if err != nil || read.baseline == nil {
		return true, unavailable("stored legacy baseline integrity invalid")
	}
	return true, nil
}

func (t *transaction) WasPublished(id string) (bool, error) {
	var exists bool
	err := t.tx.QueryRow(t.ctx, `SELECT EXISTS (SELECT 1 FROM campaign_publication_history WHERE campaign_id=$1 AND after_revision_id=$2)`, t.campaignID, id).Scan(&exists)
	return exists, err
}

func (t *transaction) Interaction(wallet, kind string) (*v2.Interaction, error) {
	value := &v2.Interaction{}
	var rowVersion int64
	var acceptedVersion *int64
	err := t.tx.QueryRow(t.ctx, `SELECT kind,status,row_version,acceptance_provenance,accepted_revision_id::text,accepted_publication_version,accepted_terms_hash,accepted_at
		FROM campaign_interaction_states WHERE campaign_id=$1 AND wallet_address=$2 AND kind=$3`, t.campaignID, wallet, kind).Scan(&value.Kind, &value.Status, &rowVersion, &value.Acceptance.Provenance, &value.Acceptance.RevisionID, &acceptedVersion, &value.Acceptance.TermsHash, &value.Acceptance.AcceptedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	value.Version = uint64(rowVersion)
	if acceptedVersion != nil {
		version := uint64(*acceptedVersion)
		value.Acceptance.PublicationVersion = &version
	}
	return value, nil
}

func (t *transaction) Command(key application.CommandKey) (*application.Record, error) {
	value := &application.Record{Key: key}
	var result []byte
	err := t.tx.QueryRow(t.ctx, `SELECT id::text,payload_hash,result FROM campaign_commands
		WHERE actor=$1 AND operation=$2 AND target_id=$3 AND client_command_id=$4`, key.Actor, key.Operation, key.TargetID, key.ClientID).Scan(&value.ID, &value.PayloadHash, &result)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(result, &value.Result); err != nil {
		return nil, unavailable("stored command result invalid")
	}
	return value, nil
}
