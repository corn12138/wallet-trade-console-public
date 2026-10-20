package web3events

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestLegacyActionAdoptionUpsertKeepsBoundOrdinal(t *testing.T) {
	pool := requireMigratedDatabase(t)
	for _, columns := range []bool{false, true} {
		for _, receipt := range []bool{false, true} {
			t.Run(fmt.Sprintf("context=%t/receipt=%t", columns, receipt), func(t *testing.T) {
				ctx := context.Background()
				chainID := isolatedChain(t, pool)
				in := adoptionInput(chainID, hashOf("a8"), columns, receipt)
				if _, err := NewRepository(pool).recordAttempt(ctx, in); err != nil {
					t.Fatal(err)
				}
				action, _, _ := actionOf(t, pool, chainID, in.txHash)
				before := contextColumnsJSON(t, pool, chainID, in.txHash)
				tx, err := pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback(ctx)
				// An unused ordinal avoids a uniqueness error masking a missing
				// SQL guard. This tests the write boundary independently of allocation.
				if _, err := writeAttemptRowWithContext(ctx, tx, in, action, 9); err != nil {
					t.Fatal(err)
				}
				if err := tx.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				_, number, _ := actionOf(t, pool, chainID, in.txHash)
				if number != 1 {
					t.Fatalf("upsert renumbered a bound attempt: ordinal=%d", number)
				}
				assertAdoptionCount(t, pool, action, 1)
				if contextColumnsJSON(t, pool, chainID, in.txHash) != before {
					t.Fatal("bound upsert changed context")
				}
			})
		}
	}
}

func TestLegacyActionAdoptionRecheckKeepsRowLock(t *testing.T) {
	pool := requireMigratedDatabase(t)
	for _, columns := range []bool{false, true} {
		t.Run(fmt.Sprintf("context=%t", columns), func(t *testing.T) {
			ctx := context.Background()
			chainID := isolatedChain(t, pool)
			in := adoptionInput(chainID, hashOf("a9"), columns, false)
			seedUnboundAttempt(t, pool, chainID, in.txHash)
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			action, err := bindAction(ctx, tx, in, existingAttempt{})
			if err != nil {
				t.Fatal(err)
			}
			var prior *contextPreimage
			if columns {
				prior = &contextPreimage{}
			}
			if number, err := claimAttemptNumber(ctx, tx, action, chainID, in.txHash, prior); err != nil || number != 1 {
				t.Fatalf("recheck allocation failed: number=%d err=%v", number, err)
			}
			_, err = pool.Exec(ctx, `SELECT id FROM web3_transactions WHERE chain_id=$1 AND tx_hash=$2 FOR UPDATE NOWAIT`, chainID, in.txHash)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
				t.Fatalf("adoption recheck did not retain row lock: %v", err)
			}
		})
	}
}
