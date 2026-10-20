package web3events

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestContextOutcomeRequiresExactLockedTransition(t *testing.T) {
	digest := str("claim")
	incoming := &ContextWrite{State: ContextRecorded, ClaimDigest: digest}
	for _, tc := range []struct {
		name     string
		prior    contextPreimage
		state    *string
		digest   *string
		inserted bool
		want     string
	}{
		{"same claim resolves", contextPreimage{str(ContextClaimUnverified), digest}, str(ContextRecorded), digest, false, "upgraded"},
		{"different locked claim", contextPreimage{str(ContextClaimUnverified), str("other")}, str(ContextRecorded), digest, false, "ignoredExisting"},
		{"already final", contextPreimage{str(ContextRecorded), digest}, str(ContextRecorded), digest, false, "ignoredExisting"},
		{"locked final null", contextPreimage{}, str(ContextRecorded), digest, false, "ignoredExisting"},
		{"unresolved final row", contextPreimage{str(ContextClaimUnverified), digest}, str(ContextClaimUnverified), digest, false, "retainedUnverified"},
		{"different final claim", contextPreimage{str(ContextClaimUnverified), digest}, str(ContextRecorded), str("other"), false, "ignoredExisting"},
		{"returned insert dominates", contextPreimage{str(ContextClaimUnverified), digest}, str(ContextRecorded), digest, true, "applied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := contextOutcome(incoming, tc.state, tc.digest, tc.inserted, tc.prior); got != tc.want {
				t.Fatalf("locked transition outcome = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestContextOutcomeRecheckLocksPreimage(t *testing.T) {
	pool, repo := contextDB(t)
	chainID, hash := isolatedChain(t, pool), hashOf("cb")
	revision, digest := aRevision(t, pool), strings.Repeat("a", 64)
	if err := submitCtx(t, repo, chainID, hash, 11, boundWrite(ContextClaimUnverified, digest, revision, "")); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	input := attemptWrite{chainID: chainID, txHash: hash, fromAddress: ownerA, senderNonce: nonceOf(11), contextColumns: true}
	action, err := bindAction(ctx, tx, input, existingAttempt{})
	if err != nil {
		t.Fatal(err)
	}
	var prior contextPreimage
	number, err := claimAttemptNumber(ctx, tx, action, chainID, hash, &prior)
	if err != nil || number != 1 || prior.state == nil || *prior.state != ContextClaimUnverified || prior.digest == nil || *prior.digest != digest {
		t.Fatalf("recheck preimage/number = %+v/%d, err=%v", prior, number, err)
	}
	_, err = pool.Exec(ctx, `SELECT id FROM web3_transactions WHERE chain_id=$1 AND tx_hash=$2 FOR UPDATE NOWAIT`, chainID, hash)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
		t.Fatalf("recheck did not retain row lock: %v", err)
	}
}
