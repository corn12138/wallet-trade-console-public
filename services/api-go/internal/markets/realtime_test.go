package markets

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/trading"
)

func TestGetRealtimeSnapshot_HappyPath(t *testing.T) {
	tradingRepo := fakeTradingRepo{
		positions: []trading.PerpPosition{
			{Token: "ETH-USD", IsLong: true, Size: "100"},
			{Token: "ETH-USD", IsLong: true, Size: "200"},
			{Token: "ETH-USD", IsLong: false, Size: "50"},
		},
		trades: []trading.PerpTrade{
			{Token: "ETH-USD", SizeDelta: "10"},
			{Token: "ETH-USD", SizeDelta: "20"},
		},
		orders: []trading.PerpOrder{
			// Bids should sort descending by triggerPrice.
			{IsLong: true, SizeDelta: "5", TriggerPrice: ptrStr("100")},
			{IsLong: true, SizeDelta: "7", TriggerPrice: ptrStr("110")},
			// Asks ascending.
			{IsLong: false, SizeDelta: "4", TriggerPrice: ptrStr("130")},
			{IsLong: false, SizeDelta: "8", TriggerPrice: ptrStr("120")},
		},
		recent: []trading.PerpTradeFull{
			{
				ID: "trade-1", Token: "ETH-USD", IsLong: true, Type: "INCREASE",
				SizeDelta: "33", Price: "115", Fee: "1",
				PNL: ptrString("0"), TxHash: "0xabc",
				CreatedAt: time.Date(2026, 5, 20, 3, 0, 0, 0, time.UTC),
			},
		},
	}

	svc := NewService(freshDeployments(), tradingRepo, nil)
	pinClock(svc, "2026-05-20T03:00:00Z")

	got, err := svc.GetRealtimeSnapshot(context.Background(), "ETH-USD", ptrInt(11155111))
	if err != nil {
		t.Fatalf("GetRealtimeSnapshot: %v", err)
	}

	if got.Symbol != "ETH-USD" {
		t.Errorf("symbol = %q", got.Symbol)
	}
	if got.ChainID == nil || *got.ChainID != 11155111 {
		t.Errorf("chainId = %v", got.ChainID)
	}
	if got.Ticker == nil {
		t.Fatalf("ticker should be non-nil")
	}
	if got.Ticker.Price != "115" {
		t.Errorf("price = %q, want 115 (from latest trade)", got.Ticker.Price)
	}
	if got.Ticker.LongOpenInterest != "300" || got.Ticker.ShortOpenInterest != "50" || got.Ticker.Volume24h != "30" {
		t.Errorf("aggregations wrong: %+v", got.Ticker)
	}
	if got.Ticker.BestBid == nil || *got.Ticker.BestBid != "110" {
		t.Errorf("bestBid = %v", got.Ticker.BestBid)
	}
	if got.Ticker.BestAsk == nil || *got.Ticker.BestAsk != "120" {
		t.Errorf("bestAsk = %v", got.Ticker.BestAsk)
	}
	if got.Ticker.Spread == nil || *got.Ticker.Spread != "10" {
		t.Errorf("spread = %v", got.Ticker.Spread)
	}

	// Orderbook sort verification.
	if len(got.Orderbook.Bids) != 2 || got.Orderbook.Bids[0][0] != "110" || got.Orderbook.Bids[1][0] != "100" {
		t.Errorf("bids not sorted descending: %+v", got.Orderbook.Bids)
	}
	if len(got.Orderbook.Asks) != 2 || got.Orderbook.Asks[0][0] != "120" || got.Orderbook.Asks[1][0] != "130" {
		t.Errorf("asks not sorted ascending: %+v", got.Orderbook.Asks)
	}

	if len(got.Trades) != 1 || got.Trades[0].Price != "115" || got.Trades[0].TradeType != "INCREASE" {
		t.Errorf("trades wrong: %+v", got.Trades)
	}
}

func TestGetRealtimeSnapshot_UnknownSymbolReturnsNullTicker(t *testing.T) {
	svc := NewService(freshDeployments(), fakeTradingRepo{}, nil)
	pinClock(svc, "2026-05-20T03:00:00Z")

	chain := 11155111
	got, err := svc.GetRealtimeSnapshot(context.Background(), "DOGE-USD", &chain)
	if err != nil {
		t.Fatalf("GetRealtimeSnapshot: %v", err)
	}
	if got.Ticker != nil {
		t.Errorf("ticker should be nil for unknown symbol; got %+v", got.Ticker)
	}
	if got.Symbol != "DOGE-USD" {
		t.Errorf("symbol should echo input; got %q", got.Symbol)
	}
	if got.ChainID == nil || *got.ChainID != 11155111 {
		t.Errorf("chainId should fall back to query value; got %v", got.ChainID)
	}
	if len(got.Orderbook.Bids) != 0 || len(got.Orderbook.Asks) != 0 || len(got.Trades) != 0 {
		t.Errorf("expected degraded empty payload; got %+v", got)
	}
}

func TestGetRealtimeSnapshot_PriceFallbackChain(t *testing.T) {
	// No recent trades; mid = (100+200)/2 = 150
	svc := NewService(freshDeployments(), fakeTradingRepo{
		orders: []trading.PerpOrder{
			{IsLong: true, SizeDelta: "1", TriggerPrice: ptrStr("100")},
			{IsLong: false, SizeDelta: "1", TriggerPrice: ptrStr("200")},
		},
	}, nil)
	pinClock(svc, "2026-05-20T03:00:00Z")

	got, _ := svc.GetRealtimeSnapshot(context.Background(), "ETH-USD", ptrInt(11155111))
	if got.Ticker.Price != "150" {
		t.Errorf("expected mid-price 150; got %q", got.Ticker.Price)
	}

	// Only bids → falls back to bestBid
	svc2 := NewService(freshDeployments(), fakeTradingRepo{
		orders: []trading.PerpOrder{{IsLong: true, SizeDelta: "1", TriggerPrice: ptrStr("100")}},
	}, nil)
	pinClock(svc2, "2026-05-20T03:00:00Z")
	got2, _ := svc2.GetRealtimeSnapshot(context.Background(), "ETH-USD", ptrInt(11155111))
	if got2.Ticker.Price != "100" {
		t.Errorf("expected bestBid fallback; got %q", got2.Ticker.Price)
	}

	// Empty orderbook + no trades → "0"
	svc3 := NewService(freshDeployments(), fakeTradingRepo{}, nil)
	pinClock(svc3, "2026-05-20T03:00:00Z")
	got3, _ := svc3.GetRealtimeSnapshot(context.Background(), "ETH-USD", ptrInt(11155111))
	if got3.Ticker.Price != "0" {
		t.Errorf("expected zero-fallback price; got %q", got3.Ticker.Price)
	}
}

func TestGetRealtimeSnapshot_NilRepoDegrades(t *testing.T) {
	svc := NewService(freshDeployments(), nil, nil)
	pinClock(svc, "2026-05-20T03:00:00Z")

	got, err := svc.GetRealtimeSnapshot(context.Background(), "ETH-USD", ptrInt(11155111))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.Ticker == nil || got.Ticker.Price != "0" {
		t.Errorf("expected zeroed ticker; got %+v", got.Ticker)
	}
}

func TestSymbolSnapshotHandler_400OnInvalidSymbol(t *testing.T) {
	svc := NewService(freshDeployments(), fakeTradingRepo{}, nil)
	pinClock(svc, "2026-05-20T03:00:00Z")

	// URL-escaped invalid symbols: two spaces, "abc def", "$ETH",
	// 65-char A run. After chi extraction + normalization these all
	// fail the controller-style validation.
	for _, escaped := range []string{
		"%20%20",
		"abc%20def",
		"%24ETH",
		strings.Repeat("A", 65),
	} {
		req := httptest.NewRequest(http.MethodGet, "/snapshot/"+escaped, nil)
		rr := httptest.NewRecorder()
		Router(svc).ServeHTTP(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("symbol %q: status = %d, body = %s", escaped, rr.Code, rr.Body.String())
		}
	}
}

func TestSymbolSnapshotHandler_200OnLowercaseInput(t *testing.T) {
	// Symbol case is normalized before regex check, so lowercase input
	// should pass and resolve to the deployed market.
	svc := NewService(freshDeployments(), fakeTradingRepo{}, nil)
	pinClock(svc, "2026-05-20T03:00:00Z")

	req := httptest.NewRequest(http.MethodGet, "/snapshot/eth-usd?chainId=31337", nil)
	rr := httptest.NewRecorder()
	Router(svc).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var got RealtimeSnapshot
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Symbol != "ETH-USD" {
		t.Errorf("symbol should normalize to upper; got %q", got.Symbol)
	}
	if got.Ticker == nil || got.Ticker.ChainID != 31337 {
		t.Errorf("ticker chainId wrong: %+v", got.Ticker)
	}
}

func TestSymbolSnapshotHandler_OmitsChainIDWhenAbsent(t *testing.T) {
	// Unknown symbol + no chainId param → chainId field must be absent
	// from JSON (matches NestJS JSON.stringify dropping `undefined`).
	svc := NewService(freshDeployments(), fakeTradingRepo{}, nil)
	pinClock(svc, "2026-05-20T03:00:00Z")

	req := httptest.NewRequest(http.MethodGet, "/snapshot/DOGE-USD", nil)
	rr := httptest.NewRecorder()
	Router(svc).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if strings.Contains(rr.Body.String(), `"chainId"`) {
		t.Errorf("chainId should be omitted when nil; body = %s", rr.Body.String())
	}
}

func TestIsValidSymbol(t *testing.T) {
	good := []string{"ETH-USD", "BTC-USD", "ASSET.V2", "PAIR:USDC", "PERP/USD", "ABC_DEF"}
	bad := []string{"", "lowercase", "with space", "$BAD", "RIP!"}
	for _, s := range good {
		if !isValidSymbol(s) {
			t.Errorf("expected %q valid", s)
		}
	}
	for _, s := range bad {
		if isValidSymbol(s) {
			t.Errorf("expected %q invalid", s)
		}
	}
	if isValidSymbol(strings.Repeat("A", 65)) {
		t.Error("65-char symbol should exceed max length")
	}
}
