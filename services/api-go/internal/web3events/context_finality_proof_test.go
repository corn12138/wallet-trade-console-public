package web3events

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func contextColumnsJSON(t *testing.T, pool *pgxpool.Pool, chainID int, hash string) string {
	t.Helper()
	var raw []byte
	err := pool.QueryRow(context.Background(), `SELECT
 (SELECT jsonb_object_agg(key,value) FROM jsonb_each(to_jsonb(t)) WHERE key LIKE 'execution_context_%')
 FROM web3_transactions t WHERE chain_id=$1 AND tx_hash=$2`, chainID, hash).Scan(&raw)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestFinalContextsSurviveEveryLaterWriter(t *testing.T) {
	pool, repo := contextDB(t)
	revision := aRevision(t, pool)
	digest := strings.Repeat("a", 64)
	states := map[string]*ContextWrite{
		"notRecorded":        nil,
		ContextRecorded:      boundWrite(ContextRecorded, digest, revision, ""),
		ContextClaimMismatch: boundWrite(ContextClaimMismatch, digest, revision, "bytesMismatch"),
		ContextUnboundExit: {State: ContextUnboundExit, Mode: p("unboundExit"), ClaimDigest: p(digest),
			Operation: p("trade.closePosition"), Market: p("ETH-USD"), Consumer: p("web-trade"),
			Manifest: p(strings.Repeat("c", 64)), Reason: p("authorityUnavailable")},
	}
	for state, initial := range states {
		for _, writer := range []string{"submit", "receipt", "reconciler"} {
			t.Run(state+"/"+writer, func(t *testing.T) {
				chainID := isolatedChain(t, pool)
				hash := hashOf("ad")
				if err := submitCtx(t, repo, chainID, hash, 1, initial); err != nil {
					t.Fatal(err)
				}
				before := contextColumnsJSON(t, pool, chainID, hash)
				if got := contextOf(t, pool, chainID, hash); got.state != state {
					t.Fatal("initial context differs from fixture")
				}
				changed := boundWrite(ContextRecorded, strings.Repeat("b", 64), revision, "")
				if state == ContextRecorded {
					changed = boundWrite(ContextClaimMismatch, strings.Repeat("b", 64), revision, "bytesMismatch")
				}
				if state == ContextUnboundExit {
					copy := *initial
					copy.ClaimDigest, copy.Reason = p(strings.Repeat("b", 64)), p("noActiveRevision")
					changed = &copy
				}
				var err error
				switch writer {
				case "submit":
					err = submitCtx(t, repo, chainID, hash, 1, changed)
				case "receipt":
					err = receiptCtx(t, repo, chainID, hash, 1, changed)
				case "reconciler":
					_, err = pool.Exec(context.Background(), `UPDATE web3_transactions SET last_checked_at=now()-interval '1 hour' WHERE chain_id=$1 AND tx_hash=$2`, chainID, hash)
					if err == nil {
						verifier := &fakeAttemptVerifier{receipt: VerifiedReceipt{Status: StatusConfirmed, BlockNumber: 88, Confirmations: 3, SenderNonce: nonceOf(1)}}
						report, runErr := NewReconciler(repo, verifier, ReconcilerConfig{BatchSize: 1}, nil).RunOnce(context.Background())
						err = runErr
						if err == nil && (report.Examined != 1 || report.Resolved != 1) {
							err = fmt.Errorf("reconciler did not resolve the pending attempt: %+v", report)
						}
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				if contextColumnsJSON(t, pool, chainID, hash) != before {
					t.Fatal("a later writer changed final context columns")
				}
				if writer != "submit" {
					var status string
					if err := pool.QueryRow(context.Background(), `SELECT status FROM web3_transactions WHERE chain_id=$1 AND tx_hash=$2`, chainID, hash).Scan(&status); err != nil || status != StatusConfirmed {
						t.Fatalf("later writer did not actually advance transaction status: status=%s err=%v", status, err)
					}
				}
			})
		}
	}
}
