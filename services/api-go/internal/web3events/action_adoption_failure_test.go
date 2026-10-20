package web3events

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestLegacyActionAdoptionFailureRollsBackAllocation(t *testing.T) {
	pool, _ := proofReportDatabase(t)
	if _, err := pool.Exec(context.Background(), `ALTER TABLE web3_transactions
		ADD CONSTRAINT adoption_value_check CHECK (value IS NULL) NOT VALID`); err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(pool)
	for _, columns := range []bool{false, true} {
		for _, receipt := range []bool{false, true} {
			for _, occupied := range []bool{false, true} {
				t.Run(fmt.Sprintf("context=%t/receipt=%t/occupied=%t", columns, receipt, occupied), func(t *testing.T) {
					ctx := context.Background()
					chainID := isolatedChain(t, pool)
					var action string
					if occupied {
						if _, err := repo.recordAttempt(ctx, adoptionInput(chainID, hashOf("a3"), columns, false)); err != nil {
							t.Fatal(err)
						}
						action, _, _ = actionOf(t, pool, chainID, hashOf("a3"))
					}
					in := adoptionInput(chainID, hashOf("b3"), columns, receipt)
					seedUnboundAttempt(t, pool, chainID, in.txHash)
					before := contextColumnsJSON(t, pool, chainID, in.txHash)
					// A real non-context constraint fails after allocation. It
					// must abort the entire adoption, including a newly made action.
					in.value = p("1")
					_, err := repo.recordAttempt(ctx, in)
					var pgErr *pgconn.PgError
					if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != "adoption_value_check" {
						t.Fatalf("expected non-context storage failure, got %v", err)
					}
					var unbound bool
					if err := pool.QueryRow(ctx, `SELECT action_id IS NULL AND attempt_number=1
						FROM web3_transactions WHERE chain_id=$1 AND tx_hash=$2`, chainID, in.txHash).Scan(&unbound); err != nil || !unbound {
						t.Fatalf("failed adoption left a partial binding: %v", err)
					}
					if contextColumnsJSON(t, pool, chainID, in.txHash) != before {
						t.Fatal("failed adoption changed context")
					}
					if occupied {
						assertAdoptionCount(t, pool, action, 1)
					} else {
						var actions int
						if err := pool.QueryRow(ctx, `SELECT count(*) FROM web3_transaction_actions WHERE chain_id=$1`, chainID).Scan(&actions); err != nil || actions != 0 {
							t.Fatalf("failed adoption retained an orphan action: count=%d err=%v", actions, err)
						}
					}
					in.value = nil
					in.fromAddress = ownerB
					if _, err := repo.recordAttempt(ctx, in); !errors.Is(err, ErrTransactionOwnerConflict) {
						t.Fatalf("foreign-owner adoption was not refused: %v", err)
					}
					in.fromAddress = ownerA
					if _, err := repo.recordAttempt(ctx, in); err != nil {
						t.Fatal(err)
					}
					action, number, _ := actionOf(t, pool, chainID, in.txHash)
					want := 1
					if occupied {
						want++
					}
					if number != want {
						t.Fatal("failed allocation consumed an ordinal")
					}
					assertAdoptionCount(t, pool, action, want)
				})
			}
		}
	}
}

func TestLegacyActionAdoptionContextFallbackAllocatesOnce(t *testing.T) {
	pool := requireMigratedDatabase(t)
	repo := NewRepository(pool)
	for _, receipt := range []bool{false, true} {
		t.Run(fmt.Sprintf("receipt=%t", receipt), func(t *testing.T) {
			ctx := context.Background()
			chainID := isolatedChain(t, pool)
			if _, err := repo.recordAttempt(ctx, adoptionInput(chainID, hashOf("a4"), true, false)); err != nil {
				t.Fatal(err)
			}
			in := adoptionInput(chainID, hashOf("b4"), true, receipt)
			seedUnboundAttempt(t, pool, chainID, in.txHash)
			before := contextColumnsJSON(t, pool, chainID, in.txHash)
			in.context.Market = p("invalid-market")
			logs := captureContextOutcomes(t)
			if _, err := repo.recordAttempt(ctx, in); err != nil {
				t.Fatalf("context fallback lost adoption: %v", err)
			}
			if !strings.Contains(logs.String(), `"msg":"contextStorageRejected"`) ||
				!strings.Contains(logs.String(), `"constraint":"web3_tx_ctx_market_check"`) {
				t.Fatal("the real context constraint fallback was not exercised")
			}
			action, number, _ := actionOf(t, pool, chainID, in.txHash)
			if number != 2 {
				t.Fatalf("fallback adoption ordinal=%d want=2", number)
			}
			assertAdoptionCount(t, pool, action, 2)
			if contextColumnsJSON(t, pool, chainID, in.txHash) != before {
				t.Fatal("fallback changed historical null context")
			}
		})
	}
}
