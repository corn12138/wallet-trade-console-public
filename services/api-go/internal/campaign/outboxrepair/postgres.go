package outboxrepair

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Postgres struct{ pool *pgxpool.Pool }

func NewPostgres(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

var _ Store = (*Postgres)(nil)

func preparedValid(p Prepared) bool {
	return validRequest(p.Request) && validAuthority(p.Authority) && prepare(p.Request, p.Authority).Fingerprint == p.Fingerprint
}
func rollback(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}
func (s *Postgres) begin(ctx context.Context, options pgx.TxOptions) (pgx.Tx, error) {
	if s == nil || s.pool == nil {
		return nil, ErrUnavailable
	}
	tx, err := s.pool.BeginTx(ctx, options)
	if err != nil {
		return nil, ErrUnavailable
	}
	return tx, nil
}
func commandLock(ctx context.Context, tx pgx.Tx, id string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "outbox-replay:"+id)
	if err != nil {
		return ErrUnavailable
	}
	return nil
}
func checkExisting(state State, p Prepared) error {
	if state.Intent.Authority.Principal != p.Authority.Principal {
		return ErrNotFound
	}
	if state.Intent.Fingerprint != p.Fingerprint {
		return ErrConflict
	}
	return nil
}
func finish(ctx context.Context, tx pgx.Tx, state State) (State, error) {
	if tx.Commit(ctx) != nil {
		return State{}, ErrOutcomeUnknown
	}
	return state, nil
}

func (s *Postgres) Admit(ctx context.Context, p Prepared, guard func(context.Context) error) (State, error) {
	if !preparedValid(p) || guard == nil {
		return State{}, ErrInvalidInput
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.begin(ctx, pgx.TxOptions{})
	if err != nil {
		return State{}, err
	}
	defer rollback(tx)
	if err = commandLock(ctx, tx, p.CommandID); err != nil {
		return State{}, err
	}
	state, err := loadState(ctx, tx, p.CommandID)
	if err == nil {
		if err = guard(ctx); err != nil {
			return State{}, safeStoreError(err)
		}
		if err = checkExisting(state, p); err != nil {
			return State{}, err
		}
		return finish(ctx, tx, state)
	}
	if !errors.Is(err, ErrNotFound) {
		return State{}, err
	}
	event, err := lockEvent(ctx, tx, p.EventID)
	if err != nil {
		return State{}, err
	}
	if err = guard(ctx); err != nil {
		return State{}, safeStoreError(err)
	}
	event, err = readEvent(ctx, tx, p.EventID)
	if err != nil {
		return State{}, err
	}
	if !event.matches(p.Expected) {
		return State{}, ErrNotReplayable
	}
	intent := Intent{Prepared: p}
	err = tx.QueryRow(ctx, `INSERT INTO public.outbox_replay_intents(command_id,principal,authority_role,authority_version,operation,source,event_id,expected_generation,expected_dead_lettered_at,expected_attempts,expected_error_code,fingerprint,reason,request_id)
 VALUES($1,$2,$3,$4,'campaign.outbox.replay','campaign',$5,$6,$7,$8,$9,$10,$11,$12) RETURNING accepted_at`, p.CommandID, p.Authority.Principal, p.Authority.Role, p.Authority.Version, p.EventID, p.Expected.Generation, p.Expected.DeadLetterAt, p.Expected.Attempts, p.Expected.ErrorCode, p.Fingerprint, p.Reason, p.RequestID).Scan(&intent.AcceptedAt)
	if err != nil {
		return State{}, ErrUnavailable
	}
	return finish(ctx, tx, State{Intent: intent})
}

func (s *Postgres) Complete(ctx context.Context, p Prepared, guard func(context.Context) error) (State, error) {
	if !preparedValid(p) || guard == nil {
		return State{}, ErrInvalidInput
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.begin(ctx, pgx.TxOptions{})
	if err != nil {
		return State{}, err
	}
	defer rollback(tx)
	if err = commandLock(ctx, tx, p.CommandID); err != nil {
		return State{}, err
	}
	state, err := loadState(ctx, tx, p.CommandID)
	if err != nil {
		return State{}, err
	}
	if err = checkExisting(state, p); err != nil {
		return State{}, err
	}
	if state.Outcome != nil {
		if err = guard(ctx); err != nil {
			return State{}, safeStoreError(err)
		}
		return finish(ctx, tx, state)
	}
	if state.Intent.Authority != p.Authority {
		return State{}, ErrAuthorityChanged
	}
	before, err := lockEvent(ctx, tx, p.EventID)
	if err != nil {
		return State{}, err
	}
	if err = guard(ctx); err != nil {
		return State{}, safeStoreError(err)
	}
	before, err = readEvent(ctx, tx, p.EventID)
	if err != nil {
		return State{}, err
	}
	status, code := "refused", "stateChanged"
	after := before
	if before.matches(p.Expected) {
		// The database stamps reset time/transaction identity and requires the
		// matching success audit at commit; direct delivery grants cannot bypass it.
		_, err = tx.Exec(ctx, `UPDATE public.durable_outbox SET dead_lettered_at=NULL,error_code=NULL,attempts=0,lease_owner=NULL,lease_until=NULL,replay_generation=replay_generation+1 WHERE id=$1`, p.EventID)
		if err != nil {
			return State{}, ErrUnavailable
		}
		after, err = readEvent(ctx, tx, p.EventID)
		if err != nil {
			return State{}, err
		}
		status, code = "succeeded", "replayed"
	}
	outcome := &Outcome{Status: status, Code: code}
	var beforeRaw, afterRaw []byte
	err = tx.QueryRow(ctx, `INSERT INTO public.outbox_replay_outcomes(command_id,event_id,replay_generation,status,code,before_state,after_state,completed_at)
 VALUES($1,$2,$3,$4,$5,$6::jsonb,$7::jsonb,clock_timestamp()) RETURNING before_state,after_state,completed_at`, p.CommandID, p.EventID, after.snapshot.Generation, status, code, before.raw, after.raw).Scan(&beforeRaw, &afterRaw, &outcome.CompletedAt)
	if err != nil {
		return State{}, ErrUnavailable
	}
	if decodeSnapshot(beforeRaw, &outcome.Before) != nil || decodeSnapshot(afterRaw, &outcome.After) != nil {
		return State{}, ErrUnavailable
	}
	state.Outcome = outcome
	return finish(ctx, tx, state)
}
