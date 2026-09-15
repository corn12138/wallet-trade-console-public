package web3events

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// requireMigratedDatabase returns a pool on a database migrated by the REAL
// migration chain.
//
// Hand-written DDL in a test can drift from the migration that actually runs —
// the previous fixture here declared BIGSERIAL/TIMESTAMPTZ/VARCHAR(20) where the
// migrated table has integer/timestamp(3)/text — so these tests refuse to invent
// a schema. Unset means the property is UNPROVEN and the test skips; set means a
// broken environment FAILS rather than skipping.
func requireMigratedDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("WEB3TX_TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("WEB3TX_TEST_DATABASE_URL unset — the durable action/attempt model is UNPROVEN, not passing")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("acceptance database is configured but unusable: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(context.Background()); err != nil {
		t.Fatalf("acceptance database is configured but unreachable: %v", err)
	}
	// Prove it is migrated rather than merely reachable.
	var exists bool
	if err := pool.QueryRow(context.Background(),
		`SELECT to_regclass('public.web3_transaction_actions') IS NOT NULL`).Scan(&exists); err != nil || !exists {
		t.Fatalf("acceptance database is not migrated (web3_transaction_actions missing): %v", err)
	}
	return pool
}

// chainCounter isolates concurrent tests without a schema per test: each takes
// its own synthetic chain id and deletes only its own rows.
var chainCounter atomic.Int32

func isolatedChain(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	chainID := 990000 + int(chainCounter.Add(1))
	clean := func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM web3_transactions WHERE chain_id = $1`, chainID)
		_, _ = pool.Exec(ctx, `DELETE FROM web3_transaction_actions WHERE chain_id = $1`, chainID)
	}
	clean()
	t.Cleanup(clean)
	return chainID
}

func hashOf(seed string) string {
	return "0x" + strings.Repeat(seed, 64/len(seed))
}

func nonceOf(n uint64) *uint64 { return &n }

const (
	ownerA = "0x000000000000000000000000000000000000dead"
	ownerB = "0x000000000000000000000000000000000000beef"
)

func actionOf(t *testing.T, pool *pgxpool.Pool, chainID int, txHash string) (id string, number int, status string) {
	t.Helper()
	if err := pool.QueryRow(context.Background(), `
		SELECT t.action_id, t.attempt_number, a.status
		FROM web3_transactions t JOIN web3_transaction_actions a ON a.id = t.action_id
		WHERE t.chain_id = $1 AND t.tx_hash = $2
	`, chainID, txHash).Scan(&id, &number, &status); err != nil {
		t.Fatalf("read action for %s: %v", txHash, err)
	}
	return id, number, status
}

// The property the whole slice exists for: a speed-up arrives under a NEW hash
// with the SAME nonce and must join the SAME action as a second attempt, with
// the original retained rather than overwritten.
func TestASpeedUpBecomesASecondAttemptOfTheSameActionAndTheOriginalIsRetained(t *testing.T) {
	pool := requireMigratedDatabase(t)
	chainID := isolatedChain(t, pool)
	repo := NewRepository(pool)
	ctx := context.Background()

	original, replacement := hashOf("a"), hashOf("b")
	nonce := nonceOf(42)

	if _, err := repo.UpsertSubmittedTransaction(ctx, SubmitTxInput{
		ChainID: chainID, TxHash: original, FromAddress: ownerA, SenderNonce: nonce,
	}); err != nil {
		t.Fatalf("submit original: %v", err)
	}
	if _, err := repo.UpsertSubmittedTransaction(ctx, SubmitTxInput{
		ChainID: chainID, TxHash: replacement, FromAddress: ownerA, SenderNonce: nonce,
	}); err != nil {
		t.Fatalf("submit replacement: %v", err)
	}

	originalAction, originalNumber, _ := actionOf(t, pool, chainID, original)
	replacementAction, replacementNumber, _ := actionOf(t, pool, chainID, replacement)

	if originalAction != replacementAction {
		t.Fatalf("a speed-up must join the same action:\n  original=%s\n  replacement=%s", originalAction, replacementAction)
	}
	if originalNumber != 1 || replacementNumber != 2 {
		t.Errorf("attempt numbers = %d and %d, want 1 and 2", originalNumber, replacementNumber)
	}

	// The replacement mines. The original must be RETAINED, marked replaced,
	// and pointed at the winner — never deleted or rewritten into the winner.
	if _, err := repo.UpsertTransactionReceipt(ctx, ReceiptTxInput{
		ChainID: chainID, TxHash: replacement, FromAddress: ownerA, SenderNonce: nonce,
		Status: StatusConfirmed, BlockNumber: 500, Confirmations: 3,
	}); err != nil {
		t.Fatalf("receipt for replacement: %v", err)
	}

	var status string
	var replacedBy *string
	if err := pool.QueryRow(ctx, `
		SELECT status, replaced_by_hash FROM web3_transactions WHERE chain_id=$1 AND tx_hash=$2
	`, chainID, original).Scan(&status, &replacedBy); err != nil {
		t.Fatalf("read original after replacement: %v", err)
	}
	if status != StatusReplaced {
		t.Errorf("original status = %q, want replaced", status)
	}
	if replacedBy == nil || *replacedBy != replacement {
		t.Errorf("replaced_by_hash = %v, want %s", replacedBy, replacement)
	}

	var attempts int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM web3_transactions WHERE action_id=$1`, originalAction).Scan(&attempts); err != nil {
		t.Fatalf("count attempts: %v", err)
	}
	if attempts != 2 {
		t.Errorf("attempts retained = %d, want 2 — a replacement must not erase history", attempts)
	}

	if _, _, actionStatus := actionOf(t, pool, chainID, replacement); actionStatus != StatusConfirmed {
		t.Errorf("action status = %q, want confirmed", actionStatus)
	}
}

// Today's frontend posts a submit and then a receipt for ONE hash. That is one
// attempt, not two.
func TestSubmitThenReceiptForOneHashRecordsExactlyOneAttempt(t *testing.T) {
	pool := requireMigratedDatabase(t)
	chainID := isolatedChain(t, pool)
	repo := NewRepository(pool)
	ctx := context.Background()
	hash := hashOf("c")

	if _, err := repo.UpsertSubmittedTransaction(ctx, SubmitTxInput{
		ChainID: chainID, TxHash: hash, FromAddress: ownerA, SenderNonce: nonceOf(1),
	}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if _, err := repo.UpsertTransactionReceipt(ctx, ReceiptTxInput{
		ChainID: chainID, TxHash: hash, FromAddress: ownerA, SenderNonce: nonceOf(1),
		Status: StatusConfirmed, BlockNumber: 10, Confirmations: 1,
	}); err != nil {
		t.Fatalf("receipt: %v", err)
	}

	actionID, number, _ := actionOf(t, pool, chainID, hash)
	if number != 1 {
		t.Errorf("attempt_number = %d, want 1", number)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT attempt_count FROM web3_transaction_actions WHERE id=$1`, actionID).Scan(&count); err != nil {
		t.Fatalf("read attempt_count: %v", err)
	}
	if count != 1 {
		t.Errorf("attempt_count = %d, want 1 — a receipt is not a new attempt", count)
	}
}

// A terminal state must survive the next client re-report, or the reconciler and
// the client flap against each other forever.
func TestATerminalStatusIsNotDowngradedByALaterClientReport(t *testing.T) {
	pool := requireMigratedDatabase(t)
	chainID := isolatedChain(t, pool)
	repo := NewRepository(pool)
	ctx := context.Background()

	for _, terminal := range []string{StatusDropped, StatusReplaced, StatusReorged} {
		t.Run(terminal, func(t *testing.T) {
			hash := hashOf(map[string]string{StatusDropped: "1", StatusReplaced: "2", StatusReorged: "3"}[terminal])
			if _, err := repo.UpsertSubmittedTransaction(ctx, SubmitTxInput{
				ChainID: chainID, TxHash: hash, FromAddress: ownerA, SenderNonce: nonceOf(90),
			}); err != nil {
				t.Fatalf("submit: %v", err)
			}
			if _, err := pool.Exec(ctx, `
				UPDATE web3_transactions SET status=$3 WHERE chain_id=$1 AND tx_hash=$2
			`, chainID, hash, terminal); err != nil {
				t.Fatalf("set terminal: %v", err)
			}

			if _, err := repo.UpsertSubmittedTransaction(ctx, SubmitTxInput{
				ChainID: chainID, TxHash: hash, FromAddress: ownerA, SenderNonce: nonceOf(90),
			}); err != nil {
				t.Fatalf("re-submit: %v", err)
			}
			var status string
			if err := pool.QueryRow(ctx, `SELECT status FROM web3_transactions WHERE chain_id=$1 AND tx_hash=$2`,
				chainID, hash).Scan(&status); err != nil {
				t.Fatalf("read status: %v", err)
			}
			if status != terminal {
				t.Errorf("after re-submit status = %q, want %q", status, terminal)
			}
		})
	}
}

// A replayed receipt must not un-write a reorg.
func TestAReplayedReceiptDoesNotReConfirmAReorgedAttempt(t *testing.T) {
	pool := requireMigratedDatabase(t)
	chainID := isolatedChain(t, pool)
	repo := NewRepository(pool)
	ctx := context.Background()
	hash := hashOf("d")

	if _, err := repo.UpsertTransactionReceipt(ctx, ReceiptTxInput{
		ChainID: chainID, TxHash: hash, FromAddress: ownerA, SenderNonce: nonceOf(3),
		Status: StatusConfirmed, BlockNumber: 77, Confirmations: 2,
	}); err != nil {
		t.Fatalf("first receipt: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE web3_transactions SET status='reorged' WHERE chain_id=$1 AND tx_hash=$2`,
		chainID, hash); err != nil {
		t.Fatalf("mark reorged: %v", err)
	}
	if _, err := repo.UpsertTransactionReceipt(ctx, ReceiptTxInput{
		ChainID: chainID, TxHash: hash, FromAddress: ownerA, SenderNonce: nonceOf(3),
		Status: StatusConfirmed, BlockNumber: 77, Confirmations: 9,
	}); err != nil {
		t.Fatalf("replayed receipt: %v", err)
	}

	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM web3_transactions WHERE chain_id=$1 AND tx_hash=$2`,
		chainID, hash).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != StatusReorged {
		t.Errorf("status = %q, want reorged — a replayed receipt must not re-confirm it", status)
	}
}

// The contract's stable 409, and the guarantee that it is unreachable without a
// client key.
func TestReusingAClientActionIDForADifferentRequestIsRejected(t *testing.T) {
	pool := requireMigratedDatabase(t)
	chainID := isolatedChain(t, pool)
	repo := NewRepository(pool)
	ctx := context.Background()

	key := "cli:checkout-abcdef01"
	first, err := ComputeRequestFingerprint(chainID, ownerA, nil, strPtr("0xaaaa"), nil, strPtr("1"), nil)
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	second, err := ComputeRequestFingerprint(chainID, ownerA, nil, strPtr("0xbbbb"), nil, strPtr("2"), nil)
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}

	if _, err := repo.UpsertSubmittedTransaction(ctx, SubmitTxInput{
		ChainID: chainID, TxHash: hashOf("e"), FromAddress: ownerA, SenderNonce: nonceOf(11),
		ClientActionID: key, ClientSupplied: true, RequestFingerprint: &first,
	}); err != nil {
		t.Fatalf("first submit: %v", err)
	}

	// Same key, same fingerprint → the SAME action, no error.
	if _, err := repo.UpsertSubmittedTransaction(ctx, SubmitTxInput{
		ChainID: chainID, TxHash: hashOf("f"), FromAddress: ownerA, SenderNonce: nonceOf(12),
		ClientActionID: key, ClientSupplied: true, RequestFingerprint: &first,
	}); err != nil {
		t.Fatalf("same fingerprint must be accepted: %v", err)
	}

	// Same key, DIFFERENT fingerprint → the stable conflict.
	if _, err := repo.UpsertSubmittedTransaction(ctx, SubmitTxInput{
		ChainID: chainID, TxHash: hashOf("0"), FromAddress: ownerA, SenderNonce: nonceOf(13),
		ClientActionID: key, ClientSupplied: true, RequestFingerprint: &second,
	}); !errors.Is(err, ErrClientActionIDReused) {
		t.Fatalf("reuse err = %v, want ErrClientActionIDReused", err)
	}
}

// A rejected report must leave nothing behind — no orphan action.
func TestAnOwnerConflictRollsBackTheWholeSequenceLeavingNoOrphanAction(t *testing.T) {
	pool := requireMigratedDatabase(t)
	chainID := isolatedChain(t, pool)
	repo := NewRepository(pool)
	ctx := context.Background()
	hash := hashOf("9")

	if _, err := repo.UpsertSubmittedTransaction(ctx, SubmitTxInput{
		ChainID: chainID, TxHash: hash, FromAddress: ownerA, SenderNonce: nonceOf(21),
	}); err != nil {
		t.Fatalf("first submit: %v", err)
	}
	var before int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM web3_transaction_actions WHERE chain_id=$1`, chainID).Scan(&before); err != nil {
		t.Fatalf("count actions: %v", err)
	}

	if _, err := repo.UpsertSubmittedTransaction(ctx, SubmitTxInput{
		ChainID: chainID, TxHash: hash, FromAddress: ownerB, SenderNonce: nonceOf(21),
	}); !errors.Is(err, ErrTransactionOwnerConflict) {
		t.Fatalf("conflicting owner err = %v, want ErrTransactionOwnerConflict", err)
	}

	var after int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM web3_transaction_actions WHERE chain_id=$1`, chainID).Scan(&after); err != nil {
		t.Fatalf("count actions: %v", err)
	}
	if after != before {
		t.Errorf("actions %d -> %d: a rejected report left an orphan action behind", before, after)
	}

	var owner string
	if err := pool.QueryRow(ctx, `SELECT from_address FROM web3_transactions WHERE chain_id=$1 AND tx_hash=$2`,
		chainID, hash).Scan(&owner); err != nil {
		t.Fatalf("read owner: %v", err)
	}
	if owner != ownerA {
		t.Errorf("owner = %s, want %s — ownership is immutable", owner, ownerA)
	}
}

// One real transaction can never mint more than one action, however many
// different keys a client throws at it.
func TestAnAttemptStaysBoundToOneActionForever(t *testing.T) {
	pool := requireMigratedDatabase(t)
	chainID := isolatedChain(t, pool)
	repo := NewRepository(pool)
	ctx := context.Background()
	hash := hashOf("8")

	if _, err := repo.UpsertSubmittedTransaction(ctx, SubmitTxInput{
		ChainID: chainID, TxHash: hash, FromAddress: ownerA, SenderNonce: nonceOf(31),
	}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	bound, _, _ := actionOf(t, pool, chainID, hash)

	fingerprint, _ := ComputeRequestFingerprint(chainID, ownerA, nil, nil, nil, nil, nil)
	if _, err := repo.UpsertSubmittedTransaction(ctx, SubmitTxInput{
		ChainID: chainID, TxHash: hash, FromAddress: ownerA, SenderNonce: nonceOf(31),
		ClientActionID: "cli:some-other-key-01", ClientSupplied: true, RequestFingerprint: &fingerprint,
	}); !errors.Is(err, ErrActionAttemptBound) {
		t.Fatalf("rebinding err = %v, want ErrActionAttemptBound", err)
	}

	still, _, _ := actionOf(t, pool, chainID, hash)
	if still != bound {
		t.Errorf("the attempt was reparented: %s -> %s", bound, still)
	}
}

// Concurrent submits of different hashes for one action must not collide on an
// attempt number.
func TestConcurrentAttemptsOfOneActionGetDistinctNumbers(t *testing.T) {
	pool := requireMigratedDatabase(t)
	chainID := isolatedChain(t, pool)
	repo := NewRepository(pool)
	ctx := context.Background()
	nonce := nonceOf(77)

	const racers = 8
	errCh := make(chan error, racers)
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		go func(i int) {
			<-start
			_, err := repo.UpsertSubmittedTransaction(ctx, SubmitTxInput{
				ChainID: chainID, FromAddress: ownerA, SenderNonce: nonce,
				TxHash: fmt.Sprintf("0x%062x%02x", 0, i),
			})
			errCh <- err
		}(i)
	}
	close(start)
	for i := 0; i < racers; i++ {
		if err := <-errCh; err != nil {
			t.Fatalf("concurrent submit: %v", err)
		}
	}

	var distinctActions, distinctNumbers, total int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(DISTINCT action_id), COUNT(DISTINCT attempt_number), COUNT(*)
		FROM web3_transactions WHERE chain_id = $1
	`, chainID).Scan(&distinctActions, &distinctNumbers, &total); err != nil {
		t.Fatalf("read: %v", err)
	}
	if total != racers {
		t.Fatalf("rows = %d, want %d", total, racers)
	}
	if distinctActions != 1 {
		t.Errorf("distinct actions = %d, want 1 — one nonce is one action", distinctActions)
	}
	if distinctNumbers != racers {
		t.Errorf("distinct attempt numbers = %d, want %d", distinctNumbers, racers)
	}
}

func strPtr(v string) *string { return &v }

// The concrete bug: the browser reports a submit, then the user switches
// account, the auth provider clears the session, and the receipt POST never
// happens. The row would otherwise stay 'pending' with block_number 0 forever.
// After a restart the reconciler must repair it from chain evidence alone.
func TestReconciliationRepairsAnAttemptWhoseReceiptWasNeverReported(t *testing.T) {
	pool := requireMigratedDatabase(t)
	chainID := isolatedChain(t, pool)
	repo := NewRepository(pool)
	ctx := context.Background()
	hash := hashOf("7")
	nonce := nonceOf(55)

	if _, err := repo.UpsertSubmittedTransaction(ctx, SubmitTxInput{
		ChainID: chainID, TxHash: hash, FromAddress: ownerA, SenderNonce: nonce,
	}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	// Age it past the recheck window, as a restart would find it.
	if _, err := pool.Exec(ctx, `
		UPDATE web3_transactions SET last_checked_at = now() - interval '1 day' WHERE chain_id=$1
	`, chainID); err != nil {
		t.Fatalf("age the row: %v", err)
	}

	blockHash := "0xrepairedblock"
	reconciler := NewReconciler(repo, &fakeAttemptVerifier{receipt: VerifiedReceipt{
		Status: StatusConfirmed, BlockNumber: 4242, BlockHash: &blockHash,
		Confirmations: 8, SenderNonce: nonce,
	}}, ReconcilerConfig{BatchSize: 10, BaseRecheck: time.Second}, nil)

	report, err := reconciler.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if report.Resolved < 1 {
		t.Fatalf("report = %+v, want at least one resolved", report)
	}

	var (
		status        string
		blockNumber   int64
		confirmations int
		storedHash    *string
		confirmedAt   *time.Time
	)
	if err := pool.QueryRow(ctx, `
		SELECT status, block_number, confirmations, block_hash, confirmed_at
		FROM web3_transactions WHERE chain_id=$1 AND tx_hash=$2
	`, chainID, hash).Scan(&status, &blockNumber, &confirmations, &storedHash, &confirmedAt); err != nil {
		t.Fatalf("read repaired row: %v", err)
	}
	if status != StatusConfirmed || blockNumber != 4242 || confirmations != 8 {
		t.Errorf("repaired row = status %q block %d confirmations %d, want confirmed/4242/8",
			status, blockNumber, confirmations)
	}
	if storedHash == nil || *storedHash != blockHash {
		t.Errorf("block_hash = %v, want %s", storedHash, blockHash)
	}
	if confirmedAt == nil {
		t.Error("confirmed_at was not stamped")
	}
	if _, _, actionStatus := actionOf(t, pool, chainID, hash); actionStatus != StatusConfirmed {
		t.Errorf("action status = %q, want confirmed", actionStatus)
	}
}

// A row the node cannot answer for must not pin itself at the front of the
// queue. Backoff is what stops one bad row from starving the batch forever.
func TestAnUnreachableNodeBacksTheAttemptOffInsteadOfStarvingTheBatch(t *testing.T) {
	pool := requireMigratedDatabase(t)
	chainID := isolatedChain(t, pool)
	repo := NewRepository(pool)
	ctx := context.Background()
	hash := hashOf("6")

	if _, err := repo.UpsertSubmittedTransaction(ctx, SubmitTxInput{
		ChainID: chainID, TxHash: hash, FromAddress: ownerA, SenderNonce: nonceOf(66),
	}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE web3_transactions SET last_checked_at = now() - interval '1 day' WHERE chain_id=$1
	`, chainID); err != nil {
		t.Fatalf("age the row: %v", err)
	}

	cfg := ReconcilerConfig{BatchSize: 10, BaseRecheck: time.Hour, MaxRecheck: 24 * time.Hour}
	reconciler := NewReconciler(repo, &fakeAttemptVerifier{receiptErr: ErrVerificationUnavailable}, cfg, nil)

	if _, err := reconciler.RunOnce(ctx); err != nil {
		t.Fatalf("first cycle: %v", err)
	}

	var checks int
	var status string
	var failureCode *string
	if err := pool.QueryRow(ctx, `
		SELECT check_attempts, status, failure_code FROM web3_transactions WHERE chain_id=$1 AND tx_hash=$2
	`, chainID, hash).Scan(&checks, &status, &failureCode); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if checks != 1 {
		t.Errorf("check_attempts = %d, want 1 — the check must be recorded even on failure", checks)
	}
	if status != StatusPending {
		t.Errorf("status = %q, want pending — an unreachable node decides nothing", status)
	}
	if failureCode == nil || *failureCode != FailureRPCUnavailable {
		t.Errorf("failure_code = %v, want %q", failureCode, FailureRPCUnavailable)
	}

	// The next cycle must NOT pick it up again: last_checked_at was bumped and
	// the backoff window is an hour.
	selected, err := repo.SelectStaleAttempts(ctx, cfg)
	if err != nil {
		t.Fatalf("re-select: %v", err)
	}
	for _, attempt := range selected {
		if attempt.ChainID == chainID && attempt.TxHash == hash {
			t.Error("a just-checked row was re-selected immediately — it would starve the batch")
		}
	}
}

// The reconciler must never resurrect or overwrite a terminal row.
func TestReconciliationLeavesATerminalAttemptAlone(t *testing.T) {
	pool := requireMigratedDatabase(t)
	chainID := isolatedChain(t, pool)
	repo := NewRepository(pool)
	ctx := context.Background()
	hash := hashOf("5")

	if _, err := repo.UpsertSubmittedTransaction(ctx, SubmitTxInput{
		ChainID: chainID, TxHash: hash, FromAddress: ownerA, SenderNonce: nonceOf(70),
	}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE web3_transactions SET status='reorged' WHERE chain_id=$1`, chainID); err != nil {
		t.Fatalf("mark reorged: %v", err)
	}

	if err := repo.ApplyAttemptOutcome(ctx, AttemptOutcome{
		ChainID: chainID, TxHash: hash, Status: StatusConfirmed, Confirmations: 99,
	}); err != nil {
		t.Fatalf("ApplyAttemptOutcome: %v", err)
	}

	var status string
	var confirmations int
	if err := pool.QueryRow(ctx, `
		SELECT status, confirmations FROM web3_transactions WHERE chain_id=$1 AND tx_hash=$2
	`, chainID, hash).Scan(&status, &confirmations); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if status != StatusReorged || confirmations != 0 {
		t.Errorf("terminal row was modified: status=%q confirmations=%d", status, confirmations)
	}
}

// A REVERTED replacement consumes the sender's nonce exactly as a successful one
// does, so its same-nonce sibling is just as replaced. Gating the linkage on
// 'confirmed' left the loser pending forever.
func TestARevertedReplacementStillMarksItsSiblingReplaced(t *testing.T) {
	pool := requireMigratedDatabase(t)
	chainID := isolatedChain(t, pool)
	repo := NewRepository(pool)
	ctx := context.Background()
	original, replacement := hashOf("a"), hashOf("b")
	nonce := nonceOf(140)

	for _, hash := range []string{original, replacement} {
		if _, err := repo.UpsertSubmittedTransaction(ctx, SubmitTxInput{
			ChainID: chainID, TxHash: hash, FromAddress: ownerA, SenderNonce: nonce,
		}); err != nil {
			t.Fatalf("submit %s: %v", hash, err)
		}
	}

	// The replacement mines and REVERTS.
	if _, err := repo.UpsertTransactionReceipt(ctx, ReceiptTxInput{
		ChainID: chainID, TxHash: replacement, FromAddress: ownerA, SenderNonce: nonce,
		Status: StatusFailed, BlockNumber: 700, Confirmations: 2,
	}); err != nil {
		t.Fatalf("reverted receipt: %v", err)
	}

	var status string
	var replacedBy *string
	if err := pool.QueryRow(ctx, `
		SELECT status, replaced_by_hash FROM web3_transactions WHERE chain_id=$1 AND tx_hash=$2
	`, chainID, original).Scan(&status, &replacedBy); err != nil {
		t.Fatalf("read original: %v", err)
	}
	if status != StatusReplaced {
		t.Errorf("original status = %q, want replaced — a revert still burns the nonce", status)
	}
	if replacedBy == nil || *replacedBy != replacement {
		t.Errorf("replaced_by_hash = %v, want %s", replacedBy, replacement)
	}
}

// A sibling can belong to a DIFFERENT action when the first attempt was recorded
// before its nonce was known. Refreshing only the winner's action left the
// loser's reporting 'pending' with no live attempt.
func TestReplacingASiblingInAnotherActionRefreshesThatActionToo(t *testing.T) {
	pool := requireMigratedDatabase(t)
	chainID := isolatedChain(t, pool)
	repo := NewRepository(pool)
	ctx := context.Background()
	original, replacement := hashOf("c"), hashOf("d")

	// Attempt 1 arrives with NO nonce -> hash-derived action.
	if _, err := repo.UpsertSubmittedTransaction(ctx, SubmitTxInput{
		ChainID: chainID, TxHash: original, FromAddress: ownerA,
	}); err != nil {
		t.Fatalf("submit original: %v", err)
	}
	// Its nonce is learned later, as the reconciler would backfill it.
	if _, err := pool.Exec(ctx, `
		UPDATE web3_transactions SET sender_nonce=$3 WHERE chain_id=$1 AND tx_hash=$2
	`, chainID, original, 150); err != nil {
		t.Fatalf("backfill nonce: %v", err)
	}
	originalAction, _, _ := actionOf(t, pool, chainID, original)

	// Attempt 2 keys the NONCE, so it lands in a different action.
	if _, err := repo.UpsertTransactionReceipt(ctx, ReceiptTxInput{
		ChainID: chainID, TxHash: replacement, FromAddress: ownerA, SenderNonce: nonceOf(150),
		Status: StatusConfirmed, BlockNumber: 800, Confirmations: 3,
	}); err != nil {
		t.Fatalf("receipt for replacement: %v", err)
	}
	replacementAction, _, _ := actionOf(t, pool, chainID, replacement)
	if originalAction == replacementAction {
		t.Skip("the two attempts share an action here; the cross-action path is not exercised")
	}

	var loserStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM web3_transaction_actions WHERE id=$1`,
		originalAction).Scan(&loserStatus); err != nil {
		t.Fatalf("read loser action: %v", err)
	}
	if loserStatus == StatusPending {
		t.Errorf("the loser's action still reports pending with no live attempt — it drifted")
	}
	if loserStatus != StatusReplaced {
		t.Errorf("loser action status = %q, want replaced", loserStatus)
	}
}

// Two concurrent reports of ONE hash must bump the counter once, not twice.
//
// This drives the two transactions explicitly rather than racing goroutines: a
// goroutine race here is not reproducible — the writes are sub-millisecond and
// simply serialise, so the test passes whether or not the guard exists. The
// interleaving that matters is that BOTH transactions read "no such attempt"
// before either commits, which `SELECT ... FOR UPDATE` cannot prevent because
// there is no row to lock. The frontend fires submit and receipt as two
// un-awaited promises, so this ordering is reachable in normal use.
func TestASecondWriterWithAStaleNotFoundDoesNotOverCountAttempts(t *testing.T) {
	pool := requireMigratedDatabase(t)
	chainID := isolatedChain(t, pool)
	ctx := context.Background()
	hash := hashOf("e")
	in := attemptWrite{
		chainID: chainID, txHash: hash, fromAddress: ownerA, senderNonce: nonceOf(160),
	}

	first, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin first: %v", err)
	}
	defer func() { _ = first.Rollback(ctx) }()
	second, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin second: %v", err)
	}
	defer func() { _ = second.Rollback(ctx) }()

	// Both writers look before either writes, and both see nothing.
	firstView, err := lockAttempt(ctx, first, chainID, hash)
	if err != nil {
		t.Fatalf("first lookup: %v", err)
	}
	secondView, err := lockAttempt(ctx, second, chainID, hash)
	if err != nil {
		t.Fatalf("second lookup: %v", err)
	}
	if firstView.found || secondView.found {
		t.Fatalf("both writers must start from a stale not-found: %+v %+v", firstView, secondView)
	}

	write := func(tx attemptTx, view existingAttempt) {
		t.Helper()
		actionID, err := bindAction(ctx, tx, in, view)
		if err != nil {
			t.Fatalf("bind: %v", err)
		}
		number := view.attemptNumber
		if !view.found {
			if number, err = claimAttemptNumber(ctx, tx, actionID, chainID, hash); err != nil {
				t.Fatalf("claim: %v", err)
			}
		}
		if _, err := writeAttemptRow(ctx, tx, in, actionID, number); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	write(first, firstView)
	if err := first.Commit(ctx); err != nil {
		t.Fatalf("commit first: %v", err)
	}
	// The second writer proceeds on its now-stale view.
	write(second, secondView)
	if err := second.Commit(ctx); err != nil {
		t.Fatalf("commit second: %v", err)
	}

	actionID, number, _ := actionOf(t, pool, chainID, hash)
	if number != 1 {
		t.Errorf("attempt_number = %d, want 1", number)
	}
	var count, rows int
	if err := pool.QueryRow(ctx, `SELECT attempt_count FROM web3_transaction_actions WHERE id=$1`, actionID).Scan(&count); err != nil {
		t.Fatalf("read attempt_count: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM web3_transactions WHERE action_id=$1`, actionID).Scan(&rows); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rows != 1 {
		t.Fatalf("attempt rows = %d, want 1", rows)
	}
	if count != rows {
		t.Errorf("attempt_count = %d but there is %d attempt row — the counter drifted", count, rows)
	}
}

// finalized_at must actually be written once an attempt reaches the configured
// confirmation depth, rather than shipping permanently NULL.
func TestReachingTheFinalityDepthStampsFinalizedAt(t *testing.T) {
	pool := requireMigratedDatabase(t)
	chainID := isolatedChain(t, pool)
	repo := NewRepository(pool)
	ctx := context.Background()
	hash := hashOf("f")

	if _, err := repo.UpsertSubmittedTransaction(ctx, SubmitTxInput{
		ChainID: chainID, TxHash: hash, FromAddress: ownerA, SenderNonce: nonceOf(170),
	}); err != nil {
		t.Fatalf("submit: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE web3_transactions SET last_checked_at = now() - interval '1 day' WHERE chain_id=$1`, chainID); err != nil {
		t.Fatalf("age the row: %v", err)
	}

	// Shallow: confirmed, but not deep enough to be called final.
	shallow := NewReconciler(repo, &fakeAttemptVerifier{receipt: VerifiedReceipt{
		Status: StatusConfirmed, BlockNumber: 10, Confirmations: 2,
	}}, ReconcilerConfig{BatchSize: 5, BaseRecheck: time.Second, FinalityDepth: 12}, nil)
	if _, err := shallow.RunOnce(ctx); err != nil {
		t.Fatalf("shallow cycle: %v", err)
	}
	var finalizedAt *time.Time
	if err := pool.QueryRow(ctx, `SELECT finalized_at FROM web3_transactions WHERE chain_id=$1 AND tx_hash=$2`,
		chainID, hash).Scan(&finalizedAt); err != nil {
		t.Fatalf("read finalized_at: %v", err)
	}
	if finalizedAt != nil {
		t.Errorf("finalized_at stamped at 2 confirmations with a depth of 12")
	}

	// Deep enough.
	if _, err := pool.Exec(ctx, `UPDATE web3_transactions SET status='pending', last_checked_at = now() - interval '1 day' WHERE chain_id=$1`, chainID); err != nil {
		t.Fatalf("reset: %v", err)
	}
	deep := NewReconciler(repo, &fakeAttemptVerifier{receipt: VerifiedReceipt{
		Status: StatusConfirmed, BlockNumber: 10, Confirmations: 30,
	}}, ReconcilerConfig{BatchSize: 5, BaseRecheck: time.Second, FinalityDepth: 12}, nil)
	if _, err := deep.RunOnce(ctx); err != nil {
		t.Fatalf("deep cycle: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT finalized_at FROM web3_transactions WHERE chain_id=$1 AND tx_hash=$2`,
		chainID, hash).Scan(&finalizedAt); err != nil {
		t.Fatalf("read finalized_at: %v", err)
	}
	if finalizedAt == nil {
		t.Error("finalized_at was not stamped at 30 confirmations with a depth of 12")
	}
}

// The drop threshold must count CONSECUTIVE absences. An RPC outage between two
// absences must reset the streak, or two outages plus one absence would look
// like three agreeing observations.
func TestAnRPCOutageResetsTheConsecutiveMissingStreak(t *testing.T) {
	pool := requireMigratedDatabase(t)
	chainID := isolatedChain(t, pool)
	repo := NewRepository(pool)
	ctx := context.Background()
	hash := hashOf("0")

	if _, err := repo.UpsertSubmittedTransaction(ctx, SubmitTxInput{
		ChainID: chainID, TxHash: hash, FromAddress: ownerA, SenderNonce: nonceOf(180),
	}); err != nil {
		t.Fatalf("submit: %v", err)
	}

	streak := func() (int, int) {
		var missing, checks int
		if err := pool.QueryRow(ctx, `
			SELECT missing_streak, check_attempts FROM web3_transactions WHERE chain_id=$1 AND tx_hash=$2
		`, chainID, hash).Scan(&missing, &checks); err != nil {
			t.Fatalf("read counters: %v", err)
		}
		return missing, checks
	}

	if err := repo.ApplyAttemptOutcome(ctx, AttemptOutcome{ChainID: chainID, TxHash: hash, Missing: true}); err != nil {
		t.Fatalf("miss 1: %v", err)
	}
	if m, _ := streak(); m != 1 {
		t.Fatalf("missing_streak = %d after one miss, want 1", m)
	}
	// An unreachable node is not an absence.
	if err := repo.ApplyAttemptOutcome(ctx, AttemptOutcome{
		ChainID: chainID, TxHash: hash, FailureCode: FailureRPCUnavailable,
	}); err != nil {
		t.Fatalf("outage: %v", err)
	}
	missing, checks := streak()
	if missing != 0 {
		t.Errorf("missing_streak = %d after an outage, want 0 — the streak must be consecutive", missing)
	}
	if checks != 2 {
		t.Errorf("check_attempts = %d, want 2 — backoff must still count every cycle", checks)
	}
}
