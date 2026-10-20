package postgres

import (
	"context"
	"errors"
	"strconv"
	"time"

	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/application"
	"github.com/jackc/pgx/v5"
)

// adminRevisionLimit keeps the private view within the 50-item array contract.
// Older revisions stay addressable through preview by their stable ID.
const adminRevisionLimit = 50

// AdminCampaign is the administrator's pre-command snapshot. It never takes a
// writer lock, so every value is advisory: the publication command re-reads the
// row under lock and its CAS on expectedPublicationVersion is authoritative.
func (s *Store) AdminCampaign(ctx context.Context, campaignID string, capabilities v2.Capabilities) (application.AdminCampaignResponse, error) {
	var result application.AdminCampaignResponse
	err := s.readSnapshot(ctx, func(q *querySnapshot) error {
		view, err := q.adminView(campaignID, capabilities)
		if err != nil {
			return err
		}
		result = application.AdminCampaignResponse{SchemaVersion: 1, ServerNow: q.timestamp(), AsOf: q.timestamp(), Item: view}
		return nil
	})
	if err != nil {
		return application.AdminCampaignResponse{}, err
	}
	return result, nil
}

func (q *querySnapshot) adminView(campaignID string, capabilities v2.Capabilities) (application.AdminCampaignView, error) {
	var view application.AdminCampaignView
	var version, interactionVersion int64
	var active *string
	err := q.tx.QueryRow(q.ctx, `SELECT d.slug,d.source,p.publication_version,p.interaction_version,p.mode,p.active_revision_id::text
		FROM campaign_definitions d JOIN campaign_publications p ON p.campaign_id=d.id WHERE d.id=$1`, campaignID).Scan(&view.Slug, &view.Source, &version, &interactionVersion, &view.Mode, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		return view, notFound()
	}
	if err != nil {
		return view, err
	}
	if version < 0 || interactionVersion < 0 || (active != nil && version < 1) {
		return view, unavailable("stored publication version invalid")
	}
	pub := v2.Publication{CampaignID: campaignID, Version: uint64(version), InteractionVersion: uint64(interactionVersion), Mode: view.Mode}
	if err = q.tx.QueryRow(q.ctx, acceptedHistorySQL, campaignID).Scan(&pub.HasAcceptedHistory); err != nil {
		return view, err
	}
	view.CampaignID, view.ActiveRevisionID, view.HasAcceptedHistory = campaignID, active, pub.HasAcceptedHistory
	view.PublicationVersion = strconv.FormatInt(version, 10)
	view.InteractionVersion = strconv.FormatInt(interactionVersion, 10)
	view.EffectivePhase, view.MaterialTermsLock = "unpublished", v2.TermsOpen
	if active != nil {
		read, err := q.revision(campaignID, *active)
		if err != nil {
			return view, err
		}
		pub.Active = &read.revision
		view.EffectivePhase = "unsupported"
		if read.supported {
			view.EffectivePhase = v2.Phase(pub, q.now)
		}
	}
	// An unparseable active term cannot be compared, so the view says unknown
	// instead of guessing open; the writer rejects the same case as unsupported.
	if lock, err := v2.TermsLock(pub, q.now); err == nil {
		view.MaterialTermsLock = lock
	} else {
		view.MaterialTermsLock = "unknown"
	}
	view.Revisions, view.RevisionsTruncated, err = q.adminRevisions(campaignID, active, capabilities)
	return view, err
}

func (q *querySnapshot) adminRevisions(campaignID string, active *string, capabilities v2.Capabilities) ([]application.AdminRevisionSummary, bool, error) {
	rows, err := q.tx.Query(q.ctx, `SELECT r.id::text,r.created_at,r.schema_version,r.terms_canonical_version,
		EXISTS (SELECT 1 FROM campaign_publication_history h WHERE h.campaign_id=r.campaign_id AND h.after_revision_id=r.id)
		FROM campaign_revisions r WHERE r.campaign_id=$1 ORDER BY r.created_at DESC,r.id DESC LIMIT $2`, campaignID, adminRevisionLimit+1)
	if err != nil {
		return nil, false, err
	}
	type entry struct {
		id                string
		createdAt         time.Time
		schema, canonical int64
		published         bool
	}
	var entries []entry
	for rows.Next() {
		var e entry
		if err = rows.Scan(&e.id, &e.createdAt, &e.schema, &e.canonical, &e.published); err != nil {
			rows.Close()
			return nil, false, err
		}
		entries = append(entries, e)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, false, err
	}
	truncated := len(entries) > adminRevisionLimit
	if truncated {
		entries = entries[:adminRevisionLimit]
	}
	result := make([]application.AdminRevisionSummary, 0, len(entries))
	for _, e := range entries {
		// Each summary passes the same stored-integrity decoding as preview, so a
		// tampered row fails the view rather than presenting an unverified hash.
		read, err := q.revision(campaignID, e.id)
		if err != nil {
			return nil, false, err
		}
		summary := application.AdminRevisionSummary{ID: e.id, CreatedAt: e.createdAt.UTC().Format(time.RFC3339Nano), SchemaVersion: e.schema, TermsCanonicalVersion: e.canonical, ContentState: "unsupported", MaterialTermsHash: read.revision.MaterialTermsHash, ContentHash: read.revision.ContentHash, Published: e.published, Active: active != nil && *active == e.id}
		if read.contentAvailable(capabilities) {
			summary.ContentState = "ready"
		}
		result = append(result, summary)
	}
	return result, truncated, nil
}
