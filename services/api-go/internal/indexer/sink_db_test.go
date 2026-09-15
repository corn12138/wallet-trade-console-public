package indexer

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type tradeLedgerSnapshot struct {
	user, amount string
}

type tradeLedgerStore struct {
	rows         map[string]tradeLedgerSnapshot
	legacyHashes map[string]bool
}

func tradeLedgerKey(chainID int, txHash string, logIndex int) string {
	return fmt.Sprintf("%d/%s/%d", chainID, txHash, logIndex)
}

func (s *tradeLedgerStore) Exec(_ context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	switch {
	case strings.Contains(query, "UPDATE token_trades") && strings.Contains(query, "log_index IS NULL"):
		chainID, txHash, logIndex := args[0].(int), args[9].(string), args[8].(int)
		if !s.legacyHashes[txHash] {
			return pgconn.NewCommandTag("UPDATE 0"), nil
		}
		delete(s.legacyHashes, txHash)
		s.rows[tradeLedgerKey(chainID, txHash, logIndex)] = tradeLedgerSnapshot{
			user: args[2].(string), amount: args[4].(string),
		}
		return pgconn.NewCommandTag("UPDATE 1"), nil
	case strings.Contains(query, "INSERT INTO token_trades"):
		chainID, txHash, logIndex := args[0].(int), args[9].(string), args[8].(int)
		key := tradeLedgerKey(chainID, txHash, logIndex)
		if _, exists := s.rows[key]; exists {
			return pgconn.NewCommandTag("INSERT 0 0"), nil
		}
		s.rows[key] = tradeLedgerSnapshot{user: args[2].(string), amount: args[4].(string)}
		return pgconn.NewCommandTag("INSERT 0 1"), nil
	case strings.Contains(query, "UPDATE token_trades"):
		chainID, txHash, logIndex := args[0].(int), args[1].(string), args[2].(int)
		key := tradeLedgerKey(chainID, txHash, logIndex)
		if _, exists := s.rows[key]; !exists {
			return pgconn.NewCommandTag("UPDATE 0"), nil
		}
		s.rows[key] = tradeLedgerSnapshot{user: args[4].(string), amount: args[6].(string)}
		return pgconn.NewCommandTag("UPDATE 1"), nil
	case strings.Contains(query, "UPDATE tokens AS token"), strings.Contains(query, "INSERT INTO token_holders"):
		return pgconn.NewCommandTag("UPDATE 1"), nil
	default:
		return pgconn.CommandTag{}, fmt.Errorf("trade ledger test received unexpected SQL")
	}
}

func (s *tradeLedgerStore) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("trade ledger test received unexpected Query")
}

func (s *tradeLedgerStore) QueryRow(context.Context, string, ...any) pgx.Row {
	address := "0x1000000000000000000000000000000000000001"
	return boundaryRow{values: []any{"token-1", &address}}
}

func TestDBSink_NilPoolReturnsErr(t *testing.T) {
	var nilRecv *DBSink
	if err := nilRecv.HandleEvent(context.Background(), ParsedEvent{}); !errors.Is(err, ErrSinkPoolNil) {
		t.Fatalf("nil receiver err = %v, want ErrSinkPoolNil", err)
	}
	if err := NewDBSink(nil).HandleEvent(context.Background(), ParsedEvent{}); !errors.Is(err, ErrSinkPoolNil) {
		t.Fatalf("nil pool err = %v, want ErrSinkPoolNil", err)
	}
}

func TestEncodeArgs(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   map[string]any
		want string
	}{
		{"nil → empty object", nil, "{}"},
		{"empty → empty object", map[string]any{}, "{}"},
		{"single key", map[string]any{"amount": "1000"}, `{"amount":"1000"}`},
	} {
		got, err := encodeArgs(tc.in)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestActorOrNil(t *testing.T) {
	if actorOrNil("") != nil {
		t.Error("empty address should map to nil (NULL column)")
	}
	if got := actorOrNil("0xAbCDef"); got != "0xabcdef" {
		t.Errorf("actor = %v, want lower-cased 0xabcdef", got)
	}
}

func tokenCreatedEvent(args map[string]any) ParsedEvent {
	return ParsedEvent{
		ChainID: 11155111,
		TxHash:  "0xabc",
		Parsed: ParsedIndexedLog{
			EventName:   "TokenCreated",
			RuntimeArgs: args,
		},
	}
}

func TestTokenCreatedRow(t *testing.T) {
	t.Run("full args, addresses lower-cased", func(t *testing.T) {
		r, ok := tokenCreatedRow(tokenCreatedEvent(map[string]any{
			"token":        "0xAAable",
			"bondingCurve": "0xBBCurve",
			"creator":      "0xCCreator",
			"symbol":       "TKN",
			"name":         "Token Name",
		}))
		if !ok {
			t.Fatal("ok=false for complete args")
		}
		if r.address != "0xaaable" || r.bondingCurve != "0xbbcurve" || r.creator != "0xccreator" {
			t.Errorf("addresses not lower-cased: %+v", r)
		}
		if r.symbol != "TKN" || r.name != "Token Name" || r.chainID != 11155111 {
			t.Errorf("unexpected row: %+v", r)
		}
	})
	t.Run("missing token → not ok", func(t *testing.T) {
		if _, ok := tokenCreatedRow(tokenCreatedEvent(map[string]any{"creator": "0xC"})); ok {
			t.Error("expected ok=false without a token address")
		}
	})
	t.Run("missing creator → not ok", func(t *testing.T) {
		if _, ok := tokenCreatedRow(tokenCreatedEvent(map[string]any{"token": "0xA"})); ok {
			t.Error("expected ok=false without a creator address")
		}
	})
}

func TestProcessEvent_NonBusinessEventIsNoop(t *testing.T) {
	// A non-business event must not touch the pool (safe on a nil-pool sink).
	ev := ParsedEvent{Parsed: ParsedIndexedLog{EventName: "Transfer"}}
	if err := (&DBSink{}).processEvent(context.Background(), nil, ev); err != nil {
		t.Fatalf("Transfer dispatch err = %v, want nil", err)
	}
}

func TestProcessEvent_TokenCreatedValidatesBeforePool(t *testing.T) {
	// TokenCreated with missing required args must error out before the
	// (nil) pool is dereferenced — proves the validation guard runs first.
	ev := tokenCreatedEvent(map[string]any{"symbol": "X"}) // no token/creator
	if err := (&DBSink{}).processEvent(context.Background(), nil, ev); err == nil {
		t.Fatal("expected an error for TokenCreated missing token/creator")
	}
}

func TestNullIfEmpty(t *testing.T) {
	if nullIfEmpty("") != nil {
		t.Error(`"" should map to nil`)
	}
	if got := nullIfEmpty("0xabc"); got != "0xabc" {
		t.Errorf("nullIfEmpty(0xabc) = %v, want 0xabc", got)
	}
}

func TestArgStringAndLowerAddr(t *testing.T) {
	if lowerAddr(123) != "" || lowerAddr(nil) != "" {
		t.Error("non-string arg should yield empty string")
	}
	if lowerAddr("0xABC") != "0xabc" {
		t.Error("lowerAddr should lower-case")
	}
	if argString(123) != "" || argString("hi") != "hi" {
		t.Error("argString mismatch")
	}
}

func TestArgBigInt(t *testing.T) {
	if argBigInt(nil) != nil || argBigInt("123") != nil || argBigInt(123) != nil {
		t.Error("non-*big.Int args must yield nil")
	}
	if got := argBigInt(big.NewInt(42)); got == nil || got.Int64() != 42 {
		t.Errorf("argBigInt(*big.Int) = %v, want 42", got)
	}
}

func tradeEvent(side string, args map[string]any, curve, tx string) ParsedEvent {
	name := "Buy"
	if side == "SELL" {
		name = "Sell"
	}
	return ParsedEvent{
		ChainID:         11155111,
		ContractAddress: curve,
		TxHash:          tx,
		Parsed:          ParsedIndexedLog{EventName: name, RuntimeArgs: args},
	}
}

func TestTradeRowFrom(t *testing.T) {
	t.Run("BUY maps buyer/tokensOut/ethIn and lower-cases curve+user", func(t *testing.T) {
		r, ok := tradeRowFrom(tradeEvent("BUY", map[string]any{
			"buyer":     "0xBuyer",
			"tokensOut": big.NewInt(1000),
			"ethIn":     big.NewInt(50),
			"newPrice":  big.NewInt(7),
		}, "0xCurve", "0xtx1"), "BUY")
		if !ok {
			t.Fatal("ok=false for complete BUY args")
		}
		if r.user != "0xbuyer" || r.curve != "0xcurve" || r.txHash != "0xtx1" {
			t.Errorf("unexpected row identity: %+v", r)
		}
		if r.tokenAmount.Int64() != 1000 || r.ethAmount.Int64() != 50 || r.newPrice.Int64() != 7 {
			t.Errorf("unexpected amounts: %+v", r)
		}
	})

	t.Run("SELL maps seller/tokensIn/ethOut", func(t *testing.T) {
		r, ok := tradeRowFrom(tradeEvent("SELL", map[string]any{
			"seller":   "0xSeller",
			"tokensIn": big.NewInt(200),
			"ethOut":   big.NewInt(9),
			"newPrice": big.NewInt(3),
		}, "0xC", "0xtx2"), "SELL")
		if !ok {
			t.Fatal("ok=false for complete SELL args")
		}
		if r.user != "0xseller" || r.tokenAmount.Int64() != 200 || r.ethAmount.Int64() != 9 {
			t.Errorf("unexpected SELL row: %+v", r)
		}
	})

	t.Run("missing eth amount defaults to 0", func(t *testing.T) {
		r, ok := tradeRowFrom(tradeEvent("BUY", map[string]any{
			"buyer":     "0xa",
			"tokensOut": big.NewInt(1),
			"newPrice":  big.NewInt(1),
		}, "0xc", "0xt"), "BUY")
		if !ok || r.ethAmount == nil || r.ethAmount.Sign() != 0 {
			t.Errorf("expected ethAmount default 0, got %+v ok=%v", r.ethAmount, ok)
		}
	})

	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"missing trader", map[string]any{"tokensOut": big.NewInt(1), "newPrice": big.NewInt(1)}},
		{"missing token amount", map[string]any{"buyer": "0xa", "newPrice": big.NewInt(1)}},
		{"missing price", map[string]any{"buyer": "0xa", "tokensOut": big.NewInt(1)}},
	} {
		t.Run(tc.name+" → not ok", func(t *testing.T) {
			if _, ok := tradeRowFrom(tradeEvent("BUY", tc.args, "0xc", "0xt"), "BUY"); ok {
				t.Errorf("expected ok=false for %s", tc.name)
			}
		})
	}
}

func TestHandleTradeKeepsEveryLogWhenTransactionHashRepeats(t *testing.T) {
	const txHash = "0xshared-transaction"
	store := &tradeLedgerStore{
		rows: map[string]tradeLedgerSnapshot{}, legacyHashes: map[string]bool{txHash: true},
	}
	sink := &DBSink{}
	first := tradeEvent("BUY", map[string]any{
		"buyer":     "0x1111111111111111111111111111111111111111",
		"tokensOut": big.NewInt(10), "ethIn": big.NewInt(1), "newPrice": big.NewInt(2),
	}, "0xcurve", txHash)
	first.BlockNumber, first.LogIndex = 100, 7
	second := tradeEvent("BUY", map[string]any{
		"buyer":     "0x2222222222222222222222222222222222222222",
		"tokensOut": big.NewInt(20), "ethIn": big.NewInt(2), "newPrice": big.NewInt(3),
	}, "0xcurve", txHash)
	second.BlockNumber, second.LogIndex = 100, 8

	for _, event := range []ParsedEvent{first, second, second, first} {
		if err := sink.handleTrade(t.Context(), &eventProjection{store: store}, event, "BUY"); err != nil {
			t.Fatalf("handleTrade log %d: %v", event.LogIndex, err)
		}
	}
	if len(store.rows) != 2 {
		t.Fatalf("durable trade rows = %d, want 2", len(store.rows))
	}
	if got := store.rows[tradeLedgerKey(11155111, txHash, 7)].amount; got != "10" {
		t.Fatalf("log 7 amount = %q", got)
	}
	if got := store.rows[tradeLedgerKey(11155111, txHash, 8)].amount; got != "20" {
		t.Fatalf("log 8 amount = %q", got)
	}
}

func TestProcessEvent_TradeValidatesBeforePool(t *testing.T) {
	// Buy/Sell with missing required args must error out before the (nil) pool is
	// dereferenced — proves the validation guard runs first for the trade path.
	for _, side := range []string{"BUY", "SELL"} {
		ev := tradeEvent(side, map[string]any{"ethIn": big.NewInt(1)}, "0xc", "0xt") // no trader/amount/price
		if err := (&DBSink{}).processEvent(context.Background(), nil, ev); err == nil {
			t.Fatalf("%s: expected an error for missing trader/amount/price", side)
		}
	}
}
