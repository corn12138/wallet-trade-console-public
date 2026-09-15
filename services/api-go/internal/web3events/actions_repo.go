package web3events

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// attemptTx is the SQL surface the action/attempt sequence needs. Keeping it
// small lets the failure-path tests drive the sequence without a live database.
type attemptTx interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
}

// uniqueViolation is the SQLSTATE for a lost insert race.
const uniqueViolation = "23505"

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolation
}

// resolveAction finds or creates the action for this attempt.
//
// The fingerprint is only ever compared when the CALLER supplied the key: a
// derived action carries a NULL fingerprint, and a NULL fingerprint can never
// conflict — it is adopted by the first request that brings one. That is what
// keeps CLIENT_ACTION_ID_REUSED unreachable for a caller that supplies no key.
func resolveAction(
	ctx context.Context,
	tx attemptTx,
	chainID int,
	owner, clientActionID string,
	actionType *string,
	fingerprint *string,
	clientSupplied bool,
) (string, error) {
	for attempt := 0; attempt < 2; attempt++ {
		var (
			id     string
			stored *string
		)
		err := tx.QueryRow(ctx, `
			SELECT id, request_fingerprint
			FROM web3_transaction_actions
			WHERE chain_id = $1 AND owner_address = $2 AND client_action_id = $3
			FOR UPDATE
		`, chainID, owner, clientActionID).Scan(&id, &stored)

		switch {
		case err == nil:
			if clientSupplied && stored != nil && fingerprint != nil && *stored != *fingerprint {
				return "", ErrClientActionIDReused
			}
			// Adopt the first fingerprint an action is ever given; never
			// overwrite one, so a reuse stays detectable.
			if stored == nil && fingerprint != nil {
				if _, err := tx.Exec(ctx, `
					UPDATE web3_transaction_actions
					SET request_fingerprint = $2, updated_at = now()
					WHERE id = $1
				`, id, *fingerprint); err != nil {
					return "", fmt.Errorf("web3events: adopt fingerprint: %w", err)
				}
			}
			return id, nil

		case errors.Is(err, pgx.ErrNoRows):
			// gen_random_uuid() because Prisma's @default(cuid()) is a
			// client-side default that emits no database default.
			var created string
			err := tx.QueryRow(ctx, `
				INSERT INTO web3_transaction_actions
					(id, chain_id, owner_address, client_action_id, action_type,
					 request_fingerprint, status, attempt_count, created_at, updated_at)
				VALUES (gen_random_uuid()::text, $1, $2, $3, $4, $5, 'pending', 0, now(), now())
				RETURNING id
			`, chainID, owner, clientActionID, actionType, fingerprint).Scan(&created)
			if err == nil {
				return created, nil
			}
			if isUniqueViolation(err) {
				// Another writer won the race; re-read it on the next pass.
				continue
			}
			return "", fmt.Errorf("web3events: create action: %w", err)

		default:
			return "", fmt.Errorf("web3events: resolve action: %w", err)
		}
	}
	return "", fmt.Errorf("web3events: action resolution did not converge")
}

// existingAttempt is what the sequence needs to know about a transaction that
// has already been reported.
type existingAttempt struct {
	found         bool
	actionID      *string
	fromAddress   string
	attemptNumber int
}

// lockAttempt reads and locks the attempt row for this chain/hash.
func lockAttempt(ctx context.Context, tx attemptTx, chainID int, txHash string) (existingAttempt, error) {
	var out existingAttempt
	err := tx.QueryRow(ctx, `
		SELECT action_id, from_address, attempt_number
		FROM web3_transactions
		WHERE chain_id = $1 AND tx_hash = $2
		FOR UPDATE
	`, chainID, txHash).Scan(&out.actionID, &out.fromAddress, &out.attemptNumber)
	if errors.Is(err, pgx.ErrNoRows) {
		return existingAttempt{}, nil
	}
	if err != nil {
		return existingAttempt{}, fmt.Errorf("web3events: lock attempt: %w", err)
	}
	out.found = true
	return out, nil
}

// claimAttemptNumber allocates the number for a new attempt, or returns the one
// the row already has.
//
// The re-check matters: lockAttempt's SELECT ... FOR UPDATE locks nothing when
// the row does not exist, so two concurrent reports of one hash can both believe
// the attempt is new. By the time we get here the action row IS locked, so a
// second look settles it and the counter is bumped once per real attempt. The
// frontend fires its submit and receipt POSTs as two un-awaited promises, so
// that overlap is reachable in normal use, not just under load.
func claimAttemptNumber(ctx context.Context, tx attemptTx, actionID string, chainID int, txHash string) (int, error) {
	var existingNumber int
	err := tx.QueryRow(ctx, `
		SELECT attempt_number FROM web3_transactions WHERE chain_id = $1 AND tx_hash = $2
	`, chainID, txHash).Scan(&existingNumber)
	if err == nil {
		return existingNumber, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("web3events: re-check attempt: %w", err)
	}
	return nextAttemptNumber(ctx, tx, actionID)
}

// nextAttemptNumber bumps the action's counter under its row lock.
func nextAttemptNumber(ctx context.Context, tx attemptTx, actionID string) (int, error) {
	var number int
	if err := tx.QueryRow(ctx, `
		UPDATE web3_transaction_actions
		SET attempt_count = attempt_count + 1, updated_at = now()
		WHERE id = $1
		RETURNING attempt_count
	`, actionID).Scan(&number); err != nil {
		return 0, fmt.Errorf("web3events: allocate attempt number: %w", err)
	}
	return number, nil
}

// refreshActionStatus recomputes the action's status from its attempts, in the
// same transaction as the attempt write so the two can never drift.
func refreshActionStatus(ctx context.Context, tx attemptTx, actionID string) error {
	rows, err := tx.Query(ctx, `
		SELECT status FROM web3_transactions WHERE action_id = $1
	`, actionID)
	if err != nil {
		return fmt.Errorf("web3events: read attempt statuses: %w", err)
	}
	defer rows.Close()

	statuses := make([]string, 0, 4)
	for rows.Next() {
		var status string
		if err := rows.Scan(&status); err != nil {
			return fmt.Errorf("web3events: scan attempt status: %w", err)
		}
		statuses = append(statuses, status)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("web3events: iterate attempt statuses: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE web3_transaction_actions SET status = $2, updated_at = now() WHERE id = $1
	`, actionID, DeriveActionStatus(statuses)); err != nil {
		return fmt.Errorf("web3events: store action status: %w", err)
	}
	return nil
}

// markReplacedSiblings closes out the other attempts that share this attempt's
// sender nonce once one of them has mined. A replacement never deletes or
// rewrites the original — it records that the original lost.
//
// It returns the action ids of every attempt it touched. Those actions may not
// be the winner's: an attempt first recorded without a nonce keys a hash-derived
// action, and a later nonce backfill can put a sibling in a different action. If
// only the winner's action were refreshed, the loser's would keep reporting
// 'pending' with no live attempt left.
func markReplacedSiblings(
	ctx context.Context,
	tx attemptTx,
	chainID int,
	owner, winnerHash string,
	senderNonce *uint64,
) ([]string, error) {
	if senderNonce == nil {
		// Without a nonce the server cannot know two hashes are the same
		// intent, so it declines to guess rather than replacing the wrong row.
		return nil, nil
	}
	rows, err := tx.Query(ctx, `
		UPDATE web3_transactions
		SET status = $4, replaced_by_hash = $3, last_checked_at = now()
		WHERE chain_id = $1
		  AND LOWER(from_address) = LOWER($2)
		  AND sender_nonce = $5
		  AND tx_hash <> $3
		  AND status = $6
		RETURNING action_id
	`, chainID, owner, winnerHash, StatusReplaced, int64(*senderNonce), StatusPending)
	if err != nil {
		return nil, fmt.Errorf("web3events: mark replaced siblings: %w", err)
	}
	defer rows.Close()

	var affected []string
	for rows.Next() {
		var actionID *string
		if err := rows.Scan(&actionID); err != nil {
			return nil, fmt.Errorf("web3events: scan replaced sibling: %w", err)
		}
		if actionID != nil {
			affected = append(affected, *actionID)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("web3events: iterate replaced siblings: %w", err)
	}
	return affected, nil
}

// refreshActions recomputes each distinct action's status.
func refreshActions(ctx context.Context, tx attemptTx, actionIDs []string) error {
	seen := make(map[string]struct{}, len(actionIDs))
	for _, id := range actionIDs {
		if _, done := seen[id]; done {
			continue
		}
		seen[id] = struct{}{}
		if err := refreshActionStatus(ctx, tx, id); err != nil {
			return err
		}
	}
	return nil
}

// isMinedStatus reports whether the chain executed the transaction at all. Both
// outcomes consume the sender's nonce, which is what makes a sibling replaced.
func isMinedStatus(status string) bool {
	return status == StatusConfirmed || status == StatusFailed
}

// ownerMatches keeps the comparison in one place: every write path lowercases
// the owner via auth.ResolveOwner, but legacy rows may not be lowercased.
func ownerMatches(stored, incoming string) bool {
	return strings.EqualFold(strings.TrimSpace(stored), strings.TrimSpace(incoming))
}

// SelectStaleAttempts returns non-terminal attempts due for another look.
//
// The backoff is exponential in check_attempts, which is what keeps a
// permanently failing row from being re-selected first on every cycle and
// starving the batch — the failure the indexer's repair pass learned to avoid.
func (r *Repository) SelectStaleAttempts(ctx context.Context, cfg ReconcilerConfig) ([]StaleAttempt, error) {
	if r.pool == nil {
		return nil, ErrPoolUnavailable
	}
	rows, err := r.pool.Query(ctx, `
		SELECT chain_id, tx_hash, from_address, sender_nonce, check_attempts, missing_streak
		FROM web3_transactions
		WHERE status = 'pending'
		  AND (
		    last_checked_at IS NULL
		    OR last_checked_at < now() - make_interval(
		         secs => LEAST($2::double precision * POWER(2, LEAST(check_attempts, 8)), $3::double precision))
		  )
		ORDER BY last_checked_at NULLS FIRST, id
		LIMIT $1
	`, cfg.BatchSize, cfg.BaseRecheck.Seconds(), cfg.MaxRecheck.Seconds())
	if err != nil {
		return nil, fmt.Errorf("web3events: select stale attempts: %w", err)
	}
	defer rows.Close()

	out := make([]StaleAttempt, 0, cfg.BatchSize)
	for rows.Next() {
		var (
			attempt StaleAttempt
			nonce   *int64
		)
		if err := rows.Scan(&attempt.ChainID, &attempt.TxHash, &attempt.Owner, &nonce,
			&attempt.CheckAttempts, &attempt.MissingStreak); err != nil {
			return nil, fmt.Errorf("web3events: scan stale attempt: %w", err)
		}
		if nonce != nil && *nonce >= 0 {
			converted := uint64(*nonce)
			attempt.SenderNonce = &converted
		}
		out = append(out, attempt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("web3events: iterate stale attempts: %w", err)
	}
	return out, nil
}

// ApplyAttemptOutcome records one examination.
//
// The check bookkeeping is written on EVERY outcome, including one that decided
// nothing, so an unreachable node moves the row down the queue instead of
// pinning it at the front. A terminal status is never overwritten here either —
// the reconciler only ever resolves a row that is still pending.
func (r *Repository) ApplyAttemptOutcome(ctx context.Context, outcome AttemptOutcome) error {
	if r.pool == nil {
		return ErrPoolUnavailable
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("web3events: begin outcome write: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var nonce *int64
	if outcome.SenderNonce != nil {
		converted := int64(*outcome.SenderNonce)
		nonce = &converted
	}

	var actionID *string
	if err := tx.QueryRow(ctx, `
		UPDATE web3_transactions
		SET check_attempts   = check_attempts + 1,
		    -- Consecutive, so any answer other than "gone" resets it.
		    missing_streak   = CASE WHEN $10::boolean THEN missing_streak + 1 ELSE 0 END,
		    last_checked_at  = now(),
		    failure_code     = $3,
		    failure_detail   = $4,
		    sender_nonce     = COALESCE(sender_nonce, $9),
		    status           = CASE WHEN $5::text = '' THEN status ELSE $5 END,
		    block_number     = COALESCE($6, block_number),
		    block_hash       = COALESCE($7, block_hash),
		    confirmations    = GREATEST(confirmations, $8),
		    confirmed_at     = CASE WHEN $5::text = 'confirmed' THEN COALESCE(confirmed_at, now()) ELSE confirmed_at END,
		    finalized_at     = CASE WHEN $11::boolean AND $5::text = 'confirmed'
		                            THEN COALESCE(finalized_at, now()) ELSE finalized_at END
		WHERE chain_id = $1 AND tx_hash = $2 AND status = 'pending'
		RETURNING action_id
	`, outcome.ChainID, outcome.TxHash, nullIfEmpty(outcome.FailureCode), outcome.FailureDetail,
		outcome.Status, outcome.BlockNumber, outcome.BlockHash, outcome.Confirmations, nonce,
		outcome.Missing, outcome.Finalized,
	).Scan(&actionID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Already terminal, or gone. Nothing to repair and nothing to
			// report — another writer got there first.
			return nil
		}
		return fmt.Errorf("web3events: apply attempt outcome: %w", err)
	}

	// A REVERTED transaction consumes the sender's nonce exactly as a
	// successful one does, so its same-nonce siblings are just as replaced.
	// Gating this on 'confirmed' left them pending forever.
	if isMinedStatus(outcome.Status) {
		var owner string
		if err := tx.QueryRow(ctx, `
			SELECT from_address FROM web3_transactions WHERE chain_id=$1 AND tx_hash=$2
		`, outcome.ChainID, outcome.TxHash).Scan(&owner); err != nil {
			return fmt.Errorf("web3events: read attempt owner: %w", err)
		}
		affected, err := markReplacedSiblings(ctx, tx, outcome.ChainID, owner, outcome.TxHash, outcome.SenderNonce)
		if err != nil {
			return err
		}
		if err := refreshActions(ctx, tx, affected); err != nil {
			return err
		}
	}
	if actionID != nil {
		if err := refreshActionStatus(ctx, tx, *actionID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func nullIfEmpty(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
