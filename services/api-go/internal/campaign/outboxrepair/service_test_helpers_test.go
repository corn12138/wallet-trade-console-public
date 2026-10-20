package outboxrepair

import (
	"context"
	"time"
)

type testAuthority struct {
	value Authority
	err   error
	calls []string
}

func (a *testAuthority) Authorize(_ context.Context, operation string) (Authority, error) {
	a.calls = append(a.calls, operation)
	return a.value, a.err
}

type testStore struct {
	state                         *State
	beforeAdmit, beforeComplete   func()
	afterComplete                 func()
	beforeQuery, beforeList       func()
	commitUnknown                 bool
	admissions, resets, listReads int
	deadline                      time.Time
	page                          Page
}

func (s *testStore) Admit(ctx context.Context, p Prepared, guard func(context.Context) error) (State, error) {
	s.deadline, _ = ctx.Deadline()
	if s.state != nil {
		if p.Fingerprint != s.state.Intent.Fingerprint {
			return State{}, ErrConflict
		}
		return *s.state, nil
	}
	if s.beforeAdmit != nil {
		s.beforeAdmit()
	}
	if err := guard(ctx); err != nil {
		return State{}, err
	}
	s.admissions++
	s.state = &State{Intent: Intent{Prepared: p, AcceptedAt: time.Now().UTC()}}
	return *s.state, nil
}

func (s *testStore) Complete(ctx context.Context, _ Prepared, guard func(context.Context) error) (State, error) {
	if s.beforeComplete != nil {
		s.beforeComplete()
	}
	if err := guard(ctx); err != nil {
		return State{}, err
	}
	s.resets++
	s.state.Outcome = &Outcome{Status: "succeeded", Code: "replayed", CompletedAt: time.Now().UTC()}
	if s.afterComplete != nil {
		s.afterComplete()
	}
	if s.commitUnknown {
		return State{}, ErrOutcomeUnknown
	}
	return *s.state, nil
}

func (s *testStore) Query(ctx context.Context, id, principal string) (State, error) {
	s.deadline, _ = ctx.Deadline()
	if s.beforeQuery != nil {
		s.beforeQuery()
	}
	if s.state == nil || s.state.Intent.CommandID != id || s.state.Intent.Authority.Principal != principal {
		return State{}, ErrNotFound
	}
	return *s.state, nil
}

func (s *testStore) ListDeadLetters(ctx context.Context, _ *Position, _ int) (Page, error) {
	s.deadline, _ = ctx.Deadline()
	s.listReads++
	if s.beforeList != nil {
		s.beforeList()
	}
	return s.page, nil
}

func requestFixture() Request {
	return Request{CommandID: "019a0000-0000-7000-8000-000000000081", EventID: "019a0000-0000-7000-8000-000000000082",
		Reason: "retry after supported schema deployment", RequestID: "request-1",
		Expected: Expected{Generation: 0, DeadLetterAt: time.Date(2026, 10, 16, 3, 0, 0, 123000, time.UTC), Attempts: 10, ErrorCode: "leaseExpired"}}
}

func authorityFixture() *testAuthority {
	return &testAuthority{value: Authority{Principal: "maintenance:test-owner", Role: "test-replay-grant", Version: 7}}
}
