package outboxrepair

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"
)

func TestUnauthenticatedOrInvalidRequestNeverAdmitted(t *testing.T) {
	store := &testStore{}
	if _, err := New(store, nil).Execute(context.Background(), requestFixture()); err != ErrAuthorityUnavailable {
		t.Fatalf("nil authority: %v", err)
	}
	a := authorityFixture()
	a.err = errors.New("untrusted credential diagnostic")
	if _, err := New(store, a).Execute(context.Background(), requestFixture()); err != ErrAuthorityUnavailable {
		t.Fatalf("unclosed authority error: %v", err)
	}
	a.err = nil
	for _, mutate := range []func(*Request){
		func(r *Request) { r.CommandID = "not-an-id" }, func(r *Request) { r.EventID = "00000000-0000-0000-0000-000000000000" },
		func(r *Request) { r.Expected.Generation = math.MaxInt64 }, func(r *Request) { r.Expected.Attempts = 0 },
		func(r *Request) { r.Expected.ErrorCode = "raw diagnostic" }, func(r *Request) { r.Expected.DeadLetterAt = r.Expected.DeadLetterAt.Add(time.Nanosecond) },
		func(r *Request) { r.Reason = "reason\nwith injected line" }, func(r *Request) { r.RequestID = " value" },
	} {
		r := requestFixture()
		mutate(&r)
		if _, err := New(store, a).Execute(context.Background(), r); err != ErrInvalidInput {
			t.Fatalf("invalid request accepted: %v", err)
		}
	}
	if store.admissions != 0 || store.resets != 0 {
		t.Fatal("refused request changed state")
	}
}

func TestAuthorityLostWhileWaitingDoesNotResetAndKeepsIntent(t *testing.T) {
	for _, phase := range []string{"admission", "completion"} {
		t.Run(phase, func(t *testing.T) {
			a, store := authorityFixture(), &testStore{}
			revoke := func() { a.err = ErrUnauthorized }
			if phase == "admission" {
				store.beforeAdmit = revoke
			} else {
				store.beforeComplete = revoke
			}
			s := New(store, a)
			if _, err := s.Execute(context.Background(), requestFixture()); err != ErrUnauthorized || store.resets != 0 {
				t.Fatalf("revocation: %v, resets=%d", err, store.resets)
			}
			if (store.state != nil) != (phase == "completion") {
				t.Fatal("accepted intent boundary lost")
			}
			if store.state != nil {
				a.err = nil
				state, err := s.Status(context.Background(), requestFixture().CommandID)
				if err != nil || state.Outcome != nil || state.Intent.CommandID != requestFixture().CommandID {
					t.Fatal("pending intent cannot be reconciled")
				}
			}
		})
	}
}

func TestUnknownCommitRetryReturnsRecordedOutcomeWithoutAnotherReset(t *testing.T) {
	a, store := authorityFixture(), &testStore{commitUnknown: true}
	s := New(store, a)
	r := requestFixture()
	if _, err := s.Execute(context.Background(), r); err != ErrOutcomeUnknown || store.state.Outcome == nil {
		t.Fatalf("unknown commit: %v", err)
	}
	r.RequestID = "reconcile-request-2"
	a.value.Version++
	result, err := s.Execute(context.Background(), r)
	if err != nil || result.Outcome == nil || store.resets != 1 || store.admissions != 1 {
		t.Fatalf("repeated effect after lost response: %v %+v", err, result)
	}
	r.Reason = "different semantic request"
	if _, err = s.Execute(context.Background(), r); err != ErrConflict || store.resets != 1 {
		t.Fatal("same command accepted a changed payload")
	}
}

func TestChangedAuthorityCannotResumePendingIntent(t *testing.T) {
	a, store := authorityFixture(), &testStore{}
	p := prepare(requestFixture(), a.value)
	store.state = &State{Intent: Intent{Prepared: p, AcceptedAt: time.Now().UTC()}}
	a.value.Version++
	s := New(store, a)
	if _, err := s.Execute(context.Background(), requestFixture()); err != ErrAuthorityChanged || store.resets != 0 {
		t.Fatalf("old grant resumed: %v", err)
	}
	if state, err := s.Status(context.Background(), p.CommandID); err != nil || state.Intent.Authority != p.Authority {
		t.Fatal("original intent attribution was lost")
	}
}

func TestStatusRechecksBeforeDisclosure(t *testing.T) {
	a, store := authorityFixture(), &testStore{}
	s := New(store, a)
	if _, err := s.Execute(context.Background(), requestFixture()); err != nil {
		t.Fatal(err)
	}
	store.beforeQuery = func() { a.err = ErrAuthorityUnavailable }
	if state, err := s.Status(context.Background(), requestFixture().CommandID); err != ErrAuthorityUnavailable || state.Intent.CommandID != "" {
		t.Fatal("late private outcome escaped after authority became unknown")
	}
	if until := time.Until(store.deadline); until <= 0 || until > 5*time.Second {
		t.Fatalf("status lacks bounded context: %s", until)
	}
	a.err = nil
	a.value.Principal = "maintenance:other-owner"
	if _, err := s.Status(context.Background(), requestFixture().CommandID); err != ErrNotFound {
		t.Fatal("another principal read an intent")
	}
}

func TestCompletedWriteRechecksBeforeDisclosureAndRetainsOutcome(t *testing.T) {
	a, store := authorityFixture(), &testStore{}
	store.afterComplete = func() { a.err = ErrUnauthorized }
	s := New(store, a)
	request := requestFixture()
	state, err := s.Execute(context.Background(), request)
	if err != ErrUnauthorized || state.Intent.CommandID != "" || state.Outcome != nil {
		t.Fatal("late private outcome escaped after authority was revoked")
	}
	if store.resets != 1 || store.state == nil || store.state.Outcome == nil {
		t.Fatal("loss of disclosure authority erased a committed outcome")
	}
	a.err = nil
	state, err = s.Status(context.Background(), request.CommandID)
	if err != nil || state.Outcome == nil || state.Outcome.Status != "succeeded" {
		t.Fatal("committed outcome cannot be reconciled after reauthorization")
	}
	if _, err := s.Execute(context.Background(), request); err != nil || store.resets != 1 {
		t.Fatal("retry applied the committed reset again")
	}
}
