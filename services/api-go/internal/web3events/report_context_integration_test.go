package web3events

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/abi"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/executionregistry"
	"github.com/jackc/pgx/v5/pgxpool"
)

// LG-03b report persistence through the REAL migrated write path
// (docs/claude-web3-lg03b-task-2026-10-14.md §6, review F1). Requires
// WEB3TX_TEST_DATABASE_URL on a database migrated by the real chain,
// including 20261014120000_execution_registry_snapshots.

type registryCode struct{}

func (registryCode) CodeAt(_ context.Context, chainID int, address string) (executionregistry.CodeObservation, error) {
	return executionregistry.CodeObservation{ChainID: chainID, Address: address, CodeHash: executionregistry.KeccakHex([]byte("code:" + address)),
		BlockNumber: 7, BlockHash: executionregistry.KeccakHex([]byte("block"))}, nil
}

func buildSnapshot(t *testing.T, deploymentsDir string) executionregistry.Snapshot {
	t.Helper()
	decl, err := os.ReadFile("../../../../packages/shared/execution-registry/declarations.json")
	if err != nil {
		t.Fatal(err)
	}
	s, err := executionregistry.Build(context.Background(), executionregistry.BuildInput{Declarations: decl, DeploymentsDir: deploymentsDir,
		ArtifactsDir: "../executionregistry/testdata/artifacts", Code: registryCode{}, Manifest: executionregistry.BuiltinManifest()})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func contextDB(t *testing.T) (*pgxpool.Pool, *Repository) {
	t.Helper()
	pool := requireMigratedDatabase(t)
	var ok bool
	if err := pool.QueryRow(context.Background(), `SELECT EXISTS (SELECT 1 FROM information_schema.columns
		WHERE table_name = 'web3_transactions' AND column_name = 'execution_context_claim_digest')`).Scan(&ok); err != nil || !ok {
		t.Fatalf("database lacks the LG-03b migration: %v", err)
	}
	return pool, NewRepository(pool).WithExecutionContextColumns()
}

var stagedRevision struct {
	sync.Mutex
	revision int64
}

// aRevision stages one sealed revision so recorded contexts have a real FK.
func aRevision(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	stagedRevision.Lock()
	defer stagedRevision.Unlock()
	if stagedRevision.revision == 0 {
		rev, err := executionregistry.NewRepository(pool).Stage(context.Background(), buildSnapshot(t, "../../../../packages/shared/deployments"), "test", "web3events")
		if err != nil {
			t.Fatal(err)
		}
		stagedRevision.revision = rev
	}
	return stagedRevision.revision
}

func p(s string) *string { return &s }

func boundWrite(state, digest string, revision int64, reason string) *ContextWrite {
	w := &ContextWrite{State: state, Mode: p("bound"), ClaimDigest: p(digest), ClaimedRevision: &revision, Operation: p("trade.approveCollateral"),
		Market: p("ETH-USD"), Binding: p(strings.Repeat("b", 64)), Consumer: p("web-trade"), Manifest: p(strings.Repeat("c", 64))}
	if reason != "" {
		w.Reason = p(reason)
	}
	if state == ContextRecorded {
		w.Revision = &revision
	}
	return w
}

type storedContext struct {
	state, digest, reason string
	revision              int64
}

func contextOf(t *testing.T, pool *pgxpool.Pool, chainID int, hash string) storedContext {
	t.Helper()
	var state, digest, reason *string
	var revision *int64
	if err := pool.QueryRow(context.Background(), `SELECT execution_context_state, execution_context_claim_digest, execution_context_reason, execution_context_revision
		FROM web3_transactions WHERE chain_id = $1 AND tx_hash = $2`, chainID, hash).Scan(&state, &digest, &reason, &revision); err != nil {
		t.Fatalf("read context of %s: %v", hash, err)
	}
	out := storedContext{state: "notRecorded"}
	if state != nil {
		out.state = *state
	}
	if digest != nil {
		out.digest = *digest
	}
	if reason != nil {
		out.reason = *reason
	}
	if revision != nil {
		out.revision = *revision
	}
	return out
}

func submitCtx(t *testing.T, repo *Repository, chainID int, hash string, nonce uint64, w *ContextWrite) error {
	t.Helper()
	_, err := repo.UpsertSubmittedTransaction(context.Background(), SubmitTxInput{ChainID: chainID, TxHash: hash, FromAddress: ownerA,
		SenderNonce: nonceOf(nonce), Context: w})
	return err
}

func receiptCtx(t *testing.T, repo *Repository, chainID int, hash string, nonce uint64, w *ContextWrite) error {
	t.Helper()
	_, err := repo.UpsertTransactionReceipt(context.Background(), ReceiptTxInput{ChainID: chainID, TxHash: hash, FromAddress: ownerA,
		SenderNonce: nonceOf(nonce), Status: StatusConfirmed, BlockNumber: 10, Confirmations: 1, Context: w})
	return err
}

// N1: a null context is final notRecorded.
func TestNullContextIsFinal(t *testing.T) {
	pool, repo := contextDB(t)
	chainID := isolatedChain(t, pool)
	rev := aRevision(t, pool)
	recorded := boundWrite(ContextRecorded, strings.Repeat("1", 64), rev, "")
	ctx := context.Background()

	// A row written before the migration (or by any writer that knows no
	// context columns): inserted directly, every context column null.
	if _, err := pool.Exec(ctx, `INSERT INTO web3_transactions (chain_id, tx_hash, from_address, block_number, status, created_at)
		VALUES ($1, $2, $3, 0, 'pending', now())`, chainID, hashOf("a1"), ownerA); err != nil {
		t.Fatal(err)
	}
	// The compatibility writer omits context columns, including when a caller
	// supplies an already-classified claim.
	old := NewRepository(pool)
	if err := submitCtx(t, old, chainID, hashOf("a2"), 2, recorded); err != nil {
		t.Fatal(err)
	}
	// A first report through the new path without a claim.
	if err := submitCtx(t, repo, chainID, hashOf("a3"), 3, nil); err != nil {
		t.Fatal(err)
	}
	for i, hash := range []string{hashOf("a1"), hashOf("a2"), hashOf("a3")} {
		if err := submitCtx(t, repo, chainID, hash, uint64(10+i), recorded); err != nil {
			t.Fatal(err)
		}
		if err := receiptCtx(t, repo, chainID, hash, uint64(10+i), recorded); err != nil {
			t.Fatal(err)
		}
		if got := contextOf(t, pool, chainID, hash); got.state != "notRecorded" {
			t.Errorf("%s: an existing null row acquired context %+v", hash, got)
		}
	}
	// A first insertion with a claim records it in the same committed row.
	if err := submitCtx(t, repo, chainID, hashOf("a4"), 4, recorded); err != nil {
		t.Fatal(err)
	}
	if got := contextOf(t, pool, chainID, hashOf("a4")); got.state != ContextRecorded || got.revision != rev {
		t.Errorf("first insertion = %+v", got)
	}
}

// waitForLockWaiter blocks until another backend waits on a lock.
func waitForLockWaiter(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		var waiting int
		_ = pool.QueryRow(context.Background(), `SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND datname = current_database()`).Scan(&waiting)
		if waiting > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the second report never waited on the first")
}

// Concurrent inserts of one server-derived action must both complete without
// duplicating identity. The compatibility path must preserve this guarantee
// even when context columns are disabled.
func TestConcurrentFirstReportsOfOneActionBothSucceed(t *testing.T) {
	pool := requireMigratedDatabase(t)
	repo := NewRepository(pool)
	chainID := isolatedChain(t, pool)
	ctx := context.Background()
	first, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recordAttemptTx(ctx, first, attemptWrite{chainID: chainID, txHash: hashOf("r1"), fromAddress: ownerA, senderNonce: nonceOf(9)}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := repo.UpsertSubmittedTransaction(ctx, SubmitTxInput{ChainID: chainID, TxHash: hashOf("r2"), FromAddress: ownerA, SenderNonce: nonceOf(9)})
		done <- err
	}()
	waitForLockWaiter(t, pool)
	if err := first.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("the concurrent same-nonce report failed: %v", err)
	}
	a1, n1, _ := actionOf(t, pool, chainID, hashOf("r1"))
	a2, n2, _ := actionOf(t, pool, chainID, hashOf("r2"))
	if a1 != a2 || n1 == n2 {
		t.Errorf("same-nonce attempts: actions %s/%s numbers %d/%d", a1, a2, n1, n2)
	}
}

// N1: two concurrent first reports — the first committed insert decides.
func TestConcurrentFirstReportsFirstInsertDecides(t *testing.T) {
	pool, repo := contextDB(t)
	rev := aRevision(t, pool)
	recorded := boundWrite(ContextRecorded, strings.Repeat("2", 64), rev, "")
	for _, tc := range []struct {
		name          string
		first, second *ContextWrite
		keys          [2]string
		want          string
		columns       bool
	}{
		{"absent first, bound second (server keys)", nil, recorded, [2]string{"", ""}, "notRecorded", true},
		{"bound first, absent second (server keys)", recorded, nil, [2]string{"", ""}, ContextRecorded, true},
		{"absent first, bound second (two client keys)", nil, recorded, [2]string{"cli:first-key-01", "cli:second-key-02"}, "notRecorded", true},
		{"legacy path with two client keys", nil, nil, [2]string{"cli:first-key-01", "cli:second-key-02"}, "notRecorded", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chainID := isolatedChain(t, pool)
			writer := repo
			if !tc.columns {
				writer = NewRepository(pool)
			}
			hash := hashOf("c")
			ctx := context.Background()
			first, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			in := attemptWrite{chainID: chainID, txHash: hash, fromAddress: ownerA, senderNonce: nonceOf(5), context: tc.first, contextColumns: tc.columns,
				clientActionID: tc.keys[0], clientSupplied: tc.keys[0] != ""}
			if _, err := recordAttemptTx(ctx, first, in); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				_, err := writer.UpsertSubmittedTransaction(ctx, SubmitTxInput{ChainID: chainID, TxHash: hash, FromAddress: ownerA,
					SenderNonce: nonceOf(5), Context: tc.second, ClientActionID: tc.keys[1], ClientSupplied: tc.keys[1] != ""})
				done <- err
			}()
			waitForLockWaiter(t, pool)
			if err := first.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			secondErr := <-done
			if got := contextOf(t, pool, chainID, hash); got.state != tc.want {
				t.Errorf("stored %+v, want %s", got, tc.want)
			}
			var attempts, actions int
			_ = pool.QueryRow(ctx, `SELECT count(*) FROM web3_transactions WHERE chain_id = $1`, chainID).Scan(&attempts)
			_ = pool.QueryRow(ctx, `SELECT count(*) FROM web3_transaction_actions WHERE chain_id = $1`, chainID).Scan(&actions)
			if attempts != 1 {
				t.Errorf("attempts = %d", attempts)
			}
			if tc.keys[0] == "" && (secondErr != nil || actions != 1) {
				t.Errorf("server-keyed race: err %v actions %d", secondErr, actions)
			}
			if tc.keys[0] != "" && (!errors.Is(secondErr, ErrActionAttemptBound) || actions != 1) {
				t.Errorf("different-key race must roll back the losing action: err %v actions %d", secondErr, actions)
			}
			var count int
			if err := pool.QueryRow(ctx, `SELECT sum(attempt_count) FROM web3_transaction_actions WHERE chain_id = $1`, chainID).Scan(&count); err != nil || count != 1 {
				t.Errorf("race consumed extra attempt numbers: count %d err %v", count, err)
			}
		})
	}
}

// N1/N2: only an identical claim resolves claimUnverified; finals never move.
func TestClaimUnverifiedTransitions(t *testing.T) {
	pool, repo := contextDB(t)
	chainID := isolatedChain(t, pool)
	rev := aRevision(t, pool)
	digest, other := strings.Repeat("3", 64), strings.Repeat("4", 64)
	hash := hashOf("d")
	if err := submitCtx(t, repo, chainID, hash, 1, boundWrite(ContextClaimUnverified, digest, rev, "")); err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		name string
		w    *ContextWrite
		want string
	}{
		{"different claim", boundWrite(ContextRecorded, other, rev, ""), ContextClaimUnverified},
		{"no claim", nil, ContextClaimUnverified},
		{"malformed claim", &ContextWrite{State: ContextClaimMismatch, Reason: p("malformed"), Diagnostic: p("market:pattern")}, ContextClaimUnverified},
		{"same claim, registry still unreadable", boundWrite(ContextClaimUnverified, digest, rev, ""), ContextClaimUnverified},
		{"same claim, now readable", boundWrite(ContextRecorded, digest, rev, ""), ContextRecorded},
		{"same claim, later mismatch", boundWrite(ContextClaimMismatch, digest, rev, "bytesMismatch"), ContextRecorded},
	}
	for _, step := range steps {
		before := contextColumnsJSON(t, pool, chainID, hash)
		if err := receiptCtx(t, repo, chainID, hash, 1, step.w); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		if got := contextOf(t, pool, chainID, hash); got.state != step.want {
			t.Errorf("%s: %+v, want %s", step.name, got, step.want)
		}
		if step.want == ContextClaimUnverified && contextColumnsJSON(t, pool, chainID, hash) != before {
			t.Errorf("%s: retained unverified context columns changed", step.name)
		}
	}
	// The reconciler never touches context.
	_ = repo.ApplyAttemptOutcome(context.Background(), AttemptOutcome{ChainID: chainID, TxHash: hash, FailureCode: FailureRPCUnavailable})
	if got := contextOf(t, pool, chainID, hash); got.state != ContextRecorded || got.revision != rev {
		t.Errorf("after reconciliation: %+v", got)
	}
}

// F1: a context-only constraint failure is recovered at the savepoint.
func TestContextStorageFallback(t *testing.T) {
	pool, repo := contextDB(t)
	rev := aRevision(t, pool)
	ctx := context.Background()
	addConstraint := func(t *testing.T, name, check string) {
		t.Helper()
		var original string
		_ = pool.QueryRow(ctx, `SELECT pg_get_constraintdef(oid) FROM pg_constraint
			WHERE conrelid = 'web3_transactions'::regclass AND conname = $1`, name).Scan(&original)
		if original != "" {
			if _, err := pool.Exec(ctx, `ALTER TABLE web3_transactions DROP CONSTRAINT `+name); err != nil {
				t.Fatal(err)
			}
		}
		t.Cleanup(func() {
			_, _ = pool.Exec(context.Background(), `ALTER TABLE web3_transactions DROP CONSTRAINT IF EXISTS `+name)
			if original != "" {
				if _, err := pool.Exec(context.Background(), `ALTER TABLE web3_transactions ADD CONSTRAINT `+name+` `+original); err != nil {
					t.Errorf("restore context constraint: %v", err)
				}
			}
		})
		if _, err := pool.Exec(ctx, fmt.Sprintf(`ALTER TABLE web3_transactions ADD CONSTRAINT %s CHECK (%s) NOT VALID`, name, check)); err != nil {
			t.Fatal(err)
		}
	}
	counts := func(chainID int) (attempts, actions, attemptCount int) {
		_ = pool.QueryRow(ctx, `SELECT count(*) FROM web3_transactions WHERE chain_id = $1`, chainID).Scan(&attempts)
		_ = pool.QueryRow(ctx, `SELECT count(*), coalesce(sum(attempt_count), 0) FROM web3_transaction_actions WHERE chain_id = $1`, chainID).Scan(&actions, &attemptCount)
		return
	}
	t.Run("new submit and receipt", func(t *testing.T) {
		chainID := isolatedChain(t, pool)
		addConstraint(t, "web3_tx_ctx_market_check", fmt.Sprintf("chain_id <> %d OR execution_context_market IS NULL OR length(execution_context_market) < 3", chainID))
		w := boundWrite(ContextRecorded, strings.Repeat("5", 64), rev, "")
		if err := submitCtx(t, repo, chainID, hashOf("e1"), 1, w); err != nil {
			t.Fatalf("submit fell through: %v", err)
		}
		if err := receiptCtx(t, repo, chainID, hashOf("e2"), 2, w); err != nil {
			t.Fatalf("receipt fell through: %v", err)
		}
		for _, hash := range []string{hashOf("e1"), hashOf("e2")} {
			if got := contextOf(t, pool, chainID, hash); got.state != "notRecorded" {
				t.Errorf("%s fallback stored %+v", hash, got)
			}
		}
		if attempts, actions, attemptCount := counts(chainID); attempts != 2 || actions != 2 || attemptCount != 2 {
			t.Errorf("fallback changed identities: attempts %d actions %d numbers %d", attempts, actions, attemptCount)
		}
		var status string
		_ = pool.QueryRow(ctx, `SELECT a.status FROM web3_transaction_actions a JOIN web3_transactions t ON t.action_id = a.id WHERE t.tx_hash = $1 AND t.chain_id = $2`, hashOf("e2"), chainID).Scan(&status)
		if status != StatusConfirmed {
			t.Errorf("the transaction did not continue after the savepoint: action status %q", status)
		}
	})
	t.Run("existing rows keep their context", func(t *testing.T) {
		chainID := isolatedChain(t, pool)
		digest := strings.Repeat("6", 64)
		if err := submitCtx(t, repo, chainID, hashOf("f1"), 1, boundWrite(ContextClaimUnverified, digest, rev, "")); err != nil {
			t.Fatal(err)
		}
		if err := submitCtx(t, repo, chainID, hashOf("f2"), 2, boundWrite(ContextRecorded, strings.Repeat("7", 64), rev, "")); err != nil {
			t.Fatal(err)
		}
		if err := submitCtx(t, repo, chainID, hashOf("f3"), 3, nil); err != nil {
			t.Fatal(err)
		}
		// The upgrade arm writes reason; make that write violate a context constraint.
		addConstraint(t, "web3_tx_ctx_reason_check", fmt.Sprintf("chain_id <> %d OR execution_context_reason IS DISTINCT FROM 'bytesMismatch'", chainID))
		if err := receiptCtx(t, repo, chainID, hashOf("f1"), 1, boundWrite(ContextClaimMismatch, digest, rev, "bytesMismatch")); err != nil {
			t.Fatalf("receipt fell through: %v", err)
		}
		if err := receiptCtx(t, repo, chainID, hashOf("f2"), 2, boundWrite(ContextClaimMismatch, digest, rev, "bytesMismatch")); err != nil {
			t.Fatal(err)
		}
		if err := receiptCtx(t, repo, chainID, hashOf("f3"), 3, boundWrite(ContextClaimMismatch, digest, rev, "bytesMismatch")); err != nil {
			t.Fatal(err)
		}
		for hash, want := range map[string]string{hashOf("f1"): ContextClaimUnverified, hashOf("f2"): ContextRecorded, hashOf("f3"): "notRecorded"} {
			if got := contextOf(t, pool, chainID, hash); got.state != want {
				t.Errorf("%s: %+v, want %s", hash, got, want)
			}
		}
	})
	t.Run("an unrelated constraint keeps today's handling", func(t *testing.T) {
		chainID := isolatedChain(t, pool)
		addConstraint(t, "web3_transactions_test_tx_type", fmt.Sprintf("chain_id <> %d OR tx_type IS DISTINCT FROM 'boom'", chainID))
		boom := "boom"
		_, err := repo.UpsertSubmittedTransaction(ctx, SubmitTxInput{ChainID: chainID, TxHash: hashOf("g1"), FromAddress: ownerA, SenderNonce: nonceOf(1),
			TxType: &boom, Context: boundWrite(ContextRecorded, strings.Repeat("8", 64), rev, "")})
		if err == nil || isContextStorageError(err) {
			t.Fatalf("unrelated failure = %v, want a non-context error", err)
		}
		if attempts, actions, _ := counts(chainID); attempts != 0 || actions != 0 {
			t.Errorf("a failed report left %d attempts / %d actions", attempts, actions)
		}
	})
}

// --- through the HTTP report handler, with the real registry as classifier ---

type inputVerifier struct {
	to, input string
}

func (v inputVerifier) VerifySubmitted(context.Context, int, string, string) (VerifiedTransaction, error) {
	to, input := v.to, v.input
	return VerifiedTransaction{ToAddress: &to, Input: &input, SenderNonce: nonceOf(77)}, nil
}

func (v inputVerifier) VerifyReceipt(context.Context, int, string, string) (VerifiedReceipt, error) {
	to, input := v.to, v.input
	return VerifiedReceipt{ToAddress: &to, Input: &input, Status: StatusConfirmed, BlockNumber: 5, SenderNonce: nonceOf(77)}, nil
}

func approveInput(t *testing.T, spender string) string {
	t.Helper()
	word, err := abi.EncodeAddress(spender)
	if err != nil {
		t.Fatal(err)
	}
	amount := make([]byte, 32)
	amount[31] = 100
	return strings.ToLower(abi.EncodeCall("approve(address,uint256)", word, amount))
}

func TestReportedAfterNextActivationIsRecordedUnderItsRevision(t *testing.T) {
	pool, repo := contextDB(t)
	ctx := context.Background()
	registryRepo := executionregistry.NewRepository(pool)
	n := buildSnapshot(t, "../../../../packages/shared/deployments")
	revN, err := registryRepo.Stage(ctx, n, "test", "N")
	if err != nil {
		t.Fatal(err)
	}
	// N+1 moves PositionManager (case C1): the approve binding of N is no
	// longer current, but a transaction already broadcast under N stays N's.
	dir := t.TempDir()
	files, _ := os.ReadDir("../../../../packages/shared/deployments")
	for _, f := range files {
		raw, _ := os.ReadFile("../../../../packages/shared/deployments/" + f.Name())
		if f.Name() == "all-sepolia.json" {
			raw = []byte(strings.Replace(string(raw), "0x020347BA9cfF1c7635327C7b8fB0e25C0FFd26c5", "0x"+strings.Repeat("f", 40), 1))
		}
		_ = os.WriteFile(dir+"/"+f.Name(), raw, 0o600)
	}
	revN1, err := registryRepo.Stage(ctx, buildSnapshot(t, dir), "test", "N+1")
	if err != nil {
		t.Fatal(err)
	}
	st, _ := registryRepo.Audit(ctx)
	var version int64
	if st.Active != nil {
		version = st.Active.ActivationVersion
	}
	if _, err := registryRepo.Activate(ctx, version, revN, "test", "N", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := registryRepo.Activate(ctx, version+1, revN1, "test", "N+1", nil); err != nil {
		t.Fatal(err)
	}
	var binding executionregistry.Binding
	var usdc, pm string
	for _, b := range n.Bindings {
		if b.ChainID == 11155111 && b.Operation == executionregistry.OpApproveCollateral && b.Market == "ETH-USD" {
			binding = b
		}
	}
	for _, e := range n.Entries {
		if e.ChainID == 11155111 && e.Name == "MockUSDC" {
			usdc = e.Address
		}
		if e.ChainID == 11155111 && e.Name == "PositionManager" {
			pm = e.Address
		}
	}
	svc := NewService(stubReader{}).WithWrites(repo, auth.StrictMiddleware(writeAuth.Verifier), inputVerifier{to: usdc, input: approveInput(t, pm)}).
		WithContextClassifier(executionregistry.NewService(registryRepo, executionregistry.BuiltinManifest()))
	router := Router(svc)
	token := signWeb3(t, ownerA)
	claim := func(revision int64, digest string) map[string]any {
		return map[string]any{"mode": "bound", "registryRevision": fmt.Sprint(revision), "operation": "trade.approveCollateral", "market": "ETH-USD",
			"bindingDigest": digest, "consumer": map[string]any{"id": "web-trade", "manifestDigest": executionregistry.BuiltinManifest().Digest}}
	}
	post := func(hash string, executionContext any) int {
		body, _ := json.Marshal(map[string]any{"chainId": 11155111, "txHash": hash, "executionContext": executionContext})
		req := httptest.NewRequest(http.MethodPost, "/transactions", strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec.Code
	}
	unique := func(seed string) string {
		return "0x" + hex.EncodeToString([]byte(fmt.Sprintf("%-32s", seed+fmt.Sprint(time.Now().UnixNano()))))[:64]
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM web3_transactions WHERE chain_id = 11155111 AND from_address = $1`, ownerA)
		_, _ = pool.Exec(context.Background(), `DELETE FROM web3_transaction_actions WHERE chain_id = 11155111 AND owner_address = $1`, ownerA)
	})
	ordinary := unique("ordinary")
	if code := post(ordinary, claim(revN, binding.BindingDigest)); code != http.StatusCreated {
		t.Fatalf("report = %d", code)
	}
	if got := contextOf(t, pool, 11155111, ordinary); got.state != ContextRecorded || got.revision != revN {
		t.Fatalf("broadcast under N reported after N+1 = %+v, want recorded under %d", got, revN)
	}
	wrongOperation := claim(revN, binding.BindingDigest)
	wrongOperation["operation"] = "trade.openPosition"
	wrongMarket := claim(revN, binding.BindingDigest)
	wrongMarket["market"] = "BTC-USD"
	wrongConsumer := claim(revN, binding.BindingDigest)
	wrongConsumer["consumer"] = map[string]any{"id": "other-client", "manifestDigest": executionregistry.BuiltinManifest().Digest}
	wrongManifest := claim(revN, binding.BindingDigest)
	wrongManifest["consumer"] = map[string]any{"id": "web-trade", "manifestDigest": strings.Repeat("f", 64)}
	for name, tc := range map[string]struct {
		context any
		state   string
		reason  string
	}{
		"wrong operation":   {wrongOperation, ContextClaimMismatch, "unknownBinding"},
		"wrong market":      {wrongMarket, ContextClaimMismatch, "unknownBinding"},
		"wrong consumer":    {wrongConsumer, ContextClaimMismatch, "evidenceIncomplete"},
		"wrong manifest":    {wrongManifest, ContextClaimMismatch, "evidenceIncomplete"},
		"unknown revision":  {claim(999999, binding.BindingDigest), ContextClaimMismatch, "unknownRevision"},
		"digest not in N+1": {claim(revN1, binding.BindingDigest), ContextClaimMismatch, "unknownBinding"},
		"malformed field":   {map[string]any{"mode": "bound", "registryRevision": "01"}, ContextClaimMismatch, "malformed"},
		"unknown field":     {map[string]any{"mode": "bound", "registryRevision": "1", "extra": "secret-value"}, ContextClaimMismatch, "malformed"},
		"over 2 KiB":        {map[string]any{"mode": strings.Repeat("x", 3000)}, ContextClaimMismatch, "malformed"},
		"absent":            {nil, "notRecorded", ""},
	} {
		hash := unique(name)
		if code := post(hash, tc.context); code != http.StatusCreated {
			t.Errorf("%s: report = %d, a context must never refuse it", name, code)
			continue
		}
		got := contextOf(t, pool, 11155111, hash)
		if got.state != tc.state || got.reason != tc.reason {
			t.Errorf("%s: %+v", name, got)
		}
		var diagnostic *string
		_ = pool.QueryRow(ctx, `SELECT execution_context_diagnostic FROM web3_transactions WHERE chain_id = 11155111 AND tx_hash = $1`, hash).Scan(&diagnostic)
		if diagnostic != nil && (strings.Contains(*diagnostic, "secret") || strings.Contains(*diagnostic, "xxx") || len(*diagnostic) > 160) {
			t.Errorf("%s: diagnostic carries raw content: %q", name, *diagnostic)
		}
	}
	// Registry outage at report time: the ordinary 293-character claim stays
	// claimUnverified with its identity, and the same claim resolves it.
	outage := NewService(stubReader{}).WithWrites(repo, auth.StrictMiddleware(writeAuth.Verifier), inputVerifier{to: usdc, input: approveInput(t, pm)}).
		WithContextClassifier(executionregistry.NewService(executionregistry.NewRepository(nil), executionregistry.BuiltinManifest()))
	ordinaryClaim := map[string]any{"mode": "bound", "registryRevision": "1", "operation": "trade.approveCollateral", "market": "ETH-USD",
		"bindingDigest": strings.Repeat("a", 64), "consumer": map[string]any{"id": "web-trade", "manifestDigest": strings.Repeat("b", 64)}}
	canonical := CanonicalClaim(NormalizeClaim(mustJSON(t, ordinaryClaim)).Claim)
	if len(canonical) != 293 {
		t.Fatalf("ordinary claim is %d characters, want 293", len(canonical))
	}
	unresolved := unique("outage")
	body, _ := json.Marshal(map[string]any{"chainId": 11155111, "txHash": unresolved, "executionContext": claim(revN, binding.BindingDigest)})
	req := httptest.NewRequest(http.MethodPost, "/transactions", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	Router(outage).ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("outage report = %d", rec.Code)
	}
	if got := contextOf(t, pool, 11155111, unresolved); got.state != ContextClaimUnverified || got.digest == "" {
		t.Fatalf("outage stored %+v", got)
	}
	req = httptest.NewRequest(http.MethodPost, "/transactions/"+unresolved+"/receipt", strings.NewReader(string(mustJSON(t, map[string]any{"chainId": 11155111,
		"executionContext": map[string]any{"consumer": map[string]any{"manifestDigest": executionregistry.BuiltinManifest().Digest, "id": "web-trade"},
			"bindingDigest": binding.BindingDigest, "market": "ETH-USD", "operation": "trade.approveCollateral", "registryRevision": fmt.Sprint(revN), "mode": "bound"}}))))
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if got := contextOf(t, pool, 11155111, unresolved); rec.Code != http.StatusOK || got.state != ContextRecorded || got.revision != revN {
		t.Fatalf("same claim (keys reordered) after the outage = %d %+v", rec.Code, got)
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
