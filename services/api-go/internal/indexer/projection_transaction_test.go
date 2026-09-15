package indexer

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/rpc"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type projectionTestState struct {
	rawEvent    bool
	token       bool
	complete    bool
	tokenStatus string
}

type projectionTestDB struct {
	state      projectionTestState
	failToken  error
	failCommit error
	lastTx     *projectionTestTx
}

func (db *projectionTestDB) begin(context.Context) (projectionTx, error) {
	tx := &projectionTestTx{
		db:         db,
		staged:     db.state,
		failToken:  db.failToken,
		failCommit: db.failCommit,
	}
	db.lastTx = tx
	return tx, nil
}

type projectionTestTx struct {
	db          *projectionTestDB
	staged      projectionTestState
	failToken   error
	failCommit  error
	committed   bool
	rolledBack  bool
	rawUpserts  int
	tokenWrites int
}

func (tx *projectionTestTx) Exec(_ context.Context, query string, _ ...any) (pgconn.CommandTag, error) {
	switch {
	case strings.Contains(query, "INSERT INTO web3_events"):
		tx.rawUpserts++
		tx.staged.rawEvent = true
		return pgconn.NewCommandTag("INSERT 0 1"), nil
	case strings.Contains(query, "INSERT INTO tokens"):
		tx.tokenWrites++
		if tx.failToken != nil {
			return pgconn.CommandTag{}, tx.failToken
		}
		tx.staged.token = true
		if tx.staged.tokenStatus != "GRADUATED" ||
			!strings.Contains(query, "WHEN tokens.status = 'GRADUATED' THEN tokens.status") {
			tx.staged.tokenStatus = "LAUNCHED"
		}
		return pgconn.NewCommandTag("INSERT 0 1"), nil
	case strings.Contains(query, "UPDATE web3_events"):
		tx.staged.complete = true
		return pgconn.NewCommandTag("UPDATE 1"), nil
	default:
		return pgconn.CommandTag{}, errors.New("projection test received unexpected SQL")
	}
}

func (tx *projectionTestTx) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("projection test received unexpected query")
}

func (tx *projectionTestTx) QueryRow(context.Context, string, ...any) pgx.Row {
	return projectionTestRow{err: errors.New("projection test received unexpected query row")}
}

func (tx *projectionTestTx) Commit(context.Context) error {
	if tx.failCommit != nil {
		return tx.failCommit
	}
	tx.db.state = tx.staged
	tx.committed = true
	return nil
}

func (tx *projectionTestTx) Rollback(context.Context) error {
	if tx.committed {
		return pgx.ErrTxClosed
	}
	tx.rolledBack = true
	return nil
}

type projectionTestRow struct{ err error }

func (r projectionTestRow) Scan(...any) error { return r.err }

type projectionTestWatcher struct{ addresses []string }

func (w *projectionTestWatcher) WatchCurve(address string) {
	w.addresses = append(w.addresses, address)
}

func projectionTokenCreatedEvent() ParsedEvent {
	args := map[string]any{
		"token":        "0x1000000000000000000000000000000000000001",
		"bondingCurve": "0x2000000000000000000000000000000000000002",
		"creator":      "0x3000000000000000000000000000000000000003",
		"symbol":       "ATOMIC",
		"name":         "Atomic Token",
	}
	return ParsedEvent{
		ChainID:         11155111,
		ContractAddress: "0x4000000000000000000000000000000000000004",
		BlockNumber:     101,
		TxHash:          "0xatomic",
		LogIndex:        2,
		Parsed: ParsedIndexedLog{
			EventName:     "TokenCreated",
			RuntimeArgs:   args,
			PersistedArgs: args,
		},
	}
}

func TestDBSink_ProjectionFailureRollsBackRawEvent(t *testing.T) {
	injected := errors.New("token projection failed")
	db := &projectionTestDB{failToken: injected}
	sink := &DBSink{beginProjection: db.begin}

	err := sink.HandleEvent(t.Context(), projectionTokenCreatedEvent())
	if !errors.Is(err, injected) {
		t.Fatalf("HandleEvent err = %v, want injected projection error", err)
	}
	if db.state.rawEvent || db.state.token || db.state.complete {
		t.Fatalf("failed transaction leaked state: %+v", db.state)
	}
	if db.lastTx == nil || !db.lastTx.rolledBack || db.lastTx.committed {
		t.Fatalf("transaction outcome = %+v, want rollback only", db.lastTx)
	}
	if db.lastTx.rawUpserts != 1 || db.lastTx.tokenWrites != 1 {
		t.Fatalf("writes = raw:%d token:%d, want 1 each", db.lastTx.rawUpserts, db.lastTx.tokenWrites)
	}
}

func TestDBSink_ReplayRepairsRawOnlyEvent(t *testing.T) {
	// A raw row from an older partial write is not a completion marker. Replay
	// must still execute the missing business projection in the same commit.
	db := &projectionTestDB{state: projectionTestState{rawEvent: true}}
	sink := &DBSink{beginProjection: db.begin}

	if err := sink.HandleEvent(t.Context(), projectionTokenCreatedEvent()); err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	if !db.state.rawEvent || !db.state.token || !db.state.complete {
		t.Fatalf("repaired state = %+v, want raw event and token", db.state)
	}
	if db.lastTx == nil || !db.lastTx.committed {
		t.Fatalf("transaction did not commit: %+v", db.lastTx)
	}
}

func TestDBSink_ReplayedTokenCreatedCannotDowngradeGraduatedToken(t *testing.T) {
	db := &projectionTestDB{state: projectionTestState{
		rawEvent: true, token: true, tokenStatus: "GRADUATED",
	}}
	sink := &DBSink{beginProjection: db.begin}

	if err := sink.HandleEvent(t.Context(), projectionTokenCreatedEvent()); err != nil {
		t.Fatalf("HandleEvent: %v", err)
	}
	if db.state.tokenStatus != "GRADUATED" {
		t.Fatalf("replayed TokenCreated changed status to %q", db.state.tokenStatus)
	}
	if !db.state.complete {
		t.Fatal("status-preserving replay did not complete")
	}
}

func TestDBSink_PostCommitEffectsWaitForCommit(t *testing.T) {
	commitFailure := errors.New("commit failed")
	db := &projectionTestDB{failCommit: commitFailure}
	watcher := &projectionTestWatcher{}
	sink := &DBSink{beginProjection: db.begin, watcher: watcher}

	if err := sink.HandleEvent(t.Context(), projectionTokenCreatedEvent()); !errors.Is(err, commitFailure) {
		t.Fatalf("HandleEvent err = %v, want commit failure", err)
	}
	if len(watcher.addresses) != 0 {
		t.Fatalf("watcher ran before commit: %v", watcher.addresses)
	}

	db.failCommit = nil
	if err := sink.HandleEvent(t.Context(), projectionTokenCreatedEvent()); err != nil {
		t.Fatalf("retry HandleEvent: %v", err)
	}
	if len(watcher.addresses) != 1 || watcher.addresses[0] != "0x2000000000000000000000000000000000000002" {
		t.Fatalf("post-commit watcher calls = %v", watcher.addresses)
	}
}

func TestBackfill_DBSinkCommitFailureDoesNotAdvanceCheckpoint(t *testing.T) {
	db := &projectionTestDB{failCommit: errors.New("commit failed")}
	sink := &DBSink{beginProjection: db.begin}
	reader := &fakeReader{
		head: 50,
		perRange: func(from, _ uint64) ([]rpc.Log, error) {
			return []rpc.Log{transferLog(from)}, nil
		},
	}
	checkpoints := &fakeCheckpoints{}

	processed, err := NewBackfiller(Config{BatchSize: 100}, reader, checkpoints, sink).Run(t.Context(), "0xc")
	if err == nil {
		t.Fatal("expected projection commit failure")
	}
	if processed != 0 {
		t.Fatalf("processed = %d, want 0", processed)
	}
	if len(checkpoints.saved) != 0 {
		t.Fatalf("checkpoint advanced after failed commit: %v", checkpoints.saved)
	}
	if db.state.rawEvent {
		t.Fatal("raw event became durable despite failed commit")
	}
}
