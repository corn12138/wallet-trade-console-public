package indexer

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type repairSource struct {
	row             []any
	returned        bool
	failureRecorded bool
}

type millisecondRepairSource struct {
	row         []any
	lastAttempt *time.Time
	queries     int
}

type orderedRepairSource struct {
	rows             [][]any
	failuresRecorded int
}

func (s *orderedRepairSource) Exec(_ context.Context, query string, _ ...any) (pgconn.CommandTag, error) {
	if !strings.Contains(query, "UPDATE web3_events") {
		return pgconn.CommandTag{}, errors.New("ordered repair source received unexpected Exec")
	}
	s.failuresRecorded++
	return pgconn.NewCommandTag("UPDATE 1"), nil
}

func (s *orderedRepairSource) Query(_ context.Context, _ string, args ...any) (pgx.Rows, error) {
	cursorSet := args[1].(bool)
	cursor := projectionRepairCursor{
		set: cursorSet, chainID: args[2].(int), blockNumber: args[3].(int64),
		logIndex: args[4].(int), rawEventID: args[5].(int64),
	}
	limit := args[6].(int)
	out := make([][]any, 0, limit)
	for _, row := range s.rows {
		candidate := projectionRepairCursor{
			set: true, chainID: row[1].(int), blockNumber: row[6].(int64),
			logIndex: row[5].(int), rawEventID: row[0].(int64),
		}
		if cursor.set && !repairCursorAfter(candidate, cursor) {
			continue
		}
		out = append(out, row)
		if len(out) == limit {
			break
		}
	}
	return &boundaryRows{values: out}, nil
}

func (s *orderedRepairSource) QueryRow(context.Context, string, ...any) pgx.Row {
	return boundaryRow{err: errors.New("ordered repair source received unexpected QueryRow")}
}

func repairCursorAfter(candidate, cursor projectionRepairCursor) bool {
	if candidate.chainID != cursor.chainID {
		return candidate.chainID > cursor.chainID
	}
	if candidate.blockNumber != cursor.blockNumber {
		return candidate.blockNumber > cursor.blockNumber
	}
	if candidate.logIndex != cursor.logIndex {
		return candidate.logIndex > cursor.logIndex
	}
	return candidate.rawEventID > cursor.rawEventID
}

func (s *millisecondRepairSource) Exec(_ context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	if !strings.Contains(query, "UPDATE web3_events") {
		return pgconn.CommandTag{}, errors.New("millisecond repair source received unexpected Exec")
	}
	attemptedAt, ok := args[2].(time.Time)
	if !ok {
		return pgconn.CommandTag{}, errors.New("millisecond repair source missing attemptedAt")
	}
	stored := attemptedAt.Truncate(time.Millisecond)
	s.lastAttempt = &stored
	return pgconn.NewCommandTag("UPDATE 1"), nil
}

func (s *millisecondRepairSource) Query(_ context.Context, _ string, args ...any) (pgx.Rows, error) {
	s.queries++
	cursorSet, ok := args[1].(bool)
	if !ok {
		return nil, errors.New("millisecond repair source missing repair cursor")
	}
	if s.queries > 2 {
		return nil, errors.New("same poison row selected repeatedly in one repair pass")
	}
	if !cursorSet {
		return &boundaryRows{values: [][]any{s.row}}, nil
	}
	return &boundaryRows{}, nil
}

func (s *millisecondRepairSource) QueryRow(context.Context, string, ...any) pgx.Row {
	return boundaryRow{err: errors.New("millisecond repair source received unexpected QueryRow")}
}

func (s *repairSource) Exec(_ context.Context, query string, _ ...any) (pgconn.CommandTag, error) {
	if !strings.Contains(query, "UPDATE web3_events") {
		return pgconn.CommandTag{}, errors.New("repair source received unexpected Exec")
	}
	s.failureRecorded = true
	return pgconn.NewCommandTag("UPDATE 1"), nil
}

func (s *repairSource) Query(context.Context, string, ...any) (pgx.Rows, error) {
	if s.returned || s.row == nil {
		return &boundaryRows{}, nil
	}
	s.returned = true
	return &boundaryRows{values: [][]any{s.row}}, nil
}

func (s *repairSource) QueryRow(context.Context, string, ...any) pgx.Row {
	return boundaryRow{err: errors.New("repair source received unexpected QueryRow")}
}

func TestRepairIncompleteRestoresLegacyRawEventAndMarksCompletion(t *testing.T) {
	actor := "0x3000000000000000000000000000000000000003"
	args := []byte(`{
		"token":"0x1000000000000000000000000000000000000001",
		"bondingCurve":"0x2000000000000000000000000000000000000002",
		"creator":"0x3000000000000000000000000000000000000003",
		"symbol":"FIX","name":"Repair"
	}`)
	source := &repairSource{row: []any{
		int64(7), 11155111, "0x4000000000000000000000000000000000000004",
		"TokenCreated", "0xrepair", 2, int64(101), &actor, args,
	}}
	db := &projectionTestDB{state: projectionTestState{rawEvent: true}}
	sink := &DBSink{beginProjection: db.begin, repairStore: source}

	report, err := sink.RepairIncomplete(t.Context(), 10)
	if err != nil {
		t.Fatalf("RepairIncomplete: %v", err)
	}
	if report != (ProjectionRepairReport{Attempted: 1, Repaired: 1}) {
		t.Fatalf("report = %+v", report)
	}
	if !db.state.rawEvent || !db.state.token || !db.state.complete {
		t.Fatalf("repaired transaction state = %+v", db.state)
	}
	if source.failureRecorded {
		t.Fatal("successful repair must not write a failure marker")
	}
}

func TestRepairIncompleteFailureRemainsIncompleteAndDurable(t *testing.T) {
	args := []byte(`{"symbol":"MISSING_DEPENDENCIES"}`)
	source := &repairSource{row: []any{
		int64(8), 11155111, "0x4000000000000000000000000000000000000004",
		"TokenCreated", "0xfail", 3, int64(102), nil, args,
	}}
	db := &projectionTestDB{state: projectionTestState{rawEvent: true}}
	sink := &DBSink{beginProjection: db.begin, repairStore: source}

	report, err := sink.RepairIncomplete(t.Context(), 10)
	if !errors.Is(err, ErrProjectionRepairIncomplete) {
		t.Fatalf("RepairIncomplete err = %v", err)
	}
	if report != (ProjectionRepairReport{Attempted: 1, Failed: 1}) {
		t.Fatalf("report = %+v", report)
	}
	if !source.failureRecorded {
		t.Fatal("failed repair must persist its retry evidence")
	}
	if db.state.token || db.state.complete {
		t.Fatalf("failed repair leaked completion: %+v", db.state)
	}
}

func TestRepairIncompleteUsesDatabaseTimestampPrecisionForOnePassBoundary(t *testing.T) {
	args := []byte(`{"symbol":"MISSING_DEPENDENCIES"}`)
	source := &millisecondRepairSource{row: []any{
		int64(9), 11155111, "0x4000000000000000000000000000000000000004",
		"TokenCreated", "0xprecision", 4, int64(103), nil, args,
	}}
	db := &projectionTestDB{state: projectionTestState{rawEvent: true}}
	sink := &DBSink{beginProjection: db.begin, repairStore: source}
	startedAt := time.Unix(1_700_000_000, 123_456_789)

	report, err := sink.repairIncompleteAt(t.Context(), 10, startedAt)
	if !errors.Is(err, ErrProjectionRepairIncomplete) {
		t.Fatalf("repairIncompleteAt err = %v", err)
	}
	if report != (ProjectionRepairReport{Attempted: 1, Failed: 1}) {
		t.Fatalf("report = %+v", report)
	}
	if source.queries != 2 {
		t.Fatalf("batch queries = %d, want one data batch plus one empty batch", source.queries)
	}
	if source.lastAttempt == nil || !source.lastAttempt.Equal(startedAt.UTC().Truncate(time.Millisecond)) {
		t.Fatalf("stored attempt boundary = %v", source.lastAttempt)
	}
}

func TestRepairIncompletePoisonRowDoesNotStarveLaterProjection(t *testing.T) {
	actor := "0x3000000000000000000000000000000000000003"
	badArgs := []byte(`{"symbol":"MISSING_DEPENDENCIES"}`)
	goodArgs := []byte(`{
		"token":"0x1000000000000000000000000000000000000001",
		"bondingCurve":"0x2000000000000000000000000000000000000002",
		"creator":"0x3000000000000000000000000000000000000003",
		"symbol":"NEXT","name":"Later Event"
	}`)
	source := &orderedRepairSource{rows: [][]any{
		{int64(10), 11155111, "0x4000000000000000000000000000000000000004",
			"TokenCreated", "0xpoison", 1, int64(100), nil, badArgs},
		{int64(11), 11155111, "0x4000000000000000000000000000000000000004",
			"TokenCreated", "0xlater", 2, int64(101), &actor, goodArgs},
	}}
	db := &projectionTestDB{state: projectionTestState{rawEvent: true}}
	sink := &DBSink{beginProjection: db.begin, repairStore: source}

	report, err := sink.RepairIncomplete(t.Context(), 1)
	if !errors.Is(err, ErrProjectionRepairIncomplete) {
		t.Fatalf("RepairIncomplete err = %v", err)
	}
	if report != (ProjectionRepairReport{Attempted: 2, Repaired: 1, Failed: 1}) {
		t.Fatalf("report = %+v", report)
	}
	if source.failuresRecorded != 1 {
		t.Fatalf("failure journal writes = %d", source.failuresRecorded)
	}
	if !db.state.token || !db.state.complete {
		t.Fatalf("later projection was starved: %+v", db.state)
	}
}

func TestRestoreProjectionEventRehydratesTypedRuntimeArgs(t *testing.T) {
	persisted := map[string]any{
		"key":             "0x" + strings.Repeat("ab", 32),
		"account":         perpAccount,
		"indexToken":      perpIndexToken,
		"collateralToken": perpCollateral,
		"collateralDelta": "10",
		"sizeDelta":       "100",
		"isLong":          true,
		"price":           "3000",
		"fee":             "1",
	}
	raw, err := json.Marshal(persisted)
	if err != nil {
		t.Fatal(err)
	}
	event, err := restoreProjectionEvent(11155111, "0xperp", "IncreasePosition",
		"0xtx", 1, 10, nil, raw)
	if err != nil {
		t.Fatalf("restoreProjectionEvent: %v", err)
	}
	if got := argBigInt(event.Parsed.RuntimeArgs["sizeDelta"]); got == nil || got.Cmp(big.NewInt(100)) != 0 {
		t.Fatalf("restored sizeDelta = %v", event.Parsed.RuntimeArgs["sizeDelta"])
	}
	if got, ok := event.Parsed.RuntimeArgs["isLong"].(bool); !ok || !got {
		t.Fatalf("restored isLong = %v", event.Parsed.RuntimeArgs["isLong"])
	}
}
