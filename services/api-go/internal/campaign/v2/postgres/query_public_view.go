package postgres

import (
	"errors"
	"strconv"
	"time"

	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/application"
	"github.com/jackc/pgx/v5"
)

// The immutable revision, aggregate counters and participant count are loaded
// together, so a 50-item page does not incur per-card database round trips.
// A legacy definition proves complete state only when an activation created
// it: such a definition names its activation, which the database requires at
// commit (migration 20261009200000), so the activation's own read-back sees it.
const publicProjection = `p.campaign_id,d.slug,p.active_revision_id::text AS revision_id,p.publication_version,p.interaction_version,p.mode,d.source,
 r.input,r.schema_version,r.terms_canonical_version,r.material_terms_hash,r.content_hash,
 (SELECT count(*) FROM campaign_interaction_states s WHERE s.campaign_id=p.campaign_id AND s.kind='join' AND s.status='joined') AS participant_count,
 ` + legacyActivatedSQL + ` AS legacy_activated`

const legacyActivatedSQL = `(d.source='legacy-unversioned' AND d.legacy_snapshot ? 'activationId')`
const publicFrom = `FROM campaign_publications p JOIN campaign_definitions d ON d.id=p.campaign_id JOIN campaign_revisions r ON r.campaign_id=p.campaign_id AND r.id=p.active_revision_id`

type publicRecord struct {
	id                          string
	slug                        *string
	revisionID                  string
	version, interactionVersion int64
	mode, source                string
	raw                         []byte
	schema, canonical           int64
	termsHash, contentHash      string
	count                       int64
	activated                   bool
}

func (r *publicRecord) targets() []any {
	return []any{&r.id, &r.slug, &r.revisionID, &r.version, &r.interactionVersion, &r.mode, &r.source, &r.raw, &r.schema, &r.canonical, &r.termsHash, &r.contentHash, &r.count, &r.activated}
}
func needsClientCapabilities(input v2.RevisionInput) bool {
	return input.DisplayContent.MediaAssetID != nil || input.Cta.ActionKind != "none"
}

func (q *querySnapshot) publicView(id string, variants v2.Variants, capabilities v2.Capabilities) (application.PublicCampaignView, error) {
	var row publicRecord
	err := q.tx.QueryRow(q.ctx, `SELECT `+publicProjection+` `+publicFrom+` WHERE p.campaign_id=$1`, id).Scan(row.targets()...)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.PublicCampaignView{}, notFound()
	}
	if err != nil {
		return application.PublicCampaignView{}, err
	}
	return q.publicRecordView(row, variants, capabilities)
}
func (q *querySnapshot) publicRecordView(row publicRecord, variants v2.Variants, capabilities v2.Capabilities) (application.PublicCampaignView, error) {
	var result application.PublicCampaignView
	if row.version < 1 || row.interactionVersion < 0 || row.count < 0 {
		return result, unavailable("stored publication version invalid")
	}
	read, err := decodeReadRevision(row.revisionID, row.id, row.raw, row.schema, row.canonical, row.termsHash, row.contentHash)
	if err != nil {
		return result, err
	}
	pub := v2.Publication{CampaignID: row.id, Version: uint64(row.version), InteractionVersion: uint64(row.interactionVersion), Mode: row.mode, Active: &read.revision}
	if err = v2.ValidateStoredPublicationEnvelope(pub); err != nil {
		return result, unavailable("stored publication envelope invalid")
	}
	result = application.PublicCampaignView{ID: row.id, Slug: row.slug, RevisionID: row.revisionID, PublicationVersion: strconv.FormatInt(row.version, 10), InteractionVersion: strconv.FormatInt(row.interactionVersion, 10), Mode: row.mode, ContentHash: row.contentHash, MaterialTermsHash: row.termsHash, ContentState: "unsupported", EffectivePhase: "unsupported", StatisticsState: "unavailable"}
	if read.supported {
		result.EffectivePhase = v2.Phase(pub, q.now)
		if next := v2.NextTransition(pub, q.now); next != nil {
			result.NextTransitionAt = stringPointer(next.UTC().Format(time.RFC3339Nano))
		}
		// Content with media or a trade entry is ready only for a client that
		// checks both against its own registries; an older client renders the
		// read-only unsupported card instead of offering admission it cannot
		// fully show (docs/campaign-content-capabilities-protocol-2026-10-13.md §6).
		if read.contentAvailable(capabilities) && (variants.ContentCapabilities || !needsClientCapabilities(read.revision.Input)) {
			result.ContentState = "ready"
			result.Content = &read.revision.Input
		}
	} else if read.baseline != nil {
		// A legacy baseline keeps its legacy window. Its content is shown only
		// to clients that negotiated the read-only variant; for every other
		// client it stays unsupported, which they already render safely.
		result.EffectivePhase = read.baseline.Phase(q.now)
		if next := read.baseline.NextTransition(q.now); next != nil {
			result.NextTransitionAt = stringPointer(next.UTC().Format(time.RFC3339Nano))
		}
		if variants.LegacyBaseline {
			view, err := read.baseline.PublicView()
			if err != nil {
				return application.PublicCampaignView{}, unavailable("stored legacy baseline capture time invalid")
			}
			result.ContentState = v2.ContentStateLegacyBaseline
			result.Baseline = &view
		}
	}
	// Legacy absence remains unknown until a controlled import proves completeness.
	if row.source == "created" || (row.source == "legacy-unversioned" && row.activated) {
		result.ParticipantCount = stringPointer(strconv.FormatInt(row.count, 10))
		result.StatisticsState = "ready"
	}
	return result, nil
}
