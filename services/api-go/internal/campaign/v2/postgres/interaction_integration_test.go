//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/application"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/postgres"
)

func TestPersonalCASReplayAndCancellationPreserveAcceptance(t *testing.T) {
	pool := poolFor(t)
	s := service(pool)
	p := publish(t, s, draft(t, s, time.Now().Add(-time.Minute), time.Now().Add(time.Hour)))
	commands := []application.Command{interaction(t, "join", p.CampaignID, 1, 0), interaction(t, "join", p.CampaignID, 1, 0)}
	var outcomes [2]application.Outcome
	var errs [2]error
	var group sync.WaitGroup
	for i := range commands {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			outcomes[i], errs[i] = s.Execute(context.Background(), wallet, commands[i])
		}(i)
	}
	group.Wait()
	winner := -1
	for i, err := range errs {
		if err == nil {
			if winner != -1 {
				t.Fatal("both stale versions joined")
			}
			winner = i
		} else {
			expectCode(t, err, "CAMPAIGN_STATE_CONFLICT")
		}
	}
	if winner < 0 {
		t.Fatal("neither join committed")
	}
	accepted := outcomes[winner].Original.State
	leave := interaction(t, "leave", p.CampaignID, 0, 1)
	left := execute(t, s, wallet, leave)
	if left.Original.State.StateVersion != "2" || left.Original.State.AcceptanceProvenance != "verified" || *left.Original.State.AcceptedTermsHash != *accepted.AcceptedTermsHash {
		t.Fatal("cancellation lost acceptance")
	}
	replay := execute(t, s, wallet, commands[winner])
	if !replay.Replayed || replay.Original.State.Status != "joined" || replay.Current.State.Status != "left" {
		t.Fatal("replay resurrected cancelled state")
	}
	_, err := s.Execute(context.Background(), wallet, interaction(t, "join", p.CampaignID, 1, 0))
	expectCode(t, err, "CAMPAIGN_STATE_CONFLICT")
	noOp := execute(t, s, wallet, interaction(t, "leave", p.CampaignID, 0, 2))
	if noOp.Original.Changed || noOp.Original.State.StateVersion != "2" {
		t.Fatal("target-state no-op advanced version")
	}
	if count(t, pool, `SELECT count(*) FROM campaign_interaction_history WHERE campaign_id=$1`, p.CampaignID) != 2 {
		t.Fatal("replay or no-op duplicated history")
	}
	if count(t, pool, `SELECT count(*) FROM durable_outbox WHERE payload->>'campaignId'=$1`, p.CampaignID) != 5 {
		t.Fatal("expected publication + two public/private interaction pairs")
	}
	if count(t, pool, `SELECT count(*) FROM durable_outbox WHERE payload->>'campaignId'=$1 AND owner_scope IS NULL AND (payload::text LIKE '%'||$2||'%' OR aggregate_id LIKE '%'||$2||'%' OR payload ? 'state')`, p.CampaignID, wallet) != 0 {
		t.Fatal("public outbox leaked owner state")
	}
	if count(t, pool, `SELECT count(*) FROM outbox_streams WHERE stream_key::jsonb->>1=$1`, p.CampaignID) != 3 {
		t.Fatal("publication/public interaction/private wallet must use separate streams")
	}
}

func TestFirstCancellationBlocksLateJoinAndKeepsKindsIndependent(t *testing.T) {
	pool := poolFor(t)
	s := service(pool)
	p := publish(t, s, draft(t, s, time.Now().Add(-time.Minute), time.Now().Add(time.Hour)))
	left := execute(t, s, wallet, interaction(t, "leave", p.CampaignID, 0, 0))
	if left.Original.State.AcceptanceProvenance != "neverAccepted" || left.Original.State.StateVersion != "1" {
		t.Fatal("first cancellation needs tombstone")
	}
	_, err := s.Execute(context.Background(), wallet, interaction(t, "join", p.CampaignID, 1, 0))
	expectCode(t, err, "CAMPAIGN_STATE_CONFLICT")
	reminder := execute(t, s, wallet, interaction(t, "remove-reminder", p.CampaignID, 0, 0))
	if reminder.Original.State.StateVersion != "1" || reminder.Original.InteractionVersion != 2 {
		t.Fatal("kind versions merged or global interaction not advanced")
	}
	joined := execute(t, s, wallet, interaction(t, "join", p.CampaignID, 1, 1))
	if joined.Original.State.StateVersion != "2" {
		t.Fatal("explicit fresh rejoin rejected")
	}
}

func TestWithdrawnCapabilityCannotPreventOwnerCancellation(t *testing.T) {
	pool := poolFor(t)
	s := service(pool)
	p := publish(t, s, draft(t, s, time.Now().Add(-time.Minute), time.Now().Add(time.Hour)))
	execute(t, s, wallet, interaction(t, "join", p.CampaignID, 1, 0))
	withdrawn := application.NewService(postgres.New(pool), allowed{}, func() v2.Capabilities { return v2.Capabilities{} })
	_, err := withdrawn.Execute(context.Background(), admin, interaction(t, "join", p.CampaignID, 1, 0))
	expectCode(t, err, "CAMPAIGN_UNSUPPORTED")
	left := execute(t, withdrawn, wallet, interaction(t, "leave", p.CampaignID, 0, 1))
	if left.Original.State.Status != "left" {
		t.Fatal("withdrawn capability trapped participation")
	}
	pause := command(t, "pause", p.CampaignID)
	pause.ExpectedPublication = pointer(1)
	execute(t, withdrawn, admin, pause)
	resume := command(t, "resume", p.CampaignID)
	resume.ExpectedPublication = pointer(2)
	_, err = withdrawn.Execute(context.Background(), admin, resume)
	expectCode(t, err, "CAMPAIGN_UNSUPPORTED")
}

type failAfterSave struct{ application.Store }
type failingTransaction struct{ application.Transaction }

func (s failAfterSave) Transact(ctx context.Context, scope application.LockScope, apply func(application.Transaction) error) error {
	return s.Store.Transact(ctx, scope, func(tx application.Transaction) error { return apply(failingTransaction{tx}) })
}
func (tx failingTransaction) Save(r application.Record, m application.Mutation) error {
	if err := tx.Transaction.Save(r, m); err != nil {
		return err
	}
	return errors.New("injected interruption after all database writes")
}

func TestFailureAfterOutboxWriteRollsBackEveryEffect(t *testing.T) {
	pool := poolFor(t)
	s := service(pool)
	p := publish(t, s, draft(t, s, time.Now().Add(-time.Minute), time.Now().Add(time.Hour)))
	broken := application.NewService(failAfterSave{postgres.New(pool)}, allowed{}, caps)
	c := interaction(t, "join", p.CampaignID, 1, 0)
	_, err := broken.Execute(context.Background(), wallet, c)
	expectCode(t, err, "CAMPAIGN_DATA_UNAVAILABLE")
	queries := []string{
		`SELECT count(*) FROM campaign_interaction_states WHERE campaign_id=$1`,
		`SELECT count(*) FROM campaign_interaction_history WHERE campaign_id=$1`,
		`SELECT count(*) FROM campaign_commands WHERE target_id=$1 AND operation='join'`,
		`SELECT count(*) FROM campaign_audit_events WHERE campaign_id=$1 AND operation='join'`,
		`SELECT count(*) FROM durable_outbox WHERE payload->>'campaignId'=$1 AND topic<>'campaign.publication.changed'`,
		`SELECT interaction_version::int FROM campaign_publications WHERE campaign_id=$1`,
	}
	for _, query := range queries {
		if count(t, pool, query, p.CampaignID) != 0 {
			t.Fatalf("transaction leaked effect: %s", query)
		}
	}
	retry := execute(t, s, wallet, c)
	if retry.Replayed || retry.Original.InteractionVersion != 1 {
		t.Fatal("rolled back command prevented retry")
	}
}

func TestAcceptedThenCancelledTermsStayLocked(t *testing.T) {
	pool := poolFor(t)
	s := service(pool)
	start := time.Now().Add(time.Hour)
	end := start.Add(time.Hour)
	p := publish(t, s, draft(t, s, start, end))
	execute(t, s, wallet, interaction(t, "set-reminder", p.CampaignID, 1, 0))
	execute(t, s, wallet, interaction(t, "remove-reminder", p.CampaignID, 0, 1))
	updated := input(start.Add(time.Hour), end.Add(time.Hour))
	updated.MaterialTerms.Copy.En.Title = "Different promise"
	c := command(t, "revision", p.CampaignID)
	c.Revision = &updated
	c.ExpectedPublication = pointer(1)
	r := execute(t, s, admin, c).Original
	c = command(t, "publish", p.CampaignID)
	c.CandidateRevisionID = r.RevisionID
	c.ExpectedPublication = pointer(1)
	_, err := s.Execute(context.Background(), admin, c)
	expectCode(t, err, "CAMPAIGN_TERMS_LOCKED")
	if count(t, pool, `SELECT publication_version::int FROM campaign_publications WHERE campaign_id=$1`, p.CampaignID) != 1 {
		t.Fatal("terms lock changed publication")
	}
}
