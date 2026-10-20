package web3events

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func seedUnboundAttempt(t *testing.T, pool *pgxpool.Pool, chainID int, hash string) {
	t.Helper()
	// Old binaries may insert after the one-time action backfill. Their default
	// ordinal is a placeholder until the row belongs to an action.
	_, err := pool.Exec(context.Background(), `INSERT INTO web3_transactions
		(chain_id, tx_hash, from_address, status, block_number, created_at)
		VALUES ($1,$2,$3,'pending',0,now())`, chainID, hash, ownerA)
	if err != nil {
		t.Fatal(err)
	}
}

func TestLegacyActionAdoptionPreContextSchema(t *testing.T) {
	url := os.Getenv("WEB3TX_LEGACY_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("WEB3TX_LEGACY_TEST_DATABASE_URL unset; pre-context schema compatibility is unproven")
	}
	t.Setenv("WEB3TX_TEST_DATABASE_URL", url)
	pool := requireMigratedDatabase(t)
	ctx := context.Background()
	var columns int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_name='web3_transactions' AND column_name LIKE 'execution_context_%'`).Scan(&columns); err != nil || columns != 0 {
		t.Fatalf("legacy fixture must lack all context columns: columns=%d err=%v", columns, err)
	}
	repo := NewRepository(pool)
	for _, receipt := range []bool{false, true} {
		t.Run(fmt.Sprintf("receipt=%t", receipt), func(t *testing.T) {
			chainID := isolatedChain(t, pool)
			if _, err := repo.recordAttempt(ctx, adoptionInput(chainID, hashOf("a5"), false, false)); err != nil {
				t.Fatal(err)
			}
			in := adoptionInput(chainID, hashOf("b5"), false, receipt)
			seedUnboundAttempt(t, pool, chainID, in.txHash)
			if _, err := repo.recordAttempt(ctx, in); err != nil {
				t.Fatalf("legacy-schema adoption failed: %v", err)
			}
			action, number, _ := actionOf(t, pool, chainID, in.txHash)
			if number != 2 {
				t.Fatalf("legacy-schema adoption ordinal=%d want=2", number)
			}
			if _, err := repo.recordAttempt(ctx, adoptionInput(chainID, hashOf("c5"), false, false)); err != nil {
				t.Fatal(err)
			}
			assertAdoptionCount(t, pool, action, 3)
		})
	}
}

func assertAdoptionCount(t *testing.T, pool *pgxpool.Pool, action string, want int) {
	t.Helper()
	var count, rows, largest int
	err := pool.QueryRow(context.Background(), `SELECT a.attempt_count, count(t.id), max(t.attempt_number)
		FROM web3_transaction_actions a JOIN web3_transactions t ON t.action_id=a.id
		WHERE a.id=$1 GROUP BY a.attempt_count`, action).Scan(&count, &rows, &largest)
	if err != nil || count != want || rows != want || largest != want {
		t.Fatalf("adoption allocation: count=%d rows=%d largest=%d want=%d err=%v", count, rows, largest, want, err)
	}
}

func adoptionInput(chainID int, hash string, columns, receipt bool) attemptWrite {
	in := attemptWrite{chainID: chainID, txHash: hash, fromAddress: ownerA,
		senderNonce: nonceOf(41), contextColumns: columns}
	if columns {
		in.context = boundWrite(ContextClaimUnverified, hashOf("aa")[2:], 1, "")
	}
	if receipt {
		in.receipt = &receiptFacts{status: StatusConfirmed, blockNumber: 10, confirmations: 1}
	}
	return in
}

func TestLegacyActionAdoptionAllocatesAndPreservesContext(t *testing.T) {
	pool := requireMigratedDatabase(t)
	repo := NewRepository(pool)
	for _, columns := range []bool{false, true} {
		for _, receipt := range []bool{false, true} {
			for _, occupied := range []bool{false, true} {
				t.Run(fmt.Sprintf("context=%t/receipt=%t/occupied=%t", columns, receipt, occupied), func(t *testing.T) {
					chainID := isolatedChain(t, pool)
					ctx := context.Background()
					want := 1
					if occupied {
						if _, err := repo.recordAttempt(ctx, adoptionInput(chainID, hashOf("a1"), columns, false)); err != nil {
							t.Fatal(err)
						}
						want++
					}
					hash := hashOf("b1")
					seedUnboundAttempt(t, pool, chainID, hash)
					before := contextColumnsJSON(t, pool, chainID, hash)
					in := adoptionInput(chainID, hash, columns, receipt)
					if _, err := repo.recordAttempt(ctx, in); err != nil {
						t.Fatalf("legacy adoption failed: %v", err)
					}
					action, number, _ := actionOf(t, pool, chainID, hash)
					if number != want {
						t.Fatalf("adopted ordinal=%d want=%d", number, want)
					}
					assertAdoptionCount(t, pool, action, want)
					// A later nonce observation cannot move or renumber a bound row.
					in.senderNonce = nonceOf(99)
					if _, err := repo.recordAttempt(ctx, in); err != nil {
						t.Fatal(err)
					}
					retryAction, retryNumber, _ := actionOf(t, pool, chainID, hash)
					if retryAction != action || retryNumber != number {
						t.Fatal("bound retry changed action or ordinal")
					}
					if contextColumnsJSON(t, pool, chainID, hash) != before {
						t.Fatal("adoption rebound the historical null context")
					}
					if _, err := repo.recordAttempt(ctx, adoptionInput(chainID, hashOf("c1"), columns, false)); err != nil {
						t.Fatalf("same-nonce follow-up failed: %v", err)
					}
					followAction, followNumber, _ := actionOf(t, pool, chainID, hashOf("c1"))
					if followAction != action || followNumber != want+1 {
						t.Fatalf("follow-up action/ordinal=%s/%d want=%s/%d", followAction, followNumber, action, want+1)
					}
					assertAdoptionCount(t, pool, action, want+1)
				})
			}
		}
	}
}
