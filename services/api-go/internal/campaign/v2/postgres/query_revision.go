package postgres

import (
	"encoding/json"
	"errors"
	"regexp"

	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
	"github.com/jackc/pgx/v5"
)

type readRevision struct {
	revision  v2.Revision
	supported bool
	// baseline is set for a verified legacyBaselineV1 revision. It is never
	// supported executable content.
	baseline *v2.LegacyBaseline
}

var readHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var readUUIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
var readIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

func unsupported(err error) bool {
	var domain *v2.DomainError
	return errors.As(err, &domain) && domain.Code == "CAMPAIGN_UNSUPPORTED"
}

// Known canonicalization can verify even a future template's raw JSON. Unknown
// canonicalization remains inert metadata and is never presented as validated
// executable content or as a verified accepted-terms snapshot.
func decodeReadRevision(id, campaignID string, raw []byte, schema, canonical int64, termsHash, contentHash string) (readRevision, error) {
	result := readRevision{revision: v2.Revision{ID: id, CampaignID: campaignID, MaterialTermsHash: termsHash, ContentHash: contentHash}}
	invalid := func() (readRevision, error) { return readRevision{}, unavailable("stored revision integrity invalid") }
	if !readUUIDPattern.MatchString(id) || !readIDPattern.MatchString(campaignID) || schema < 1 || canonical < 1 || !readHashPattern.MatchString(termsHash) || !readHashPattern.MatchString(contentHash) {
		return invalid()
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(raw, &envelope) != nil {
		return invalid()
	}
	var inputSchema, inputCanonical int64
	if json.Unmarshal(envelope["schemaVersion"], &inputSchema) != nil || json.Unmarshal(envelope["termsCanonicalVersion"], &inputCanonical) != nil || inputSchema != schema || inputCanonical != canonical {
		return invalid()
	}
	if canonical != 1 {
		return result, nil
	}
	var template string
	if schema == 1 && json.Unmarshal(envelope["templateId"], &template) == nil && template == v2.LegacyBaselineTemplate {
		return decodeReadBaseline(result, raw, envelope)
	}
	known, discriminatorErr := knownReadContract(envelope, schema)
	if discriminatorErr != nil {
		return invalid()
	}
	if !known {
		hash, err := v2.CanonicalHash(json.RawMessage(raw))
		if err != nil || hash != contentHash {
			return invalid()
		}
		hash, err = v2.CanonicalHash(envelope["materialTerms"])
		if err != nil || hash != termsHash {
			return invalid()
		}
		return result, nil
	}
	input, err := v2.DecodeRevision(raw)
	if unsupported(err) {
		hash, hashErr := v2.CanonicalHash(json.RawMessage(raw))
		if hashErr != nil || hash != contentHash {
			return invalid()
		}
		hash, hashErr = v2.CanonicalHash(envelope["materialTerms"])
		if hashErr != nil || hash != termsHash {
			return invalid()
		}
		return result, nil
	}
	if err != nil {
		return invalid()
	}
	result.revision.Input = input
	if err = v2.ValidateStoredRevisionIntegrity(result.revision); unsupported(err) {
		return result, nil
	} else if err != nil {
		return invalid()
	}
	result.supported = true
	return result, nil
}

// decodeReadBaseline accepts a legacy baseline only in its exact stored shape
// and with both hashes recomputed from the stored JSON.
func decodeReadBaseline(result readRevision, raw []byte, envelope map[string]json.RawMessage) (readRevision, error) {
	invalid := func() (readRevision, error) {
		return readRevision{}, unavailable("stored legacy baseline integrity invalid")
	}
	input, err := v2.DecodeLegacyBaseline(raw)
	if err != nil {
		return invalid()
	}
	if hash, err := v2.CanonicalHash(json.RawMessage(raw)); err != nil || hash != result.revision.ContentHash {
		return invalid()
	}
	if hash, err := v2.CanonicalHash(envelope["materialTerms"]); err != nil || hash != result.revision.MaterialTermsHash {
		return invalid()
	}
	start, err := v2.ParseLegacyTime(input.MaterialTerms.Legacy.StartAt)
	if err != nil {
		return invalid()
	}
	end, err := v2.ParseLegacyTime(input.MaterialTerms.Legacy.EndAt)
	if err != nil {
		return invalid()
	}
	result.baseline = &v2.LegacyBaseline{RevisionID: result.revision.ID, CampaignID: result.revision.CampaignID, Input: input, Start: start, End: end,
		MaterialTermsHash: result.revision.MaterialTermsHash, ContentHash: result.revision.ContentHash}
	return result, nil
}

func (q *querySnapshot) revision(campaignID, id string) (readRevision, error) {
	key := campaignID + ":" + id
	if value, ok := q.revisions[key]; ok {
		return value, nil
	}
	var rid, cid, termsHash, contentHash string
	var raw []byte
	var schema, canonical int64
	err := q.tx.QueryRow(q.ctx, `SELECT id::text,campaign_id,input,schema_version,terms_canonical_version,material_terms_hash,content_hash FROM campaign_revisions WHERE campaign_id=$1 AND id=$2`, campaignID, id).Scan(&rid, &cid, &raw, &schema, &canonical, &termsHash, &contentHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return readRevision{}, notFound()
	}
	if err != nil {
		return readRevision{}, err
	}
	value, err := decodeReadRevision(rid, cid, raw, schema, canonical, termsHash, contentHash)
	if err == nil {
		if q.revisions == nil {
			q.revisions = map[string]readRevision{}
		}
		q.revisions[key] = value
	}
	return value, err
}

// Integrity is already established once per immutable revision in this snapshot.
// Live registry removal only controls rendering/execution; it cannot invalidate
// the stored acceptance or require re-hashing the same large copy for each row.
func (r readRevision) contentAvailable(capabilities v2.Capabilities) bool {
	if !r.supported {
		return false
	}
	input := r.revision.Input
	if !capabilities.Templates[input.TemplateID] {
		return false
	}
	if asset := input.DisplayContent.MediaAssetID; asset != nil && !capabilities.MediaAssets[*asset] {
		return false
	}
	if cta := input.Cta; cta.ActionKind == "openTrade" {
		return cta.ChainID != nil && cta.MarketID != nil && capabilities.TradeMarkets[*cta.ChainID][*cta.MarketID]
	}
	return input.Cta.ActionKind == "none"
}
