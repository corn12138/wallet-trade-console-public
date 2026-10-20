package web3events

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func waitForReportBlocker(t *testing.T, pool *pgxpool.Pool, waiter, blocker uint32) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for ctx.Err() == nil {
		var blocked bool
		if err := pool.QueryRow(ctx, `SELECT $2::integer=ANY(pg_blocking_pids($1::integer))`, waiter, blocker).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the designated report backend did not wait on the designated first writer")
}

func TestContextFirstInsertIsAtomicToConcurrentReader(t *testing.T) {
	pool, _ := contextDB(t)
	chainID := isolatedChain(t, pool)
	revision := aRevision(t, pool)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	hash := hashOf("ab")
	input := attemptWrite{chainID: chainID, txHash: hash, fromAddress: ownerA, senderNonce: nonceOf(1), contextColumns: true,
		context: boundWrite(ContextRecorded, strings.Repeat("a", 64), revision, "")}
	if _, err := recordAttemptTx(ctx, tx, input); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := tx.QueryRow(ctx, `SELECT execution_context_state FROM web3_transactions WHERE chain_id=$1 AND tx_hash=$2`, chainID, hash).Scan(&state); err != nil || state != ContextRecorded {
		t.Fatalf("the inserted row does not contain its classified context: state=%q err=%v", state, err)
	}
	var visible int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM web3_transactions WHERE chain_id=$1 AND tx_hash=$2`, chainID, hash).Scan(&visible); err != nil || visible != 0 {
		t.Fatalf("concurrent reader observed an uncommitted attempt: count=%d err=%v", visible, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if got := contextOf(t, pool, chainID, hash); got.state != ContextRecorded || got.revision != revision {
		t.Fatalf("concurrent reader observed a committed null context: %+v", got)
	}
}

func TestContextFirstWriterWinsBothOrdersAndClientKeyModes(t *testing.T) {
	pool, _ := contextDB(t)
	revision := aRevision(t, pool)
	recorded := boundWrite(ContextRecorded, strings.Repeat("a", 64), revision, "")
	for _, clientKeys := range []bool{false, true} {
		for _, boundFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("clientKeys=%v/boundFirst=%v", clientKeys, boundFirst), func(t *testing.T) {
				chainID := isolatedChain(t, pool)
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				first, err := pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer first.Rollback(context.Background())
				input := attemptWrite{chainID: chainID, txHash: hashOf("ac"), fromAddress: ownerA, senderNonce: nonceOf(5), contextColumns: true}
				if boundFirst {
					input.context = recorded
				}
				if clientKeys {
					input.clientSupplied, input.clientActionID = true, "cli:proof-first-01"
				}
				if _, err = recordAttemptTx(ctx, first, input); err != nil {
					t.Fatal(err)
				}
				secondConn, err := pool.Acquire(ctx)
				if err != nil {
					t.Fatal(err)
				}
				waiter, blocker := secondConn.Conn().PgConn().PID(), first.Conn().PgConn().PID()
				input.context = recorded
				if boundFirst {
					input.context = nil
				}
				if clientKeys {
					input.clientActionID = "cli:proof-second-02"
				}
				var logs bytes.Buffer
				old := slog.Default()
				slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
				defer slog.SetDefault(old)
				done := make(chan error, 1)
				go func() {
					defer secondConn.Release()
					tx, err := secondConn.Begin(ctx)
					if err == nil {
						defer tx.Rollback(context.Background())
						_, err = recordAttemptTx(ctx, tx, input)
						if err == nil {
							err = tx.Commit(ctx)
						}
					}
					done <- err
				}()
				waitForReportBlocker(t, pool, waiter, blocker)
				if err = first.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				secondErr := <-done
				if clientKeys && !errors.Is(secondErr, ErrActionAttemptBound) || !clientKeys && secondErr != nil {
					t.Fatalf("losing report result: %v", secondErr)
				}
				want := "notRecorded"
				if boundFirst {
					want = ContextRecorded
				}
				if got := contextOf(t, pool, chainID, input.txHash); got.state != want {
					t.Fatalf("first committed context changed: got=%s want=%s", got.state, want)
				}
				if !clientKeys && !strings.Contains(logs.String(), `"contextOutcome":"ignoredExisting"`) {
					t.Fatal("the losing report did not record ignoredExisting")
				}
				var actions, attempts, allocated int
				if err := pool.QueryRow(ctx, `SELECT count(*),COALESCE(sum(attempt_count),0) FROM web3_transaction_actions WHERE chain_id=$1`, chainID).Scan(&actions, &allocated); err != nil {
					t.Fatal(err)
				}
				if err := pool.QueryRow(ctx, `SELECT count(*) FROM web3_transactions WHERE chain_id=$1`, chainID).Scan(&attempts); err != nil || actions != 1 || attempts != 1 || allocated != 1 {
					t.Fatalf("first-writer action allocation changed: actions=%d attempts=%d allocated=%d err=%v", actions, attempts, allocated, err)
				}
			})
		}
	}
}
