package web3events

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Workbench reads against the real migrated schema, seeded through the real
// write path. WEB3TX_TEST_DATABASE_URL unset means UNPROVEN, not passing.

func submitAction(t *testing.T, repo *Repository, chainID int, owner, hash string, nonce uint64) string {
	t.Helper()
	if _, err := repo.UpsertSubmittedTransaction(context.Background(), SubmitTxInput{
		ChainID: chainID, TxHash: hash, FromAddress: owner, SenderNonce: nonceOf(nonce),
	}); err != nil {
		t.Fatalf("submit %s: %v", hash, err)
	}
	id, _, _ := actionOf(t, repo.pool, chainID, hash)
	return id
}

func hashN(prefix string, n int) string {
	return fmt.Sprintf("0x%s%062x", prefix, n)
}

func pageAll(t *testing.T, repo *Repository, owner string, chainID int, status string, limit int) ([]string, bool) {
	t.Helper()
	var (
		ids     []string
		cursor  *ActionCursorState
		updates bool
	)
	for pages := 0; pages < 50; pages++ {
		page, err := repo.ListActions(context.Background(), ActionListQuery{Owner: owner, ChainID: chainID, Status: status, Limit: limit, Cursor: cursor})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		updates = updates || page.HasUpdates
		for _, row := range page.Rows {
			ids = append(ids, row.ID)
		}
		if !page.HasMore {
			return ids, updates
		}
		state := ActionCursorState{AsOf: page.AsOf, HighWater: ActionKey{page.Rows[0].CreatedAt, page.Rows[0].ID}}
		if cursor != nil {
			state.AsOf, state.HighWater = cursor.AsOf, cursor.HighWater
		}
		last := page.Rows[len(page.Rows)-1]
		state.After = ActionKey{last.CreatedAt, last.ID}
		// Through the wire encoding, exactly as a client would continue.
		decoded, err := DecodeActionCursor(EncodeActionCursor(owner, chainID, status, state), owner, chainID, status)
		if err != nil {
			t.Fatalf("cursor round trip: %v", err)
		}
		cursor = &decoded
	}
	t.Fatal("paging did not terminate")
	return nil, false
}

func expectedOrder(t *testing.T, pool *pgxpool.Pool, owner string, chainID int) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT id FROM web3_transaction_actions WHERE owner_address=$1 AND chain_id=$2 ORDER BY created_at DESC, id DESC
	`, owner, chainID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		ids = append(ids, id)
	}
	return ids
}

func TestWorkbenchIsolatesOwnersAndChains(t *testing.T) {
	pool := requireMigratedDatabase(t)
	chainX, chainY := isolatedChain(t, pool), isolatedChain(t, pool)
	repo := NewRepository(pool)
	ctx := context.Background()

	aX := submitAction(t, repo, chainX, ownerA, hashN("a", 1), 1)
	bX := submitAction(t, repo, chainX, ownerB, hashN("b", 1), 1)
	aY := submitAction(t, repo, chainY, ownerA, hashN("a", 2), 1)

	page, err := repo.ListActions(ctx, ActionListQuery{Owner: ownerA, ChainID: chainX, Limit: 20})
	if err != nil || len(page.Rows) != 1 || page.Rows[0].ID != aX {
		t.Fatalf("A on X = %+v, %v; want only %s", page.Rows, err, aX)
	}
	for name, probe := range map[string]struct {
		owner string
		chain int
		id    string
	}{
		"B reads A's action":           {ownerB, chainX, aX},
		"A reads B's action":           {ownerA, chainX, bX},
		"A reads its X action on Y":    {ownerA, chainY, aX},
		"A reads its Y action on X":    {ownerA, chainX, aY},
		"A reads a nonexistent action": {ownerA, chainX, "00000000-0000-4000-8000-000000000000"},
	} {
		if _, found, _, err := repo.GetAction(ctx, probe.owner, probe.chain, probe.id); err != nil || found {
			t.Errorf("%s: found=%v err=%v, want not found", name, found, err)
		}
		if page, found, err := repo.ListAttempts(ctx, AttemptListQuery{Owner: probe.owner, ChainID: probe.chain, ActionID: probe.id, Limit: 20}); err != nil || found || len(page.Rows) != 0 {
			t.Errorf("%s attempts: found=%v rows=%d err=%v, want not found", name, found, len(page.Rows), err)
		}
	}
	if _, found, _, err := repo.GetAction(ctx, ownerA, chainX, aX); err != nil || !found {
		t.Fatalf("own action: found=%v err=%v", found, err)
	}
}

func TestWorkbenchPagesEqualTimestampsWithoutDuplicatesOrGaps(t *testing.T) {
	pool := requireMigratedDatabase(t)
	chainID := isolatedChain(t, pool)
	repo := NewRepository(pool)
	ctx := context.Background()
	for i := 0; i < 9; i++ {
		submitAction(t, repo, chainID, ownerA, hashN("c", i), uint64(i))
	}
	// Seven of the nine share one created_at, so only the id breaks the tie.
	if _, err := pool.Exec(ctx, `
		UPDATE web3_transaction_actions SET created_at = TIMESTAMP '2026-10-13 08:00:00.123'
		WHERE chain_id = $1 AND id IN (SELECT id FROM web3_transaction_actions WHERE chain_id = $1 ORDER BY id LIMIT 7)
	`, chainID); err != nil {
		t.Fatal(err)
	}
	want := expectedOrder(t, pool, ownerA, chainID)
	for _, limit := range []int{1, 2, 4, 9, 20} {
		got, updates := pageAll(t, repo, ownerA, chainID, "", limit)
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("limit %d:\n got  %v\n want %v", limit, got, want)
		}
		if updates {
			t.Errorf("limit %d: a fixed data set reported updates", limit)
		}
	}
}

func TestWorkbenchPagingDuringChangesHintsARefreshWithoutDuplicates(t *testing.T) {
	pool := requireMigratedDatabase(t)
	chainID := isolatedChain(t, pool)
	repo := NewRepository(pool)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		submitAction(t, repo, chainID, ownerA, hashN("d", i), uint64(i))
	}
	if _, err := pool.Exec(ctx, `
		UPDATE web3_transaction_actions SET created_at = TIMESTAMP '2026-10-13 08:00:00.000', updated_at = TIMESTAMP '2026-10-13 08:00:00.000'
		WHERE chain_id = $1
	`, chainID); err != nil {
		t.Fatal(err)
	}
	first, err := repo.ListActions(ctx, ActionListQuery{Owner: ownerA, ChainID: chainID, Limit: 2})
	if err != nil || len(first.Rows) != 2 || !first.HasMore {
		t.Fatalf("first page = %+v, %v", first, err)
	}
	cursor := ActionCursorState{AsOf: first.AsOf, HighWater: ActionKey{first.Rows[0].CreatedAt, first.Rows[0].ID}, After: ActionKey{first.Rows[1].CreatedAt, first.Rows[1].ID}}

	unchanged, err := repo.ListActions(ctx, ActionListQuery{Owner: ownerA, ChainID: chainID, Limit: 10, Cursor: &cursor})
	if err != nil || unchanged.HasUpdates || len(unchanged.Rows) != 3 {
		t.Fatalf("unchanged continuation = %d rows, updates=%v, %v", len(unchanged.Rows), unchanged.HasUpdates, err)
	}
	// An unseen action changes: the continuation still serves it once, and
	// hints that the first page is stale.
	time.Sleep(5 * time.Millisecond)
	if _, err := pool.Exec(ctx, `UPDATE web3_transaction_actions SET updated_at = now() WHERE id = $1`, unchanged.Rows[2].ID); err != nil {
		t.Fatal(err)
	}
	if changed, err := repo.ListActions(ctx, ActionListQuery{Owner: ownerA, ChainID: chainID, Limit: 10, Cursor: &cursor}); err != nil || !changed.HasUpdates {
		t.Errorf("a changed row must hint a refresh: updates=%v err=%v", changed.HasUpdates, err)
	}
	// A new action arrives above the high-water mark: not page content.
	newcomer := submitAction(t, repo, chainID, ownerA, hashN("d", 99), 99)
	if _, err := pool.Exec(ctx, `UPDATE web3_transaction_actions SET created_at = now() WHERE id = $1`, newcomer); err != nil {
		t.Fatal(err)
	}
	second, err := repo.ListActions(ctx, ActionListQuery{Owner: ownerA, ChainID: chainID, Limit: 10, Cursor: &cursor})
	if err != nil {
		t.Fatal(err)
	}
	if !second.HasUpdates {
		t.Error("a newer action and a changed row must hint a refresh")
	}
	seen := map[string]bool{first.Rows[0].ID: true, first.Rows[1].ID: true}
	for _, row := range second.Rows {
		if seen[row.ID] {
			t.Errorf("duplicate across pages: %s", row.ID)
		}
		if row.ID == newcomer {
			t.Error("a row above the high-water mark leaked into a continuation page")
		}
		seen[row.ID] = true
	}
	if len(seen) != 5 {
		t.Errorf("pages covered %d of the 5 original actions", len(seen))
	}

	// A status filter is part of the cursor's binding and of the query.
	none, err := repo.ListActions(ctx, ActionListQuery{Owner: ownerA, ChainID: chainID, Status: StatusConfirmed, Limit: 10})
	if err != nil || len(none.Rows) != 0 {
		t.Errorf("confirmed filter = %d rows, %v; want 0", len(none.Rows), err)
	}
}

func TestWorkbenchKeepsReplacementAttemptsUnderOneActionInOrder(t *testing.T) {
	pool := requireMigratedDatabase(t)
	chainID := isolatedChain(t, pool)
	repo := NewRepository(pool)
	ctx := context.Background()
	original, speedUp := hashOf("7"), hashOf("8")
	actionID := submitAction(t, repo, chainID, ownerA, original, 42)
	if again := submitAction(t, repo, chainID, ownerA, speedUp, 42); again != actionID {
		t.Fatalf("speed-up landed in action %s, want %s", again, actionID)
	}
	if _, err := repo.UpsertTransactionReceipt(ctx, ReceiptTxInput{ChainID: chainID, TxHash: speedUp, FromAddress: ownerA,
		SenderNonce: nonceOf(42), Status: StatusConfirmed, BlockNumber: 500, Confirmations: 3}); err != nil {
		t.Fatal(err)
	}
	var views []AttemptView
	var after *AttemptKey
	for i := 0; i < 3; i++ {
		page, found, err := repo.ListAttempts(ctx, AttemptListQuery{Owner: ownerA, ChainID: chainID, ActionID: actionID, Limit: 1, After: after})
		if err != nil || !found {
			t.Fatalf("attempts: found=%v err=%v", found, err)
		}
		for _, row := range page.Rows {
			views = append(views, NewAttemptView(row))
		}
		if !page.HasMore {
			break
		}
		last := page.Rows[len(page.Rows)-1]
		decoded, err := DecodeAttemptCursor(EncodeAttemptCursor(ownerA, chainID, actionID, AttemptKey{last.AttemptNumber, last.ID}), ownerA, chainID, actionID)
		if err != nil {
			t.Fatal(err)
		}
		after = &decoded
	}
	if len(views) != 2 || views[0].AttemptNumber != 1 || views[1].AttemptNumber != 2 {
		t.Fatalf("attempts = %+v", views)
	}
	if views[0].TxHash != original || views[0].ExecutionStatus != "replaced" || views[0].ReplacedByHash == nil || *views[0].ReplacedByHash != speedUp {
		t.Errorf("original attempt = %+v", views[0])
	}
	if views[1].TxHash != speedUp || views[1].ExecutionStatus != "succeeded" || views[1].Finality != "included" || views[1].Canonicality != "unknown" ||
		views[1].Observation.BlockNumber == nil || *views[1].Observation.BlockNumber != "500" || views[1].SenderNonce == nil || *views[1].SenderNonce != "42" {
		t.Errorf("speed-up attempt = %+v", views[1])
	}
	action, found, _, err := repo.GetAction(ctx, ownerA, chainID, actionID)
	if err != nil || !found || NewActionView(action).WorkflowStatus != "completed" || action.AttemptCount != 2 {
		t.Errorf("action = %+v found=%v err=%v", action, found, err)
	}
}

func TestWorkbenchPreservesAnUnknownOutcome(t *testing.T) {
	pool := requireMigratedDatabase(t)
	chainID := isolatedChain(t, pool)
	repo := NewRepository(pool)
	ctx := context.Background()
	hash := hashOf("9")
	actionID := submitAction(t, repo, chainID, ownerA, hash, 3)
	detail := SanitizeFailureDetail(FailureRPCUnavailable, nil)
	if err := repo.ApplyAttemptOutcome(ctx, AttemptOutcome{ChainID: chainID, TxHash: hash, FailureCode: FailureRPCUnavailable, FailureDetail: detail}); err != nil {
		t.Fatal(err)
	}
	page, found, err := repo.ListAttempts(ctx, AttemptListQuery{Owner: ownerA, ChainID: chainID, ActionID: actionID, Limit: 20})
	if err != nil || !found || len(page.Rows) != 1 {
		t.Fatalf("attempts = %+v found=%v err=%v", page.Rows, found, err)
	}
	view := NewAttemptView(page.Rows[0])
	if view.ExecutionStatus != "pending" || view.FailureCode == nil || *view.FailureCode != FailureRPCUnavailable ||
		view.Observation.BlockNumber != nil || view.Observation.LastCheckedAt == nil || view.Finality != "unknown" {
		t.Errorf("an unreachable node must leave a pending, unknown-block attempt: %+v", view)
	}
}
