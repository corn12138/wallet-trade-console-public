//go:build integration

package postgres_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/application"
)

func TestConcurrentCreateUsesFixedCollectionKey(t *testing.T) {
	pool := poolFor(t)
	s := service(pool)
	c := command(t, "create", "")
	revision := input(time.Now().Add(time.Hour), time.Now().Add(2*time.Hour))
	c.Revision = &revision
	var outcomes [2]application.Outcome
	var errs [2]error
	var group sync.WaitGroup
	for i := range outcomes {
		group.Add(1)
		go func(i int) { defer group.Done(); outcomes[i], errs[i] = s.Execute(context.Background(), admin, c) }(i)
	}
	group.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if outcomes[0].Original.CampaignID != outcomes[1].Original.CampaignID || outcomes[0].Replayed == outcomes[1].Replayed {
		t.Fatal("duplicate creation was not replayed")
	}
	id := outcomes[0].Original.CampaignID
	if count(t, pool, `SELECT count(*) FROM campaign_commands WHERE client_command_id=$1`, c.ClientCommandID) != 1 {
		t.Fatal("duplicate command ledger")
	}
	archive := command(t, "archive", id)
	archive.ExpectedPublication = pointer(0)
	execute(t, s, admin, archive)
	if count(t, pool, `SELECT count(*) FROM durable_outbox WHERE payload->>'campaignId'=$1`, id) != 0 {
		t.Fatal("unpublished draft archive leaked into public events")
	}
	replay := execute(t, s, admin, c)
	if replay.Original.PublicationVersion != 0 || replay.Current.PublicationVersion != 1 || !replay.Replayed {
		t.Fatal("replay must separate original and current")
	}
	c.Reason = "Changed semantic payload"
	_, err := s.Execute(context.Background(), admin, c)
	expectCode(t, err, "IDEMPOTENCY_CONFLICT")
}

func TestTwoAdministratorsCASOnePublication(t *testing.T) {
	pool := poolFor(t)
	s := service(pool)
	d := draft(t, s, time.Now().Add(time.Hour), time.Now().Add(2*time.Hour))
	var commands [2]application.Command
	for i := range commands {
		commands[i] = command(t, "publish", d.CampaignID)
		commands[i].ExpectedPublication = pointer(0)
		commands[i].CandidateRevisionID = d.RevisionID
	}
	var errs [2]error
	var group sync.WaitGroup
	for i := range commands {
		group.Add(1)
		go func(i int) { defer group.Done(); _, errs[i] = s.Execute(context.Background(), admin, commands[i]) }(i)
	}
	group.Wait()
	success := 0
	for _, err := range errs {
		if err == nil {
			success++
		} else {
			expectCode(t, err, "CAMPAIGN_VERSION_CONFLICT")
		}
	}
	if success != 1 {
		t.Fatal("CAS admitted multiple administrators")
	}
	if count(t, pool, `SELECT count(*) FROM campaign_publication_history WHERE campaign_id=$1`, d.CampaignID) != 1 {
		t.Fatal("history must commit once")
	}
	if count(t, pool, `SELECT count(*) FROM durable_outbox WHERE payload->>'campaignId'=$1`, d.CampaignID) != 1 {
		t.Fatal("publication must enqueue once")
	}
}

func TestJoinPauseRaceLinearizesAtPublicationLock(t *testing.T) {
	pool := poolFor(t)
	s := service(pool)
	p := publish(t, s, draft(t, s, time.Now().Add(-time.Minute), time.Now().Add(time.Hour)))
	join := interaction(t, "join", p.CampaignID, 1, 0)
	pause := command(t, "pause", p.CampaignID)
	pause.ExpectedPublication = pointer(1)
	var joinErr, pauseErr error
	var group sync.WaitGroup
	group.Add(2)
	go func() { defer group.Done(); _, joinErr = s.Execute(context.Background(), wallet, join) }()
	go func() { defer group.Done(); _, pauseErr = s.Execute(context.Background(), admin, pause) }()
	group.Wait()
	if pauseErr != nil {
		t.Fatal(pauseErr)
	}
	want := 0
	if joinErr == nil {
		want = 1
	} else {
		expectCode(t, joinErr, "CAMPAIGN_VERSION_CONFLICT")
	}
	if got := count(t, pool, `SELECT count(*) FROM campaign_interaction_history WHERE campaign_id=$1`, p.CampaignID); got != want {
		t.Fatalf("history=%d, accepted=%d", got, want)
	}
}

func TestWaitingForLockUsesPostLockDatabaseClock(t *testing.T) {
	pool := poolFor(t)
	s := service(pool)
	end := time.Now().Add(6 * time.Second).Truncate(time.Second)
	p := publish(t, s, draft(t, s, time.Now().Add(-time.Minute), end))
	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(context.Background(), `SELECT 1 FROM campaign_publications WHERE campaign_id=$1 FOR UPDATE`, p.CampaignID); err != nil {
		t.Fatal(err)
	}
	c := interaction(t, "join", p.CampaignID, 1, 0)
	if !time.Now().Before(end.Add(-200 * time.Millisecond)) {
		t.Fatal("fixture must submit before the campaign deadline to prove a lock-boundary crossing")
	}
	finished := make(chan error, 1)
	go func() { _, err := s.Execute(context.Background(), wallet, c); finished <- err }()
	select {
	case err := <-finished:
		t.Fatalf("command bypassed aggregate lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	time.Sleep(time.Until(end.Add(100 * time.Millisecond)))
	if err = tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	expectCode(t, <-finished, "CAMPAIGN_NOT_OPEN")
	if count(t, pool, `SELECT count(*) FROM campaign_interaction_states WHERE campaign_id=$1`, p.CampaignID) != 0 {
		t.Fatal("expired join persisted")
	}
}
