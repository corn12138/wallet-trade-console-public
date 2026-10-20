package outboxrepair

import (
	"context"
	"testing"
)

type discardCommittedRepairResponse struct {
	Store
	discarded bool
}

func (s *discardCommittedRepairResponse) Complete(ctx context.Context, p Prepared, guard func(context.Context) error) (State, error) {
	state, err := s.Store.Complete(ctx, p, guard)
	if err == nil && !s.discarded {
		s.discarded = true
		return State{}, ErrOutcomeUnknown
	}
	return state, err
}

// This injects response loss after a real successful PostgreSQL commit; it does
// not claim that a database transport or process was interrupted.
func TestPostgresCommittedOutcomeReconcilesAfterInjectedResponseLoss(t *testing.T) {
	pool := repairDatabase(t)
	ctx := context.Background()
	p := preparedRepair(t, pool, seedRepair(t, pool, 1, true)[0])
	store := &discardCommittedRepairResponse{Store: NewPostgres(pool)}
	authority := &testAuthority{value: p.Authority}
	service := New(store, authority)
	if _, err := service.Execute(ctx, p.Request); err != ErrOutcomeUnknown {
		t.Fatalf("discarded response: %v", err)
	}
	gen, dead := scalarRepair(t, pool, p.EventID)
	if gen != 1 || dead != nil {
		t.Fatal("success was not committed before response loss")
	}
	result, err := service.Status(ctx, p.CommandID)
	if err != nil || result.Outcome == nil || result.Outcome.Status != "succeeded" || !result.Outcome.CompletedAt.Equal(result.Outcome.After.AvailableAt) {
		t.Fatalf("status reconciliation: %+v %v", result, err)
	}
	execRepair(t, pool, `UPDATE public.durable_outbox SET attempts=3,error_code='publisherUnavailable',dead_lettered_at=$2 WHERE id=$1`, p.EventID, p.Expected.DeadLetterAt)
	authority.value.Version++
	authority.value.Role = "new-valid-replay-grant"
	retry := p.Request
	retry.RequestID = "retry-distinct-correlation"
	result, err = service.Execute(ctx, retry)
	if err != nil || result.Outcome == nil || result.Outcome.After.Generation != 1 {
		t.Fatalf("retry reconciliation: %+v %v", result, err)
	}
	gen, dead = scalarRepair(t, pool, p.EventID)
	if gen != 1 || dead == nil {
		t.Fatal("retry reset a later incarnation")
	}
	assertRepairCount(t, pool, "outbox_replay_intents", 1)
	assertRepairCount(t, pool, "outbox_replay_outcomes", 1)
}
