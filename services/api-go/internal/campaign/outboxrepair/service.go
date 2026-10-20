package outboxrepair

import (
	"context"
	"errors"
	"time"
)

type Service struct {
	store     Store
	authority Authorizer
}

func New(store Store, authority Authorizer) *Service {
	return &Service{store: store, authority: authority}
}

func (s *Service) authorize(ctx context.Context, operation string) (Authority, error) {
	if s == nil || s.authority == nil {
		return Authority{}, ErrAuthorityUnavailable
	}
	a, err := s.authority.Authorize(ctx, operation)
	if err != nil {
		if errors.Is(err, ErrUnauthorized) {
			return Authority{}, ErrUnauthorized
		}
		return Authority{}, ErrAuthorityUnavailable
	}
	if !validAuthority(a) {
		return Authority{}, ErrAuthorityUnavailable
	}
	return a, nil
}

func (s *Service) guard(operation string, original Authority) func(context.Context) error {
	return func(ctx context.Context) error {
		current, err := s.authorize(ctx, operation)
		if err != nil {
			return err
		}
		if current != original {
			return ErrAuthorityChanged
		}
		return nil
	}
}

func safeStoreError(err error) error {
	if err == nil {
		return nil
	}
	for _, known := range []error{ErrInvalidInput, ErrUnauthorized, ErrAuthorityUnavailable, ErrAuthorityChanged,
		ErrUnavailable, ErrOutcomeUnknown, ErrConflict, ErrNotFound, ErrNotReplayable} {
		if errors.Is(err, known) {
			return known
		}
	}
	return ErrUnavailable
}

// Execute preserves the caller's command ID across admission and completion.
// An error after admission does not prove that no reset committed: callers must
// reconcile that command, including when authority was lost before disclosure.
func (s *Service) Execute(ctx context.Context, request Request) (State, error) {
	var empty State
	if !validRequest(request) {
		return empty, ErrInvalidInput
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	a, err := s.authorize(ctx, ReplayOperation)
	if err != nil {
		return empty, err
	}
	if s.store == nil {
		return empty, ErrUnavailable
	}
	command, guard := prepare(request, a), s.guard(ReplayOperation, a)
	state, err := s.store.Admit(ctx, command, guard)
	if err != nil {
		return empty, safeStoreError(err)
	}
	if state.Outcome == nil {
		if state.Intent.Authority != a {
			return empty, ErrAuthorityChanged
		}
		state, err = s.store.Complete(ctx, command, guard)
		if err != nil {
			return empty, safeStoreError(err)
		}
	}
	if err = guard(ctx); err != nil {
		return empty, err
	}
	return state, nil
}

func (s *Service) Status(ctx context.Context, commandID string) (State, error) {
	var empty State
	if !uuid.MatchString(commandID) {
		return empty, ErrInvalidInput
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	a, err := s.authorize(ctx, ReplayOperation)
	if err != nil {
		return empty, err
	}
	if s.store == nil {
		return empty, ErrUnavailable
	}
	state, err := s.store.Query(ctx, commandID, a.Principal)
	if err != nil {
		return empty, safeStoreError(err)
	}
	if err = s.guard(ReplayOperation, a)(ctx); err != nil {
		return empty, err
	}
	return state, nil
}
