package postgres

import (
	"context"
	"encoding/json"

	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/application"
)

// Casts are nested behind a bounded digit check: future/unknown JSON cannot
// turn the public compatibility path into a PostgreSQL cast failure.
const publicSortSQL = `CASE WHEN jsonb_typeof(r.input->'displayContent'->'sortOrder')='number'
 AND r.input#>>'{displayContent,sortOrder}' ~ '^[0-9]{1,5}$'
 THEN CASE WHEN (r.input#>>'{displayContent,sortOrder}')::integer<=10000 THEN (r.input#>>'{displayContent,sortOrder}')::integer ELSE 0 END ELSE 0 END`
const publicPhaseSQL = `CASE WHEN r.schema_version=1 AND r.terms_canonical_version=1
 AND r.input->>'templateId'='campaignCardV1'
 AND r.input#>>'{displayContent,layout}' IN ('compact','featured')
 AND r.input#>>'{materialTerms,rewardDelivery}'='display-only'
 AND r.input#>>'{cta,actionKind}' IN ('none','openTrade')
 AND r.input#>>'{materialTerms,rules,join,kind}'='walletJoinV1' AND r.input#>>'{materialTerms,rules,join,version}'='1'
 AND r.input#>>'{materialTerms,rules,reminder,kind}'='reminderIntentV1' AND r.input#>>'{materialTerms,rules,reminder,version}'='1'
 THEN CASE WHEN $1 < r.input#>>'{materialTerms,startAt}' THEN 'upcoming' WHEN $1 >= r.input#>>'{materialTerms,endAt}' THEN 'ended' ELSE 'active' END
 WHEN r.schema_version=1 AND r.terms_canonical_version=1 AND r.input->>'templateId'='legacyBaselineV1'
 AND r.input#>>'{materialTerms,legacy,startAt}' ~ '^[1-9][0-9]{3}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]{3}Z$'
 AND r.input#>>'{materialTerms,legacy,endAt}' ~ '^[1-9][0-9]{3}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]{3}Z$'
 THEN CASE WHEN r.input#>>'{materialTerms,legacy,endAt}' <= r.input#>>'{materialTerms,legacy,startAt}' COLLATE "C" THEN 'unsupported'
 WHEN $7 < r.input#>>'{materialTerms,legacy,startAt}' COLLATE "C" THEN 'upcoming' WHEN $7 >= r.input#>>'{materialTerms,legacy,endAt}' COLLATE "C" THEN 'ended' ELSE 'active' END
 ELSE 'unsupported' END`

func (s *Store) List(ctx context.Context, query application.ListQuery, capabilities v2.Capabilities) (application.ListResponse, error) {
	var result application.ListResponse
	cursor, err := application.DecodeQueryCursor(query.Cursor, "list", application.ListScope(query), query.Phase)
	if err != nil {
		return result, err
	}
	if query.Limit < 1 || query.Limit > 50 {
		return result, v2.InvalidInput("query limit")
	}
	err = s.readSnapshot(ctx, func(q *querySnapshot) error {
		return q.list(query, cursor, capabilities, &result)
	})
	if err != nil {
		return application.ListResponse{}, err
	}
	return result, nil
}

func (q *querySnapshot) list(query application.ListQuery, cursor application.QueryCursor, capabilities v2.Capabilities, result *application.ListResponse) error {
	*result = application.ListResponse{SchemaVersion: 1, ServerNow: q.timestamp(), AsOf: q.timestamp(), Items: []application.PublicCampaignView{}}
	rows, err := q.tx.Query(q.ctx, `WITH candidates AS (SELECT `+publicProjection+`,`+publicSortSQL+` AS sort_order,`+publicPhaseSQL+` AS phase `+publicFrom+`)
 SELECT * FROM candidates WHERE ($2='' OR phase=$2) AND ($3='' OR (sort_order,campaign_id)>($4,$3))
 AND (NOT $6::boolean OR (mode='enabled' AND phase<>'unsupported')) ORDER BY sort_order,campaign_id LIMIT $5`, q.now.UTC().Format("2006-01-02T15:04:05Z"), query.Phase, cursor.ID, cursor.SortOrder, query.Limit+1, query.LegacyRepresentable,
		// Legacy baselines keep millisecond windows; formatting truncates, so
		// the comparison agrees with the Go phase at the same instant.
		v2.FormatLegacyTime(q.now))
	if err != nil {
		return err
	}
	type entry struct {
		record publicRecord
		order  int64
		phase  string
	}
	entries := []entry{}
	for rows.Next() {
		var e entry
		targets := e.record.targets()
		targets = append(targets, &e.order, &e.phase)
		if err = rows.Scan(targets...); err != nil {
			rows.Close()
			return err
		}
		entries = append(entries, e)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	more := len(entries) > query.Limit
	if more {
		entries = entries[:query.Limit]
	}
	budget, emitted := queryPageBudget, 0
	for _, e := range entries {
		if emitted > 0 && len(e.record.raw)+2048 > budget {
			more = true
			break
		}
		view, err := q.publicRecordView(e.record, query.Variants, capabilities)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(view)
		if err != nil {
			return err
		}
		if len(raw) > budget {
			more = true
			break
		}
		budget -= len(raw) + 1
		emitted++
		result.Items = append(result.Items, view)
	}
	if more {
		if emitted == 0 {
			return unavailable("campaign page exceeds response budget")
		}
		last := entries[emitted-1]
		next := application.EncodeQueryCursor(application.QueryCursor{Kind: "list", Scope: application.ListScope(query), Phase: query.Phase, ID: last.record.id, SortOrder: last.order})
		result.NextCursor = &next
	}
	return nil
}
func (s *Store) Detail(ctx context.Context, id string, variants v2.Variants, capabilities v2.Capabilities) (application.DetailResponse, error) {
	var result application.DetailResponse
	err := s.readSnapshot(ctx, func(q *querySnapshot) error {
		return q.detail(id, variants, capabilities, &result)
	})
	if err != nil {
		return application.DetailResponse{}, err
	}
	return result, nil
}

func (q *querySnapshot) detail(id string, variants v2.Variants, capabilities v2.Capabilities, result *application.DetailResponse) error {
	view, err := q.publicView(id, variants, capabilities)
	*result = application.DetailResponse{SchemaVersion: 1, ServerNow: q.timestamp(), AsOf: q.timestamp(), Item: view}
	return err
}
func (s *Store) Preview(ctx context.Context, campaignID, revisionID string, capabilities v2.Capabilities) (application.PreviewResponse, error) {
	var result application.PreviewResponse
	err := s.readSnapshot(ctx, func(q *querySnapshot) error {
		read, err := q.revision(campaignID, revisionID)
		if err != nil {
			return err
		}
		view := application.PreviewView{CampaignID: campaignID, RevisionID: revisionID, ContentState: "unsupported", MaterialTermsHash: read.revision.MaterialTermsHash, ContentHash: read.revision.ContentHash}
		if read.supported {
			if read.contentAvailable(capabilities) {
				view.ContentState = "ready"
				view.Content = &read.revision.Input
			}
		}
		if err = q.tx.QueryRow(q.ctx, `SELECT EXISTS (SELECT 1 FROM campaign_publication_history WHERE campaign_id=$1 AND after_revision_id=$2)`, campaignID, revisionID).Scan(&view.IsPublished); err != nil {
			return err
		}
		result = application.PreviewResponse{SchemaVersion: 1, ServerNow: q.timestamp(), AsOf: q.timestamp(), Item: view}
		return nil
	})
	if err != nil {
		return application.PreviewResponse{}, err
	}
	return result, nil
}
