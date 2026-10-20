package postgres

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/application"
	"github.com/jackc/pgx/v5"
)

type personalRow struct {
	kind, status, provenance string
	version                  int64
	revisionID               *string
	publicationVersion       *int64
	termsHash                *string
	acceptedAt               *time.Time
	changedAt                time.Time
}

func stringPointer(value string) *string { return &value }
func absentState() application.PersonalStateView {
	return application.PersonalStateView{State: "absent", StateVersion: stringPointer("0")}
}
func (row personalRow) view() (application.PersonalStateView, error) {
	value := application.PersonalStateView{State: "unknown"}
	positive, negative := "joined", "left"
	if row.kind == "reminder" {
		positive, negative = "set", "removed"
	} else if row.kind != "join" {
		return value, nil
	}
	if row.status != positive && row.status != negative {
		return value, nil
	}
	if row.provenance != "verified" && row.provenance != "neverAccepted" && row.provenance != "legacyUnknown" {
		return value, nil
	}
	if row.version < 1 || row.changedAt.IsZero() {
		return value, unavailable("stored personal version invalid")
	}
	empty := row.revisionID == nil && row.publicationVersion == nil && row.termsHash == nil
	switch row.provenance {
	case "verified":
		if row.revisionID == nil || !readUUIDPattern.MatchString(*row.revisionID) || row.publicationVersion == nil || *row.publicationVersion < 1 || row.termsHash == nil || !readHashPattern.MatchString(*row.termsHash) || row.acceptedAt == nil {
			return value, unavailable("stored acceptance invalid")
		}
	case "neverAccepted":
		if !empty || row.acceptedAt != nil || row.status == positive {
			return value, unavailable("stored tombstone invalid")
		}
	case "legacyUnknown":
		if !empty {
			return value, unavailable("stored legacy provenance invalid")
		}
	}
	value = application.PersonalStateView{State: "known", StateVersion: stringPointer(strconv.FormatInt(row.version, 10)), Status: &row.status, AcceptanceProvenance: &row.provenance, AcceptedRevisionID: row.revisionID, AcceptedTermsHash: row.termsHash, ChangedAt: stringPointer(row.changedAt.UTC().Format(time.RFC3339Nano))}
	if row.publicationVersion != nil {
		value.AcceptedPublicationVersion = stringPointer(strconv.FormatInt(*row.publicationVersion, 10))
	}
	if row.acceptedAt != nil {
		value.AcceptedAt = stringPointer(row.acceptedAt.UTC().Format(time.RFC3339Nano))
	}
	return value, nil
}
func (q *querySnapshot) personal(campaignID, actor, kind string) (*personalRow, error) {
	row := &personalRow{}
	err := q.tx.QueryRow(q.ctx, `SELECT kind,status,row_version,acceptance_provenance,accepted_revision_id::text,accepted_publication_version,accepted_terms_hash,accepted_at,changed_at FROM campaign_interaction_states WHERE campaign_id=$1 AND wallet_address=$2 AND kind=$3`, campaignID, actor, kind).Scan(&row.kind, &row.status, &row.version, &row.provenance, &row.revisionID, &row.publicationVersion, &row.termsHash, &row.acceptedAt, &row.changedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return row, nil
}
func (s *Store) Me(ctx context.Context, actor string, ids []string) (application.MeResponse, error) {
	var result application.MeResponse
	err := s.readSnapshot(ctx, func(q *querySnapshot) error {
		return q.me(actor, ids, &result)
	})
	if err != nil {
		return application.MeResponse{}, err
	}
	return result, nil
}

func (q *querySnapshot) me(actor string, ids []string, result *application.MeResponse) error {
	*result = application.MeResponse{SchemaVersion: 1, ServerNow: q.timestamp(), AsOf: q.timestamp(), Items: []application.MyCampaignView{}}
	for _, id := range ids {
		var version, interactionVersion int64
		var active *string
		var source string
		var activated bool
		err := q.tx.QueryRow(q.ctx, `SELECT p.publication_version,p.interaction_version,p.active_revision_id::text,d.source,`+legacyActivatedSQL+`
			FROM campaign_publications p JOIN campaign_definitions d ON d.id=p.campaign_id WHERE p.campaign_id=$1`, id).Scan(&version, &interactionVersion, &active, &source, &activated)
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound()
		}
		if err != nil {
			return err
		}
		if version < 0 || interactionVersion < 0 {
			return unavailable("stored publication version invalid")
		}
		join, err := q.personal(id, actor, "join")
		if err != nil {
			return err
		}
		reminder, err := q.personal(id, actor, "reminder")
		if err != nil {
			return err
		}
		if active == nil && join == nil && reminder == nil {
			return notFound()
		}
		view := application.MyCampaignView{ID: id, PublicationVersion: strconv.FormatInt(version, 10), InteractionVersion: strconv.FormatInt(interactionVersion, 10), Participation: absentState(), Reminder: absentState()}
		// Missing new-table rows cannot prove absence until a legacy import has
		// established complete ownership/state provenance.
		if source == "legacy-unversioned" && !activated {
			view.Participation = application.PersonalStateView{State: "unknown"}
			view.Reminder = application.PersonalStateView{State: "unknown"}
		}
		if join != nil {
			view.Participation, err = join.view()
			if err != nil {
				return err
			}
		}
		if reminder != nil {
			view.Reminder, err = reminder.view()
			if err != nil {
				return err
			}
		}
		result.Items = append(result.Items, view)
	}
	return nil
}
