package outboxrepair

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/durableoutbox"
)

func TestPostgresAuditedReplayPreservesIdentityAndUnblocksOnlyItsStream(t *testing.T) {
	pool := repairDatabase(t)
	ctx := context.Background()
	ids := seedRepair(t, pool, 2, false)
	healthy := seedRepair(t, pool, 1, false)[0]
	deliveries := durableoutbox.NewRepository(pool)
	options := durableoutbox.Options{Source: "campaign", Batch: 4, Lease: time.Minute, MaxAttempts: 3, RetryBase: time.Second, RetryMax: time.Minute}
	claimed, err := deliveries.Claim(ctx, options)
	if err != nil || len(claimed) != 2 {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	var old durableoutbox.Delivery
	for _, d := range claimed {
		if d.ID == ids[0] {
			old = d
			if err = deliveries.Reject(ctx, d, options, true); err != nil {
				t.Fatal(err)
			}
		} else if d.ID == healthy {
			if err = deliveries.Ack(ctx, d); err != nil {
				t.Fatal(err)
			}
		}
	}
	if pending, err := deliveries.Claim(ctx, options); err != nil || len(pending) != 0 {
		t.Fatal("poison successor was published")
	}
	store := NewPostgres(pool)
	p := preparedRepair(t, pool, ids[0])
	if _, err = store.Admit(ctx, p, allowRepair); err != nil {
		t.Fatal(err)
	}
	result, err := store.Complete(ctx, p, allowRepair)
	if err != nil || result.Outcome == nil || result.Outcome.Status != "succeeded" || result.Outcome.After.Generation != 1 {
		t.Fatalf("complete: %+v %v", result, err)
	}
	replay, err := deliveries.Claim(ctx, options)
	if err != nil || len(replay) != 1 || replay[0].ID != ids[0] || replay[0].Token == old.Token {
		t.Fatalf("original ID/new lease: %+v %v", replay, err)
	}
	if err = deliveries.Ack(ctx, old); !errors.Is(err, durableoutbox.ErrLeaseLost) {
		t.Fatalf("stale acknowledgement: %v", err)
	}
	if _, err = store.Complete(ctx, p, allowRepair); err != nil {
		t.Fatal(err)
	}
	if err = deliveries.Ack(ctx, replay[0]); err != nil {
		t.Fatal(err)
	}
	next, err := deliveries.Claim(ctx, options)
	if err != nil || len(next) != 1 || next[0].ID != ids[1] {
		t.Fatalf("successor: %+v %v", next, err)
	}
	assertRepairCount(t, pool, "outbox_replay_intents", 1)
	assertRepairCount(t, pool, "outbox_replay_outcomes", 1)
}
func TestPostgresReplayConcurrentIdentityConflictAndLaterIncarnation(t *testing.T) {
	pool := repairDatabase(t)
	ctx := context.Background()
	id := seedRepair(t, pool, 1, true)[0]
	store := NewPostgres(pool)
	p := preparedRepair(t, pool, id)
	var wg sync.WaitGroup
	results := make(chan error, 12)
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := store.Admit(ctx, p, allowRepair); err != nil {
				results <- err
				return
			}
			_, err := store.Complete(ctx, p, allowRepair)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	assertRepairCount(t, pool, "outbox_replay_intents", 1)
	assertRepairCount(t, pool, "outbox_replay_outcomes", 1)
	other := p
	other.Reason = "different command payload"
	other = prepare(other.Request, other.Authority)
	if _, err := store.Admit(ctx, other, allowRepair); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE public.durable_outbox SET attempts=3,error_code='publisherUnavailable',dead_lettered_at=$2 WHERE id=$1`, id, p.Expected.DeadLetterAt); err != nil {
		t.Fatal(err)
	}
	// Even an identical timestamp/attempt/code is a distinct replay incarnation.
	if result, err := store.Complete(ctx, p, allowRepair); err != nil || result.Outcome.After.Generation != 1 {
		t.Fatalf("recorded outcome: %+v %v", result, err)
	}
	gen, dead := scalarRepair(t, pool, id)
	if gen != 1 || dead == nil {
		t.Fatal("duplicate command reset a later incarnation")
	}
	stale := p
	stale.CommandID = repairID(t)
	stale = prepare(stale.Request, stale.Authority)
	if _, err := store.Admit(ctx, stale, allowRepair); !errors.Is(err, ErrNotReplayable) {
		t.Fatalf("stale generation admitted: %v", err)
	}
}
func TestPostgresAcceptedIntentRestartStaleRefusalAndAuthorityChange(t *testing.T) {
	pool := repairDatabase(t)
	ctx := context.Background()
	id := seedRepair(t, pool, 1, true)[0]
	store := NewPostgres(pool)
	first := preparedRepair(t, pool, id)
	second := first
	second.CommandID = repairID(t)
	second = prepare(second.Request, second.Authority)
	for _, p := range []Prepared{first, second} {
		if _, err := store.Admit(ctx, p, allowRepair); err != nil {
			t.Fatal(err)
		}
	}
	newer := first
	newer.Authority.Version++
	if _, err := store.Complete(ctx, newer, allowRepair); !errors.Is(err, ErrAuthorityChanged) {
		t.Fatalf("old grant resumed: %v", err)
	}
	if _, err := NewPostgres(pool).Complete(ctx, first, allowRepair); err != nil {
		t.Fatal(err)
	}
	result, err := store.Complete(ctx, second, allowRepair)
	if err != nil || result.Outcome.Status != "refused" || result.Outcome.Code != "stateChanged" {
		t.Fatalf("stale accepted intent: %+v %v", result, err)
	}
	result, err = store.Admit(ctx, newer, allowRepair)
	if err != nil || result.Outcome == nil {
		t.Fatalf("new grant cannot reconcile old result: %+v %v", result, err)
	}
	if _, err = store.Query(ctx, first.CommandID, "maintenance:other"); !errors.Is(err, ErrNotFound) {
		t.Fatal("cross-principal query exposed audit")
	}
}
