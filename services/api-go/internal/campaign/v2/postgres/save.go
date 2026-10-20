package postgres

import (
	"encoding/json"
	"time"

	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/application"
)

func (t *transaction) Save(record application.Record, mutation application.Mutation) error {
	if mutation.Definition != nil {
		if t.campaignID != "" || mutation.Revision == nil || record.Key.Operation != "create" {
			return v2.InvalidInput("invalid create mutation")
		}
		if err := t.createDefinition(record, mutation); err != nil {
			return err
		}
		t.campaignID = mutation.Definition.ID
	}
	if t.campaignID == "" || mutation.BasePublication.CampaignID != t.campaignID || record.Result.CampaignID != t.campaignID {
		return v2.InvalidInput("mutation campaign mismatch")
	}
	if mutation.Revision != nil {
		if err := t.saveRevision(record, *mutation.Revision); err != nil {
			return err
		}
	}
	if mutation.Definition != nil {
		_, err := t.tx.Exec(t.ctx, `INSERT INTO campaign_publications(campaign_id,updated_by,updated_at) VALUES ($1,$2,$3)`, t.campaignID, record.Key.Actor, record.At)
		if err != nil {
			return err
		}
	}
	result, err := json.Marshal(record.Result)
	if err != nil {
		return err
	}
	_, err = t.tx.Exec(t.ctx, `INSERT INTO campaign_commands(id,actor,operation,target_id,client_command_id,payload_hash,
		expected_publication_version,expected_state_version,reason,request_id,result,occurred_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, record.ID, record.Key.Actor, record.Key.Operation, record.Key.TargetID, record.Key.ClientID, record.PayloadHash,
		integer(record.ExpectedPublication), integer(record.ExpectedState), record.Reason, record.RequestID, result, record.At)
	if err != nil {
		return err
	}
	if mutation.Publication != nil {
		if err = t.savePublication(record, *mutation.Publication); err != nil {
			return err
		}
	}
	if mutation.Interaction != nil && mutation.Interaction.Changed {
		if err = t.saveInteraction(record, mutation); err != nil {
			return err
		}
	}
	auditID, err := application.NewID()
	if err != nil {
		return err
	}
	_, err = t.tx.Exec(t.ctx, `INSERT INTO campaign_audit_events(id,campaign_id,command_id,actor,operation,changes,occurred_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`, auditID, t.campaignID, record.ID, record.Key.Actor, record.Key.Operation, result, record.At)
	if err != nil {
		return err
	}
	return t.saveEvents(record, mutation)
}

func (t *transaction) createDefinition(record application.Record, mutation application.Mutation) error {
	terms := mutation.Revision.Input.MaterialTerms
	start, err := time.Parse(time.RFC3339, terms.StartAt)
	if err != nil {
		return err
	}
	end, err := time.Parse(time.RFC3339, terms.EndAt)
	if err != nil {
		return err
	}
	// The shared legacy primary key remains stable; these compatibility columns
	// are not a public read model. HTTP mounting requires the read/write cutover.
	_, err = t.tx.Exec(t.ctx, `INSERT INTO launchpad_campaigns(id,title,description,start_date,end_date,status,reward,updated_at)
		VALUES ($1,$2,$3,$4,$5,'unpublished',$6,$7)`, mutation.Definition.ID, terms.Copy.En.Title, terms.Copy.En.Description, start, end, terms.Copy.En.RewardDescription, record.At)
	if err != nil {
		return err
	}
	_, err = t.tx.Exec(t.ctx, `INSERT INTO campaign_definitions(id,slug,created_by,created_at) VALUES ($1,$2,$3,$4)`, mutation.Definition.ID, nullable(mutation.Definition.Slug), record.Key.Actor, record.At)
	return err
}

func (t *transaction) saveRevision(record application.Record, revision v2.Revision) error {
	if revision.CampaignID != t.campaignID {
		return v2.InvalidInput("revision campaign mismatch")
	}
	if err := v2.ValidateStoredRevisionIntegrity(revision); err != nil {
		return err
	}
	raw, err := json.Marshal(revision.Input)
	if err != nil {
		return err
	}
	_, err = t.tx.Exec(t.ctx, `INSERT INTO campaign_revisions(id,campaign_id,schema_version,terms_canonical_version,input,material_terms_hash,content_hash,created_by,created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, revision.ID, revision.CampaignID, revision.Input.SchemaVersion, revision.Input.TermsCanonicalVersion, raw, revision.MaterialTermsHash, revision.ContentHash, record.Key.Actor, record.At)
	return err
}

func (t *transaction) savePublication(record application.Record, change application.PublicationChange) error {
	if change.Before.CampaignID != t.campaignID || change.After.CampaignID != t.campaignID {
		return v2.InvalidInput("publication campaign mismatch")
	}
	tag, err := t.tx.Exec(t.ctx, `UPDATE campaign_publications SET publication_version=$2,active_revision_id=$3,mode=$4,updated_by=$5,updated_at=$6
		WHERE campaign_id=$1 AND publication_version=$7`, t.campaignID, int64(change.After.Version), revisionID(change.After.Active), change.After.Mode, record.Key.Actor, record.At, int64(change.Before.Version))
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return unavailable("publication lock invariant")
	}
	_, err = t.tx.Exec(t.ctx, `INSERT INTO campaign_publication_history(campaign_id,publication_version,command_id,actor,before_mode,after_mode,before_revision_id,after_revision_id,occurred_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, t.campaignID, int64(change.After.Version), record.ID, record.Key.Actor, change.Before.Mode, change.After.Mode, revisionID(change.Before.Active), revisionID(change.After.Active), record.At)
	return err
}

func integer(value *uint64) any {
	if value == nil {
		return nil
	}
	// pgx checks uint64 against PostgreSQL bigint; narrowing first would wrap.
	return *value
}
func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}
func revisionID(value *v2.Revision) any {
	if value == nil {
		return nil
	}
	return value.ID
}
