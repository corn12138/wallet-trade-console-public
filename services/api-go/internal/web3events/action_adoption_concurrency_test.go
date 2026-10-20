package web3events

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestLegacyActionAdoptionAfterInitialMiss(t *testing.T) {
	pool := requireMigratedDatabase(t)
	for _, columns := range []bool{false, true} {
		t.Run(fmt.Sprintf("context=%t", columns), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			chainID := isolatedChain(t, pool)
			in := adoptionInput(chainID, hashOf("d1"), columns, false)
			if _, err := NewRepository(pool).recordAttempt(ctx, in); err != nil {
				t.Fatal(err)
			}
			action, _, _ := actionOf(t, pool, chainID, in.txHash)
			first, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer first.Rollback(context.Background())
			if _, err := first.Exec(ctx, `SELECT id FROM web3_transaction_actions WHERE id=$1 FOR UPDATE`, action); err != nil {
				t.Fatal(err)
			}
			second, err := pool.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer second.Release()
			in.txHash = hashOf("e1")
			done := runContextOutcomeReport(ctx, second, in)
			// The row is absent until this writer demonstrably waits on the
			// action. An old writer can insert without taking that action lock.
			waitForReportBlocker(t, pool, second.Conn().PgConn().PID(), first.Conn().PgConn().PID())
			seedUnboundAttempt(t, pool, chainID, in.txHash)
			before := contextColumnsJSON(t, pool, chainID, in.txHash)
			if err := first.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatalf("initial-miss adoption failed: %v", err)
			}
			gotAction, number, _ := actionOf(t, pool, chainID, in.txHash)
			if gotAction != action || number != 2 {
				t.Fatalf("initial-miss adoption ordinal=%d action=%s", number, gotAction)
			}
			assertAdoptionCount(t, pool, action, 2)
			if contextColumnsJSON(t, pool, chainID, in.txHash) != before {
				t.Fatal("initial-miss adoption changed null context")
			}
		})
	}
}

func TestLegacyActionAdoptionInitialMissRefusesForeignBinding(t *testing.T) {
	pool := requireMigratedDatabase(t)
	for _, columns := range []bool{false, true} {
		for _, foreignAction := range []bool{false, true} {
			t.Run(fmt.Sprintf("context=%t/foreignAction=%t", columns, foreignAction), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				chainID := isolatedChain(t, pool)
				repo := NewRepository(pool)
				in := adoptionInput(chainID, hashOf("d6"), columns, false)
				if _, err := repo.recordAttempt(ctx, in); err != nil {
					t.Fatal(err)
				}
				action, _, _ := actionOf(t, pool, chainID, in.txHash)
				first, err := pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer first.Rollback(context.Background())
				if _, err := first.Exec(ctx, `SELECT id FROM web3_transaction_actions WHERE id=$1 FOR UPDATE`, action); err != nil {
					t.Fatal(err)
				}
				second, err := pool.Acquire(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer second.Release()
				in.txHash = hashOf("e6")
				done := runContextOutcomeReport(ctx, second, in)
				waitForReportBlocker(t, pool, second.Conn().PgConn().PID(), first.Conn().PgConn().PID())
				wantErr := ErrTransactionOwnerConflict
				if foreignAction {
					other := adoptionInput(chainID, in.txHash, false, false)
					other.senderNonce = nonceOf(99)
					if _, err := repo.recordAttempt(ctx, other); err != nil {
						t.Fatal(err)
					}
					wantErr = ErrActionAttemptBound
				} else {
					seedUnboundAttempt(t, pool, chainID, in.txHash)
					if _, err := pool.Exec(ctx, `UPDATE web3_transactions SET from_address=$3 WHERE chain_id=$1 AND tx_hash=$2`, chainID, in.txHash, ownerB); err != nil {
						t.Fatal(err)
					}
				}
				snapshot := func() string {
					t.Helper()
					var raw []byte
					if err := pool.QueryRow(ctx, `SELECT to_jsonb(t) FROM web3_transactions t WHERE chain_id=$1 AND tx_hash=$2`, chainID, in.txHash).Scan(&raw); err != nil {
						t.Fatal(err)
					}
					return string(raw)
				}
				before := snapshot()
				if err := first.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				if err := <-done; !errors.Is(err, wantErr) {
					t.Fatalf("initial-miss foreign row error=%v want=%v", err, wantErr)
				}
				if snapshot() != before {
					t.Fatal("refused adoption changed foreign row")
				}
				assertAdoptionCount(t, pool, action, 1)
				if foreignAction {
					otherAction, _, _ := actionOf(t, pool, chainID, in.txHash)
					assertAdoptionCount(t, pool, otherAction, 1)
				}
			})
		}
	}
}

func TestLegacyActionAdoptionConcurrentReporters(t *testing.T) {
	pool := requireMigratedDatabase(t)
	for _, columns := range []bool{false, true} {
		for _, sameHash := range []bool{false, true} {
			t.Run(fmt.Sprintf("context=%t/sameHash=%t", columns, sameHash), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				chainID := isolatedChain(t, pool)
				hash := hashOf("d2")
				secondHash, want := hashOf("e2"), 2
				seedUnboundAttempt(t, pool, chainID, hash)
				if sameHash {
					secondHash, want = hash, 1
				} else {
					seedUnboundAttempt(t, pool, chainID, secondHash)
				}
				before := contextColumnsJSON(t, pool, chainID, secondHash)
				first, err := pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer first.Rollback(context.Background())
				if _, err := recordAttemptTx(ctx, first, adoptionInput(chainID, hash, columns, false)); err != nil {
					t.Fatal(err)
				}
				second, err := pool.Acquire(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer second.Release()
				done := runContextOutcomeReport(ctx, second, adoptionInput(chainID, secondHash, columns, false))
				waitForReportBlocker(t, pool, second.Conn().PgConn().PID(), first.Conn().PgConn().PID())
				if err := first.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				if err := <-done; err != nil {
					t.Fatalf("concurrent adoption failed: %v", err)
				}
				action, number, _ := actionOf(t, pool, chainID, secondHash)
				if number != want {
					t.Fatalf("concurrent adopted ordinal=%d want=%d", number, want)
				}
				firstAction, firstNumber, _ := actionOf(t, pool, chainID, hash)
				if firstAction != action || firstNumber != 1 {
					t.Fatal("concurrent adoption changed first binding")
				}
				assertAdoptionCount(t, pool, action, want)
				if contextColumnsJSON(t, pool, chainID, secondHash) != before {
					t.Fatal("concurrent adoption changed null context")
				}
			})
		}
	}
}
