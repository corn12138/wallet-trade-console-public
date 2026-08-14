package trading

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func ptrInt(v int) *int { return &v }

func TestBuildPerpPositionsQuery_NoFilters(t *testing.T) {
	q, args := buildPerpPositionsQuery(nil, nil)
	wantSQL := `SELECT token, "isLong", size FROM perp_positions WHERE status = $1`
	if q != wantSQL {
		t.Errorf("SQL = %q, want %q", q, wantSQL)
	}
	if !reflect.DeepEqual(args, []any{"OPEN"}) {
		t.Errorf("args = %v", args)
	}
}

func TestBuildPerpPositionsQuery_ChainFilter(t *testing.T) {
	q, args := buildPerpPositionsQuery(nil, ptrInt(31337))
	wantSQL := `SELECT token, "isLong", size FROM perp_positions WHERE status = $1 AND chain_id = $2`
	if q != wantSQL {
		t.Errorf("SQL = %q, want %q", q, wantSQL)
	}
	if !reflect.DeepEqual(args, []any{"OPEN", 31337}) {
		t.Errorf("args = %v", args)
	}
}

func TestBuildPerpPositionsQuery_SymbolsFilter(t *testing.T) {
	q, args := buildPerpPositionsQuery([]string{"ETH-USD", "BTC-USD"}, ptrInt(11155111))
	wantSQL := `SELECT token, "isLong", size FROM perp_positions WHERE status = $1 AND chain_id = $2 AND token IN ($3, $4)`
	if q != wantSQL {
		t.Errorf("SQL = %q, want %q", q, wantSQL)
	}
	if !reflect.DeepEqual(args, []any{"OPEN", 11155111, "ETH-USD", "BTC-USD"}) {
		t.Errorf("args = %v", args)
	}
}

func TestBuildPerpTradesQuery_NoExtraFilters(t *testing.T) {
	since := time.Date(2026, 5, 19, 0, 0, 0, 0, time.UTC)
	q, args := buildPerpTradesQuery(nil, nil, since)
	wantSQL := `SELECT token, "sizeDelta" FROM perp_trades WHERE "createdAt" >= $1`
	if q != wantSQL {
		t.Errorf("SQL = %q, want %q", q, wantSQL)
	}
	if len(args) != 1 || !args[0].(time.Time).Equal(since) {
		t.Errorf("args = %v", args)
	}
}

func TestBuildPerpTradesQuery_ChainAndSymbols(t *testing.T) {
	since := time.Date(2026, 5, 19, 0, 0, 0, 0, time.UTC)
	q, args := buildPerpTradesQuery([]string{"ETH-USD"}, ptrInt(11155111), since)
	wantSQL := `SELECT token, "sizeDelta" FROM perp_trades WHERE "createdAt" >= $1 AND chain_id = $2 AND token IN ($3)`
	if q != wantSQL {
		t.Errorf("SQL = %q, want %q", q, wantSQL)
	}
	if len(args) != 3 || args[1] != 11155111 || args[2] != "ETH-USD" {
		t.Errorf("args = %v", args)
	}
}

func TestRepository_NilPoolReturnsErr(t *testing.T) {
	r := NewRepository(nil)
	ctx := context.Background()
	if _, err := r.ListOpenPositions(ctx, nil, nil); err != ErrPoolUnavailable {
		t.Errorf("ListOpenPositions err = %v, want ErrPoolUnavailable", err)
	}
	if _, err := r.ListRecentTrades(ctx, nil, nil, time.Time{}); err != ErrPoolUnavailable {
		t.Errorf("ListRecentTrades err = %v, want ErrPoolUnavailable", err)
	}
}
