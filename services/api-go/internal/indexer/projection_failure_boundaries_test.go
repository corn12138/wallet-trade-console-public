package indexer

import (
	"context"
	"errors"
	"math/big"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type boundaryProjectionState struct {
	raw, ledger, derived, complete bool
}

type boundaryProjectionDB struct {
	state   boundaryProjectionState
	failSQL string
	failErr error
	lastTx  *boundaryProjectionTx
}

func (db *boundaryProjectionDB) begin(context.Context) (projectionTx, error) {
	tx := &boundaryProjectionTx{db: db, staged: db.state}
	db.lastTx = tx
	return tx, nil
}

type boundaryProjectionTx struct {
	db                          *boundaryProjectionDB
	staged                      boundaryProjectionState
	rolledBack, committed       bool
	ledgerWrites, derivedWrites int
}

func (tx *boundaryProjectionTx) Exec(_ context.Context, query string, _ ...any) (pgconn.CommandTag, error) {
	switch {
	case strings.Contains(query, "INSERT INTO web3_events"):
		tx.staged.raw = true
	case strings.Contains(query, "UPDATE token_trades") && strings.Contains(query, "log_index IS NULL"):
		return pgconn.NewCommandTag("UPDATE 0"), nil
	case strings.Contains(query, "INSERT INTO token_trades"),
		strings.Contains(query, "INSERT INTO perp_trades"),
		strings.Contains(query, "INSERT INTO staking_actions"):
		tx.ledgerWrites++
		tx.staged.ledger = true
	case strings.Contains(query, "INSERT INTO token_holders"),
		strings.Contains(query, "INSERT INTO perp_positions"),
		strings.Contains(query, "INSERT INTO user_stakes"):
		tx.derivedWrites++
		if strings.Contains(query, tx.db.failSQL) {
			return pgconn.CommandTag{}, tx.db.failErr
		}
		tx.staged.derived = true
	case strings.Contains(query, "UPDATE web3_events"):
		tx.staged.complete = true
	}
	return pgconn.NewCommandTag("UPDATE 1"), nil
}

func (tx *boundaryProjectionTx) Query(_ context.Context, query string, _ ...any) (pgx.Rows, error) {
	now := time.Unix(1_700_000_000, 0).UTC()
	switch {
	case strings.Contains(query, "SELECT id FROM staking_pools"):
		return &boundaryRows{values: [][]any{{"pool-1"}}}, nil
	case strings.Contains(query, "FROM perp_trades"):
		return &boundaryRows{values: [][]any{{
			"perp-trade-1", "INCREASE", "100", "10", "3000", "1", nil,
			"0xperp", 3, int64(100), now,
		}}}, nil
	case strings.Contains(query, "FROM staking_actions"):
		return &boundaryRows{values: [][]any{{
			"STAKE", "1000000000000000000", "0xstake", 4, int64(101), now,
		}}}, nil
	default:
		return nil, errors.New("boundary test received unexpected query")
	}
}

func (tx *boundaryProjectionTx) QueryRow(_ context.Context, query string, _ ...any) pgx.Row {
	if strings.Contains(query, "SELECT id, address FROM tokens") {
		address := "0x1000000000000000000000000000000000000001"
		return boundaryRow{values: []any{"token-1", &address}}
	}
	return boundaryRow{err: errors.New("boundary test received unexpected query row")}
}

func (tx *boundaryProjectionTx) Commit(context.Context) error {
	tx.db.state = tx.staged
	tx.committed = true
	return nil
}

func (tx *boundaryProjectionTx) Rollback(context.Context) error {
	if tx.committed {
		return pgx.ErrTxClosed
	}
	tx.rolledBack = true
	return nil
}

type boundaryRow struct {
	values []any
	err    error
}

func (r boundaryRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	return assignBoundaryValues(dest, r.values)
}

type boundaryRows struct {
	values [][]any
	index  int
	closed bool
	err    error
}

func (r *boundaryRows) Close()                                       { r.closed = true }
func (r *boundaryRows) Err() error                                   { return r.err }
func (r *boundaryRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *boundaryRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *boundaryRows) Values() ([]any, error)                       { return r.values[r.index-1], nil }
func (r *boundaryRows) RawValues() [][]byte                          { return nil }
func (r *boundaryRows) Conn() *pgx.Conn                              { return nil }

func (r *boundaryRows) Next() bool {
	if r.closed || r.index >= len(r.values) {
		r.closed = true
		return false
	}
	r.index++
	return true
}

func (r *boundaryRows) Scan(dest ...any) error {
	if r.index == 0 || r.index > len(r.values) {
		return errors.New("boundary rows Scan called without Next")
	}
	return assignBoundaryValues(dest, r.values[r.index-1])
}

func assignBoundaryValues(dest, values []any) error {
	if len(dest) != len(values) {
		return errors.New("boundary row destination count mismatch")
	}
	for i := range dest {
		target := reflect.ValueOf(dest[i])
		if target.Kind() != reflect.Pointer || target.IsNil() {
			return errors.New("boundary row destination is not a pointer")
		}
		value := reflect.ValueOf(values[i])
		if !value.IsValid() {
			target.Elem().Set(reflect.Zero(target.Elem().Type()))
			continue
		}
		if value.Type().AssignableTo(target.Elem().Type()) {
			target.Elem().Set(value)
			continue
		}
		if value.Type().ConvertibleTo(target.Elem().Type()) {
			target.Elem().Set(value.Convert(target.Elem().Type()))
			continue
		}
		return errors.New("boundary row value type mismatch")
	}
	return nil
}

type boundarySymbolResolver struct{}

func (boundarySymbolResolver) SymbolForIndexToken(int, string) string { return "ETH-USD" }

func TestProjectionFailureBoundariesRollbackLedgerAndRawEvent(t *testing.T) {
	tradeArgs := map[string]any{
		"buyer":     "0x1111111111111111111111111111111111111111",
		"tokensOut": big.NewInt(10), "ethIn": big.NewInt(2), "newPrice": big.NewInt(3),
	}
	trade := tradeEvent("BUY", tradeArgs, "0xcurve", "0xtrade")
	trade.LogIndex = 2
	trade.BlockNumber = 99
	trade.Parsed.PersistedArgs = serializeJSONRecord(tradeArgs)

	perp := toParsed(t, ParseIndexedLog(increasePositionLog(
		big.NewInt(100), big.NewInt(10), big.NewInt(3000), big.NewInt(1), true)))
	perp.ContractAddress = "0xperp"
	perp.TxHash = "0xperp"

	stakedSpec := EventSpecByName("Staked")
	staked := toParsed(t, ParseIndexedLog(Log{
		Address: "0xpool", Topics: []string{stakedSpec.Topic0Hex, "0x" + addrWord(perpAccount)},
		Data: "0x" + uintWord(big.NewInt(1_000_000_000_000_000_000)),
	}))
	staked.ContractAddress = "0xpool"
	staked.TxHash = "0xstake"
	staked.LogIndex = 4
	staked.BlockNumber = 101

	for _, tc := range []struct {
		name, failSQL string
		event         ParsedEvent
		configure     func(*DBSink)
	}{
		{"trade to holder", "INSERT INTO token_holders", trade, func(*DBSink) {}},
		{"perp trade to position", "INSERT INTO perp_positions", perp, func(s *DBSink) {
			s.SetSymbolResolver(boundarySymbolResolver{})
		}},
		{"staking action to user stake", "INSERT INTO user_stakes", staked, func(*DBSink) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			injected := errors.New("injected derived write failure")
			db := &boundaryProjectionDB{failSQL: tc.failSQL, failErr: injected}
			sink := &DBSink{beginProjection: db.begin}
			tc.configure(sink)

			err := sink.HandleEvent(t.Context(), tc.event)
			if !errors.Is(err, injected) {
				t.Fatalf("HandleEvent err = %v, want injected failure", err)
			}
			if db.state != (boundaryProjectionState{}) {
				t.Fatalf("failed transaction leaked state: %+v", db.state)
			}
			if db.lastTx == nil || !db.lastTx.rolledBack || db.lastTx.committed {
				t.Fatalf("transaction outcome = %+v, want rollback only", db.lastTx)
			}
			if db.lastTx.ledgerWrites != 1 || db.lastTx.derivedWrites != 1 {
				t.Fatalf("boundary writes = ledger:%d derived:%d, want 1 each",
					db.lastTx.ledgerWrites, db.lastTx.derivedWrites)
			}
		})
	}
}

func TestRebuildPerpStateIsDeterministic(t *testing.T) {
	base := time.Unix(1_700_000_000, 0).UTC()
	entries := []perpLedgerEntry{
		{id: "t1", kind: "INCREASE", sizeDelta: "100", collateralDelta: "10", price: "3000", fee: "1", txHash: "0x1", logIndex: 1, blockNumber: 10, createdAt: base},
		{id: "t2", kind: "DECREASE", sizeDelta: "40", collateralDelta: "4", price: "2900", fee: "1", txHash: "0x2", logIndex: 2, blockNumber: 11, createdAt: base.Add(time.Second)},
	}
	first, prices, err := rebuildPerpState(11155111, perpAccount, "ETH-USD", true, entries)
	if err != nil {
		t.Fatalf("first rebuild: %v", err)
	}
	second, secondPrices, err := rebuildPerpState(11155111, perpAccount, "ETH-USD", true, entries)
	if err != nil {
		t.Fatalf("second rebuild: %v", err)
	}
	if !reflect.DeepEqual(first, second) || !reflect.DeepEqual(prices, secondPrices) {
		t.Fatalf("rebuild changed across replay:\nfirst=%+v\nsecond=%+v", first, second)
	}
	if len(first) != 1 || first[0].state.size.Int64() != 60 ||
		first[0].state.collateral.Int64() != 6 || first[0].state.status != "OPEN" {
		t.Fatalf("rebuilt position = %+v", first)
	}
}

func TestRebuildStakingStateIsDeterministicAcrossLifecycles(t *testing.T) {
	base := time.Unix(1_700_000_000, 0).UTC()
	entries := []stakingLedgerEntry{
		{action: "STAKE", amount: "10", txHash: "0x1", logIndex: 1, blockNumber: 10, createdAt: base},
		{action: "UNSTAKE", amount: "10", txHash: "0x2", logIndex: 2, blockNumber: 11, createdAt: base.Add(time.Second)},
		{action: "CLAIM", amount: "2", txHash: "0x3", logIndex: 3, blockNumber: 12, createdAt: base.Add(2 * time.Second)},
		{action: "STAKE", amount: "5", txHash: "0x4", logIndex: 4, blockNumber: 13, createdAt: base.Add(3 * time.Second)},
	}
	first, err := rebuildStakingState(11155111, "pool-1", perpAccount, entries)
	if err != nil {
		t.Fatalf("first rebuild: %v", err)
	}
	second, err := rebuildStakingState(11155111, "pool-1", perpAccount, entries)
	if err != nil {
		t.Fatalf("second rebuild: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("rebuild changed across replay:\nfirst=%+v\nsecond=%+v", first, second)
	}
	if len(first) != 2 || first[0].amount.Sign() != 0 || first[0].rewards.Int64() != 2 ||
		first[0].unstakedAt == nil || first[1].amount.Int64() != 5 || first[1].unstakedAt != nil {
		t.Fatalf("rebuilt stakes = %+v", first)
	}
	if first[0].id == first[1].id {
		t.Fatal("separate staking lifecycles must retain distinct stable IDs")
	}
}
