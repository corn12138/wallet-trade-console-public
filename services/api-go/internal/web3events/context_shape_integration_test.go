package web3events

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// Each mutation is otherwise well typed, so only a total shape predicate can
// reject it. SQL UNKNOWN must never count as a valid context state.
func TestContextShapeRejectsMissingRequiredValues(t *testing.T) {
	pool, _ := contextDB(t)
	chainID := isolatedChain(t, pool)
	ctx := context.Background()
	base := `INSERT INTO web3_transactions
  (chain_id, tx_hash, from_address, block_number, status, created_at,
   execution_context_state, execution_context_mode, execution_context_claim_digest,
   execution_context_claimed_revision, execution_context_operation, execution_context_market,
   execution_context_binding, execution_context_consumer, execution_context_manifest,
   execution_context_reason, execution_context_revision, execution_context_classified_at)
  VALUES ($1, $2, $3, 0, 'pending', now(), $4, $5, repeat('a',64),
   1, 'trade.approveCollateral', 'ETH-USD', repeat('b',64), 'web-trade', repeat('c',64), $6, NULL, now())`
	for i, tc := range []struct {
		name, state  string
		mode, reason any
	}{
		{"recorded needs revision", "recorded", "bound", nil},
		{"unverified needs mode", "claimUnverified", nil, nil},
		{"mismatch needs reason", "claimMismatch", "bound", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := pool.Exec(ctx, base, chainID, hashOf(string(rune('a'+i))), ownerA, tc.state, tc.mode, tc.reason)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != "web3_tx_ctx_shape_check" {
				t.Fatalf("invalid shape accepted or wrong constraint: %v", err)
			}
		})
	}
}

func TestContextDiagnosticUsesOnlyApprovedVocabulary(t *testing.T) {
	pool, _ := contextDB(t)
	chainID := isolatedChain(t, pool)
	_, err := pool.Exec(context.Background(), `INSERT INTO web3_transactions
  (chain_id, tx_hash, from_address, block_number, status, created_at,
   execution_context_state, execution_context_reason, execution_context_diagnostic, execution_context_classified_at)
  VALUES ($1, $2, $3, 0, 'pending', now(), 'claimMismatch', 'malformed', 'secret:value', now())`,
		chainID, hashOf("diagnostic"), ownerA)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != "web3_tx_ctx_diagnostic_check" {
		t.Fatalf("raw diagnostic accepted or wrong constraint: %v", err)
	}
}
