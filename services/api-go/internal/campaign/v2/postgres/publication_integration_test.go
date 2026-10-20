//go:build integration

package postgres_test

import (
	"context"
	"testing"
	"time"
)

func TestDisplayRevisionRollbackKeepsMonotonicHistory(t *testing.T) {
	pool := poolFor(t)
	s := service(pool)
	start := time.Now().Add(-time.Hour)
	end := time.Now().Add(time.Hour)
	p := publish(t, s, draft(t, s, start, end))
	execute(t, s, wallet, interaction(t, "join", p.CampaignID, 1, 0))
	display := input(start, end)
	display.DisplayContent.SortOrder = 7
	c := command(t, "revision", p.CampaignID)
	c.Revision = &display
	c.ExpectedPublication = pointer(1)
	candidate := execute(t, s, admin, c).Original
	c = command(t, "publish", p.CampaignID)
	c.ExpectedPublication = pointer(1)
	c.CandidateRevisionID = candidate.RevisionID
	if result := execute(t, s, admin, c).Original; result.PublicationVersion != 2 {
		t.Fatal("display revision not published")
	}
	c = command(t, "rollback", p.CampaignID)
	c.ExpectedPublication = pointer(2)
	c.CandidateRevisionID = p.RevisionID
	rolled := execute(t, s, admin, c).Original
	if rolled.PublicationVersion != 3 || rolled.RevisionID != p.RevisionID || rolled.InteractionVersion != 1 {
		t.Fatal("rollback must be a new publication preserving interaction")
	}
	if count(t, pool, `SELECT accepted_publication_version::int FROM campaign_interaction_states WHERE campaign_id=$1 AND wallet_address=$2`, p.CampaignID, wallet) != 1 {
		t.Fatal("display publication rewrote old acceptance")
	}
	if count(t, pool, `SELECT count(*) FROM campaign_publication_history WHERE campaign_id=$1`, p.CampaignID) != 3 {
		t.Fatal("rollback erased publication history")
	}
	if count(t, pool, `SELECT count(*) FROM durable_outbox WHERE payload->>'campaignId'=$1 AND (payload->>'aggregateId'<>aggregate_id OR payload->>'aggregateVersion'<>aggregate_version::text)`, p.CampaignID) != 0 {
		t.Fatal("event envelope disagrees with durable stream")
	}
}

func TestCrossCampaignCandidateCannotPublish(t *testing.T) {
	pool := poolFor(t)
	s := service(pool)
	start := time.Now().Add(time.Hour)
	end := start.Add(time.Hour)
	a := draft(t, s, start, end)
	b := draft(t, s, start, end)
	c := command(t, "publish", a.CampaignID)
	c.CandidateRevisionID = b.RevisionID
	c.ExpectedPublication = pointer(0)
	_, err := s.Execute(context.Background(), admin, c)
	expectCode(t, err, "CAMPAIGN_NOT_FOUND")
	if count(t, pool, `SELECT count(*) FROM campaign_publication_history WHERE campaign_id=$1`, a.CampaignID) != 0 {
		t.Fatal("cross-campaign publication committed")
	}
}

func TestSlugConflictRollsBackLegacyIdentityAndCommand(t *testing.T) {
	pool := poolFor(t)
	s := service(pool)
	revision := input(time.Now().Add(time.Hour), time.Now().Add(2*time.Hour))
	c := command(t, "create", "")
	c.Revision = &revision
	c.Slug = "test-" + c.ClientCommandID
	execute(t, s, admin, c)
	c.ClientCommandID = commandID(t)
	before := count(t, pool, `SELECT count(*) FROM launchpad_campaigns`)
	_, err := s.Execute(context.Background(), admin, c)
	expectCode(t, err, "CAMPAIGN_SLUG_CONFLICT")
	if count(t, pool, `SELECT count(*) FROM launchpad_campaigns`) != before {
		t.Fatal("slug failure leaked legacy identity")
	}
	if count(t, pool, `SELECT count(*) FROM campaign_commands WHERE client_command_id=$1`, c.ClientCommandID) != 0 {
		t.Fatal("slug failure recorded success")
	}
}

func TestUnknownLegacyHistoryCannotBeMistakenForNoAcceptance(t *testing.T) {
	pool := poolFor(t)
	s := service(pool)
	original := draft(t, s, time.Now().Add(time.Hour), time.Now().Add(2*time.Hour))
	id, revisionID := commandID(t), commandID(t)
	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	guardExec(t, tx, `INSERT INTO launchpad_campaigns(id,title,start_date,end_date,updated_at)
		SELECT $1,title,start_date,end_date,updated_at FROM launchpad_campaigns WHERE id=$2`, id, original.CampaignID)
	guardExec(t, tx, `INSERT INTO campaign_definitions(id,source,legacy_snapshot,created_by)
		VALUES ($1,'legacy-unversioned','{}',$2)`, id, admin)
	guardExec(t, tx, `INSERT INTO campaign_revisions(id,campaign_id,schema_version,terms_canonical_version,input,material_terms_hash,content_hash,created_by)
		SELECT $1,$2,schema_version,terms_canonical_version,input,material_terms_hash,content_hash,created_by
		FROM campaign_revisions WHERE id=$3`, revisionID, id, original.RevisionID)
	guardExec(t, tx, `INSERT INTO campaign_publications(campaign_id,updated_by) VALUES ($1,$2)`, id, admin)
	if err = tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	c := command(t, "publish", id)
	c.CandidateRevisionID, c.ExpectedPublication = revisionID, pointer(0)
	_, err = s.Execute(context.Background(), admin, c)
	expectCode(t, err, "CAMPAIGN_LEGACY_TERMS_UNVERIFIED")
	if count(t, pool, `SELECT count(*) FROM campaign_interaction_history WHERE campaign_id=$1`, id) != 0 ||
		count(t, pool, `SELECT count(*) FROM campaign_publication_history WHERE campaign_id=$1`, id) != 0 ||
		count(t, pool, `SELECT count(*) FROM durable_outbox WHERE payload->>'campaignId'=$1`, id) != 0 {
		t.Fatal("unknown legacy history invented acceptance or publication evidence")
	}
}

// launchpad_campaigns stores UTC wall-clock values without a time zone, and pgx
// writes a time.Time's wall clock. The product relies on transaction.Now()
// returning UTC; this pins that, so the compatibility row keeps the definition
// instant on any host time zone (run under TZ=Asia/Shanghai and TZ=UTC).
func TestCreateWritesLegacyTimestampsAsUTC(t *testing.T) {
	pool := poolFor(t)
	s := service(pool)
	created := draft(t, s, time.Now().Add(time.Hour), time.Now().Add(2*time.Hour))
	if count(t, pool, `SELECT count(*) FROM launchpad_campaigns l JOIN campaign_definitions d ON d.id=l.id
		WHERE l.id=$1 AND abs(extract(epoch FROM l.updated_at - (d.created_at AT TIME ZONE 'UTC'))) < 0.001`, created.CampaignID) != 1 {
		t.Fatal("legacy updated_at must be the definition instant in UTC wall-clock time")
	}
}
