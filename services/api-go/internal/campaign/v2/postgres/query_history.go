package postgres

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/application"
)

type historyRow struct {
	state                           personalRow
	campaignID, operation, clientID string
	before                          *string
	interactionVersion              int64
}

// History lists the wallet's own commands. A legacy import is the
// administrator's command about the wallet: the base representation leaves its
// rows out (the imported state is visible through Me), and the negotiated
// representation lists them as imported entries, never as the wallet's own
// command (docs/campaign-legacy-presentation-protocol-2026-10-12.md §4). Both
// page one stream ordered by the per-wallet unique history key.
func (s *Store) History(ctx context.Context, actor string, query application.HistoryQuery) (application.HistoryResponse, error) {
	var result application.HistoryResponse
	scope := application.HistoryScope(actor, query.Variants)
	cursor, err := application.DecodeQueryCursor(query.Cursor, "history", scope, "")
	if err != nil {
		return result, err
	}
	if query.Limit < 1 || query.Limit > 50 {
		return result, unavailable("query limit invalid")
	}
	err = s.readSnapshot(ctx, func(q *querySnapshot) error {
		result = application.HistoryResponse{SchemaVersion: 1, ServerNow: q.timestamp(), AsOf: q.timestamp(), Items: []application.HistoryView{}}
		var cursorAt any
		if cursor.At != "" {
			at, err := time.Parse(time.RFC3339Nano, cursor.At)
			if err != nil {
				return err
			}
			cursorAt = at
		}
		rows, err := q.tx.Query(q.ctx, `SELECT h.campaign_id,h.kind,h.after_status,h.row_version,h.acceptance_provenance,h.accepted_revision_id::text,h.accepted_publication_version,h.accepted_terms_hash,h.accepted_at,h.occurred_at,h.before_status,h.interaction_version,c.operation,c.client_command_id::text
   FROM campaign_interaction_history h JOIN campaign_commands c ON c.id=h.command_id
   WHERE h.wallet_address=$1 AND ($7::boolean OR c.operation<>'legacy-import') AND ($2::timestamptz IS NULL OR h.occurred_at<$2 OR (h.occurred_at=$2 AND (h.campaign_id,h.kind,h.row_version)>($3,$4,$5)))
   ORDER BY h.occurred_at DESC,h.campaign_id,h.kind,h.row_version LIMIT $6`, actor, cursorAt, cursor.ID, cursor.StateKind, cursor.RowVersion, query.Limit+1, query.Variants.LegacyImportHistory)
		if err != nil {
			return err
		}
		entries := []historyRow{}
		for rows.Next() {
			var row historyRow
			r := &row.state
			if err = rows.Scan(&row.campaignID, &r.kind, &r.status, &r.version, &r.provenance, &r.revisionID, &r.publicationVersion, &r.termsHash, &r.acceptedAt, &r.changedAt, &row.before, &row.interactionVersion, &row.operation, &row.clientID); err != nil {
				rows.Close()
				return err
			}
			entries = append(entries, row)
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
		for _, row := range entries {
			view, err := q.historyView(row, query.Variants)
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
				return unavailable("campaign history exceeds response budget")
			}
			row := entries[emitted-1]
			next := application.EncodeQueryCursor(application.QueryCursor{Kind: "history", Scope: scope, ID: row.campaignID, At: row.state.changedAt.UTC().Format(time.RFC3339Nano), StateKind: row.state.kind, RowVersion: row.state.version})
			result.NextCursor = &next
		}
		return nil
	})
	if err != nil {
		return application.HistoryResponse{}, err
	}
	return result, nil
}
func (q *querySnapshot) historyView(row historyRow, variants v2.Variants) (application.HistoryView, error) {
	state, err := row.state.view()
	if err != nil {
		return application.HistoryView{}, err
	}
	if row.interactionVersion < 1 {
		return application.HistoryView{}, unavailable("stored history version invalid")
	}
	occurredAt := row.state.changedAt.UTC().Format(time.RFC3339Nano)
	if row.operation == "legacy-import" {
		return importedHistoryView(row, state, occurredAt, variants)
	}
	result := application.HistoryView{CampaignID: row.campaignID, Kind: row.state.kind, State: state, BeforeStatus: row.before, Operation: stringPointer(row.operation), ClientCommandID: stringPointer(row.clientID), InteractionVersion: strconv.FormatInt(row.interactionVersion, 10), OccurredAt: occurredAt, SnapshotState: "unsupported"}
	if variants.LegacyImportHistory {
		result.Entry = stringPointer(v2.HistoryEntryCommand)
	}
	if state.State == "unknown" {
		return result, nil
	}
	switch row.state.provenance {
	case "neverAccepted":
		result.SnapshotState = "none"
	case "legacyUnknown":
		result.SnapshotState = "legacyUnknown"
	case "verified":
		read, err := q.revision(row.campaignID, *row.state.revisionID)
		if err != nil {
			return result, unavailable("accepted revision unavailable")
		}
		if read.revision.MaterialTermsHash != *row.state.termsHash {
			return result, unavailable("accepted terms hash mismatch")
		}
		if read.supported {
			result.SnapshotState = "ready"
			result.AcceptedTerms = &read.revision.Input.MaterialTerms
		}
	}
	return result, nil
}

// importedHistoryView presents an imported state as what it is: the import
// time, the earliest time the legacy tables recorded the interaction, and
// unknown accepted terms. The administrator's command, its key and the
// archived source rows stay private.
func importedHistoryView(row historyRow, state application.PersonalStateView, occurredAt string, variants v2.Variants) (application.HistoryView, error) {
	if !variants.LegacyImportHistory {
		return application.HistoryView{}, unavailable("imported history requires the negotiated representation")
	}
	if row.before != nil || row.state.provenance != "legacyUnknown" || row.state.version != 1 || state.State != "known" {
		return application.HistoryView{}, unavailable("stored imported history invalid")
	}
	return application.HistoryView{Entry: stringPointer(v2.HistoryEntryLegacyImport), CampaignID: row.campaignID, Kind: row.state.kind, State: state,
		InteractionVersion: strconv.FormatInt(row.interactionVersion, 10), OccurredAt: occurredAt, SnapshotState: "legacyUnknown",
		LegacyImport: &v2.QueryHistoryImportView{ImportedAt: occurredAt, LegacyRecordedAt: state.AcceptedAt}}, nil
}
