//go:build integration

package postgres_test

import (
	"context"
	"testing"
	"time"

	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/application"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/postgres"
)

func adminView(t *testing.T, store *postgres.Store, id string) application.AdminCampaignView {
	t.Helper()
	value, err := store.AdminCampaign(context.Background(), id, caps())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = time.Parse(time.RFC3339Nano, value.ServerNow); err != nil || value.ServerNow != value.AsOf {
		t.Fatal("administrator view must carry one database snapshot time")
	}
	return value.Item
}

// materialCandidate creates a revision whose terms differ from the active one,
// then reports whether the locked writer accepted it. The administrator view's
// prediction is checked against this real outcome rather than a second rule.
func materialCandidate(t *testing.T, s *application.Service, id string, expected uint64, start, end time.Time) error {
	t.Helper()
	changed := input(start, end)
	changed.MaterialTerms.Copy.En.Rules = "Changed eligibility " + commandID(t)
	c := command(t, "revision", id)
	c.Revision, c.ExpectedPublication = &changed, pointer(expected)
	candidate := execute(t, s, admin, c).Original
	c = command(t, "publish", id)
	c.CandidateRevisionID, c.ExpectedPublication = candidate.RevisionID, pointer(expected)
	_, err := s.Execute(context.Background(), admin, c)
	return err
}

func TestAdminViewDraftPublicationAndLockAgreeWithWriter(t *testing.T) {
	pool := poolFor(t)
	s, store := service(pool), postgres.New(pool)
	start, end := time.Now().Add(time.Hour), time.Now().Add(2*time.Hour)
	created := draft(t, s, start, end)
	view := adminView(t, store, created.CampaignID)
	if view.PublicationVersion != "0" || view.ActiveRevisionID != nil || view.EffectivePhase != "unpublished" || view.MaterialTermsLock != v2.TermsOpen || view.Source != "created" {
		t.Fatalf("unpublished draft view invalid: %+v", view)
	}
	if len(view.Revisions) != 1 || view.Revisions[0].ID != created.RevisionID || view.Revisions[0].Published || view.Revisions[0].Active || view.Revisions[0].ContentState != "ready" {
		t.Fatal("draft revision summary invalid")
	}

	published := publish(t, s, created)
	view = adminView(t, store, created.CampaignID)
	if view.PublicationVersion != "1" || view.ActiveRevisionID == nil || *view.ActiveRevisionID != created.RevisionID || view.EffectivePhase != "upcoming" || view.MaterialTermsLock != v2.TermsOpen {
		t.Fatalf("published upcoming view invalid: %+v", view)
	}
	// Open terms: a material candidate must actually publish.
	if err := materialCandidate(t, s, created.CampaignID, 1, start, end); err != nil {
		t.Fatalf("open lock prediction disagreed with writer: %v", err)
	}
	view = adminView(t, store, created.CampaignID)
	if len(view.Revisions) != 2 || !view.Revisions[0].Active || !view.Revisions[0].Published || view.Revisions[1].Active || !view.Revisions[1].Published {
		t.Fatal("revision summaries must be newest first with published/active facts")
	}

	// A cancelled acceptance still binds, so the lock remains after removal even
	// though the campaign has not started.
	execute(t, s, wallet, interaction(t, "set-reminder", published.CampaignID, 2, 0))
	execute(t, s, wallet, interaction(t, "remove-reminder", published.CampaignID, 0, 1))
	view = adminView(t, store, created.CampaignID)
	if !view.HasAcceptedHistory || view.MaterialTermsLock != v2.TermsLocked || view.InteractionVersion != "2" {
		t.Fatalf("accepted history must lock terms: %+v", view)
	}
	expectCode(t, materialCandidate(t, s, created.CampaignID, 2, start, end), "CAMPAIGN_TERMS_LOCKED")
}

func TestAdminViewStartedCampaignIsLockedWithoutParticipants(t *testing.T) {
	pool := poolFor(t)
	s, store := service(pool), postgres.New(pool)
	start, end := time.Now().Add(-time.Minute), time.Now().Add(time.Hour)
	p := publish(t, s, draft(t, s, start, end))
	view := adminView(t, store, p.CampaignID)
	if view.EffectivePhase != "active" || view.HasAcceptedHistory || view.MaterialTermsLock != v2.TermsLocked {
		t.Fatalf("started campaign must be locked: %+v", view)
	}
	expectCode(t, materialCandidate(t, s, p.CampaignID, 1, start, end), "CAMPAIGN_TERMS_LOCKED")
}

func TestAdminViewArchivedAndRollbackFacts(t *testing.T) {
	pool := poolFor(t)
	s, store := service(pool), postgres.New(pool)
	p := publish(t, s, draft(t, s, time.Now().Add(time.Hour), time.Now().Add(2*time.Hour)))
	c := command(t, "archive", p.CampaignID)
	c.ExpectedPublication = pointer(1)
	execute(t, s, admin, c)
	view := adminView(t, store, p.CampaignID)
	if view.Mode != "archived" || view.PublicationVersion != "2" || view.ActiveRevisionID == nil {
		t.Fatalf("archived view must keep the last active revision and version: %+v", view)
	}
	c = command(t, "rollback", p.CampaignID)
	c.ExpectedPublication, c.CandidateRevisionID = pointer(2), p.RevisionID
	_, err := s.Execute(context.Background(), admin, c)
	expectCode(t, err, "CAMPAIGN_ARCHIVED")
	if adminView(t, store, p.CampaignID).PublicationVersion != "2" {
		t.Fatal("rejected rollback changed the archived publication")
	}
}

func TestAdminViewLegacyUnversionedBlocksPublication(t *testing.T) {
	pool := poolFor(t)
	s, store := service(pool), postgres.New(pool)
	original := draft(t, s, time.Now().Add(time.Hour), time.Now().Add(2*time.Hour))
	id, revisionID := commandID(t), commandID(t)
	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	guardExec(t, tx, `INSERT INTO launchpad_campaigns(id,title,start_date,end_date,updated_at)
		SELECT $1,title,start_date,end_date,updated_at FROM launchpad_campaigns WHERE id=$2`, id, original.CampaignID)
	guardExec(t, tx, `INSERT INTO campaign_definitions(id,source,legacy_snapshot,created_by) VALUES ($1,'legacy-unversioned','{}',$2)`, id, admin)
	guardExec(t, tx, `INSERT INTO campaign_revisions(id,campaign_id,schema_version,terms_canonical_version,input,material_terms_hash,content_hash,created_by)
		SELECT $1,$2,schema_version,terms_canonical_version,input,material_terms_hash,content_hash,created_by FROM campaign_revisions WHERE id=$3`, revisionID, id, original.RevisionID)
	guardExec(t, tx, `INSERT INTO campaign_publications(campaign_id,updated_by) VALUES ($1,$2)`, id, admin)
	if err = tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	view := adminView(t, store, id)
	if view.Source != "legacy-unversioned" || !view.HasAcceptedHistory || view.MaterialTermsLock != v2.TermsLegacyUnverified {
		t.Fatalf("legacy view must report unverified terms: %+v", view)
	}
	c := command(t, "publish", id)
	c.CandidateRevisionID, c.ExpectedPublication = revisionID, pointer(0)
	_, err = s.Execute(context.Background(), admin, c)
	expectCode(t, err, "CAMPAIGN_LEGACY_TERMS_UNVERIFIED")
}

func TestAdminViewRevisionListIsBoundedAndMissingIsNotFound(t *testing.T) {
	pool := poolFor(t)
	s, store := service(pool), postgres.New(pool)
	start, end := time.Now().Add(time.Hour), time.Now().Add(2*time.Hour)
	created := draft(t, s, start, end)
	for i := 0; i < 50; i++ {
		next := input(start, end)
		next.DisplayContent.SortOrder = int64(i + 1)
		c := command(t, "revision", created.CampaignID)
		c.Revision, c.ExpectedPublication = &next, pointer(0)
		execute(t, s, admin, c)
	}
	view := adminView(t, store, created.CampaignID)
	if len(view.Revisions) != 50 || !view.RevisionsTruncated || view.Revisions[49].ID == created.RevisionID {
		t.Fatal("51 revisions must return the newest 50 and report truncation")
	}
	_, err := store.AdminCampaign(context.Background(), "campaign-absent-"+commandID(t)[:8], caps())
	expectCode(t, err, "CAMPAIGN_NOT_FOUND")
}
