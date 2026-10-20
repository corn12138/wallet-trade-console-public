package postgres

import "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/application"

func (t *transaction) saveInteraction(record application.Record, mutation application.Mutation) error {
	change := mutation.Interaction
	after := change.After
	accepted := after.Acceptance
	args := []any{t.campaignID, record.Key.Actor, after.Kind, after.Status, int64(after.Version), accepted.Provenance, accepted.RevisionID, integer(accepted.PublicationVersion), accepted.TermsHash, accepted.AcceptedAt, record.At}
	// INSERT ... ON CONFLICT would run the initial-version trigger even on an
	// update. The aggregate lock already protects this existence decision.
	if change.Before == nil {
		_, err := t.tx.Exec(t.ctx, `INSERT INTO campaign_interaction_states(campaign_id,wallet_address,kind,status,row_version,acceptance_provenance,
			accepted_revision_id,accepted_publication_version,accepted_terms_hash,accepted_at,changed_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, args...)
		if err != nil {
			return err
		}
	} else {
		args = append(args, int64(change.Before.Version))
		tag, err := t.tx.Exec(t.ctx, `UPDATE campaign_interaction_states SET status=$4,row_version=$5,acceptance_provenance=$6,
			accepted_revision_id=$7,accepted_publication_version=$8,accepted_terms_hash=$9,accepted_at=$10,changed_at=$11
			WHERE campaign_id=$1 AND wallet_address=$2 AND kind=$3 AND row_version=$12`, args...)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return unavailable("personal state lock invariant")
		}
	}
	var before any
	if change.Before != nil {
		before = change.Before.Status
	}
	_, err := t.tx.Exec(t.ctx, `INSERT INTO campaign_interaction_history(campaign_id,wallet_address,kind,row_version,interaction_version,command_id,before_status,after_status,
		acceptance_provenance,accepted_revision_id,accepted_publication_version,accepted_terms_hash,accepted_at,occurred_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`, t.campaignID, record.Key.Actor, after.Kind, int64(after.Version), int64(record.Result.InteractionVersion), record.ID, before, after.Status,
		accepted.Provenance, accepted.RevisionID, integer(accepted.PublicationVersion), accepted.TermsHash, accepted.AcceptedAt, record.At)
	if err != nil {
		return err
	}
	tag, err := t.tx.Exec(t.ctx, `UPDATE campaign_publications SET interaction_version=$2,updated_by=$3,updated_at=$4
		WHERE campaign_id=$1 AND interaction_version=$5`, t.campaignID, int64(record.Result.InteractionVersion), record.Key.Actor, record.At, int64(mutation.BasePublication.InteractionVersion))
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return unavailable("interaction lock invariant")
	}
	return nil
}
