package portfolio

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/token"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/web3events"
)

type fakeTokens struct {
	tok        token.Token
	tokErr     error
	trades     []token.Trade
	tradesErr  error
	holders    []token.Holder
	holdersErr error
}

func (f *fakeTokens) FindByAddress(_ context.Context, _ string) (token.Token, error) {
	return f.tok, f.tokErr
}
func (f *fakeTokens) ListTradesForToken(_ context.Context, _ string, _ int) ([]token.Trade, error) {
	return f.trades, f.tradesErr
}
func (f *fakeTokens) ListHoldersForToken(_ context.Context, _ string, _ int) ([]token.Holder, error) {
	return f.holders, f.holdersErr
}

type fakeApprovals struct {
	count int64
	err   error
}

func (f *fakeApprovals) CountByContractAndName(_ context.Context, _, _ string) (int64, error) {
	return f.count, f.err
}

func ptrStr(v string) *string     { return &v }
func ptrFloat(v float64) *float64 { return &v }
func ptrDec(v string) *string     { return &v }

const validPortfolioAddr = "0x1234567890abcdef1234567890abcdef12345678"

type stubWalletCounter struct {
	n      int
	err    error
	onCall func()
}

func (s *stubWalletCounter) CountWallets(_ context.Context, _ *int) (int, error) {
	if s.onCall != nil {
		s.onCall()
	}
	return s.n, s.err
}

type stubEventCounter struct {
	n   int64
	err error
}

func (s *stubEventCounter) CountByActor(_ context.Context, _ string, _ *int) (int64, error) {
	return s.n, s.err
}

type stubTxCounter struct {
	n   int
	err error
}

func (s *stubTxCounter) CountTransactionsByFromAddress(_ context.Context, _ string, _ *int, _ string) (int, error) {
	return s.n, s.err
}

type stubTrendingLister struct {
	rows []token.TrendingTokenFull
	err  error
}

func (s *stubTrendingLister) ListTrendingFull(_ context.Context, _ *int, _ int) ([]token.TrendingTokenFull, error) {
	return s.rows, s.err
}

func TestService_NotFoundPropagates(t *testing.T) {
	svc := NewService(&fakeTokens{tokErr: token.ErrNotFound}, nil)
	if _, err := svc.GetAssetDetail(context.Background(), "0xabc"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestService_NilTokensReturnsNotFound(t *testing.T) {
	svc := NewService(nil, nil)
	if _, err := svc.GetAssetDetail(context.Background(), "0xabc"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestService_HappyPathAggregates(t *testing.T) {
	tok := token.Token{
		ID: "tok-1", Address: ptrStr("0xabc"), ChainID: 11155111,
		Symbol: "HONEY", Name: "Honey DAO",
		MarketCap:      ptrDec("38400000"),
		Volume24h:      ptrDec("1400000"),
		PriceChange24h: ptrFloat(4.6),
	}
	svc := NewService(&fakeTokens{
		tok:     tok,
		trades:  []token.Trade{{ID: "t1"}, {ID: "t2"}},
		holders: []token.Holder{{ID: "h1"}},
	}, &fakeApprovals{count: 7})

	got, err := svc.GetAssetDetail(context.Background(), "0xABC")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got.ID != "tok-1" || got.ChainID != 11155111 || got.Symbol != "HONEY" {
		t.Errorf("core fields wrong: %+v", got)
	}
	if got.MarketCap != 38.4e6 || got.Volume24h != 1.4e6 || got.PriceChange24h != 4.6 {
		t.Errorf("nullable floats deref wrong: %+v", got)
	}
	if got.ApprovalCount != 7 {
		t.Errorf("approvalCount = %d, want 7", got.ApprovalCount)
	}
	if len(got.RecentTrades) != 2 || len(got.Holders) != 1 {
		t.Errorf("trades/holders len = %d/%d, want 2/1", len(got.RecentTrades), len(got.Holders))
	}
}

func TestService_NullableFloatsZeroOut(t *testing.T) {
	tok := token.Token{ID: "tok-1", Address: ptrStr("0xabc"), Symbol: "X", Name: "X"}
	svc := NewService(&fakeTokens{tok: tok}, nil)
	got, err := svc.GetAssetDetail(context.Background(), "0xabc")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got.MarketCap != 0 || got.Volume24h != 0 || got.PriceChange24h != 0 {
		t.Errorf("nil floats should be 0, got %+v", got)
	}
	if got.ApprovalCount != 0 {
		t.Errorf("nil ApprovalCounter should yield 0, got %d", got.ApprovalCount)
	}
}

func TestService_ApprovalsPoolUnavailableDoesNotPropagate(t *testing.T) {
	// A pool-unavailable error from the approvals counter must NOT
	// fail the whole request — the token + trades + holders payload
	// is still useful with approvalCount=0.
	tok := token.Token{ID: "tok-1", Address: ptrStr("0xabc"), Symbol: "X", Name: "X"}
	svc := NewService(
		&fakeTokens{tok: tok},
		&fakeApprovals{err: web3events.ErrPoolUnavailable},
	)
	got, err := svc.GetAssetDetail(context.Background(), "0xabc")
	if err != nil {
		t.Fatalf("err = %v, want nil (degraded)", err)
	}
	if got.ApprovalCount != 0 {
		t.Errorf("approvalCount = %d, want 0 on degraded", got.ApprovalCount)
	}
}

func TestService_ApprovalsHardErrorPropagates(t *testing.T) {
	wantErr := errors.New("network down")
	tok := token.Token{ID: "tok-1", Address: ptrStr("0xabc"), Symbol: "X", Name: "X"}
	svc := NewService(&fakeTokens{tok: tok}, &fakeApprovals{err: wantErr})
	if _, err := svc.GetAssetDetail(context.Background(), "0xabc"); !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want wrapped %q", err, wantErr)
	}
}

func TestService_NoAddressInToken(t *testing.T) {
	// Token row without a contract address (PENDING status, etc.):
	// skip the approval count query — the count by contract address
	// would be meaningless.
	tok := token.Token{ID: "tok-1", Address: nil, Symbol: "PRESALE", Name: "PRE"}
	svc := NewService(&fakeTokens{tok: tok}, &fakeApprovals{count: 99})
	got, err := svc.GetAssetDetail(context.Background(), "ignored")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got.ApprovalCount != 0 {
		t.Errorf("approvalCount = %d, want 0 when token has no address", got.ApprovalCount)
	}
}

func TestHandler_404(t *testing.T) {
	svc := NewService(&fakeTokens{tokErr: token.ErrNotFound}, nil)
	mux := Router(svc, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/assets/0xabc", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "token not found") {
		t.Errorf("body = %q", rec.Body.String())
	}
}

func TestGetSummary_NoOwnerWalletsCountedFromDB(t *testing.T) {
	// No-owner path: walletCount comes from the DB (not the "1"
	// shortcut); recentActivity, alerts, inventory stay zero.
	svc := NewServiceFromDeps(Deps{
		Wallets: &stubWalletCounter{n: 42},
	})
	got, err := svc.GetSummary(context.Background(), "", nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got.TrackedWallets != 42 {
		t.Errorf("trackedWallets = %d, want 42", got.TrackedWallets)
	}
	if got.Address != nil {
		t.Errorf("address = %v, want nil", got.Address)
	}
	if got.DataCompleteness != "syncing" {
		t.Errorf("dataCompleteness = %q, want syncing", got.DataCompleteness)
	}
}

func TestGetSummary_OwnerShortcutsWalletCountToOne(t *testing.T) {
	// Owner present: walletCount becomes 1 without hitting the DB.
	// Even if a wallet counter is wired, it must not be called.
	called := false
	wc := &stubWalletCounter{n: 999, onCall: func() { called = true }}
	svc := NewServiceFromDeps(Deps{Wallets: wc})
	got, _ := svc.GetSummary(context.Background(), validPortfolioAddr, nil)
	if got.TrackedWallets != 1 {
		t.Errorf("trackedWallets = %d, want 1", got.TrackedWallets)
	}
	if called {
		t.Errorf("owner path must not query wallet count")
	}
}

func TestGetSummary_RecentActivityIsMaxOfEventsAndTx(t *testing.T) {
	// recentActivityCount = max(events, txCount). NestJS uses Math.max
	// so the higher of the two sources wins.
	svc := NewServiceFromDeps(Deps{
		Events:       &stubEventCounter{n: 5},
		Transactions: &stubTxCounter{n: 12},
	})
	got, _ := svc.GetSummary(context.Background(), validPortfolioAddr, nil)
	if got.RecentActivityCount != 12 {
		t.Errorf("recentActivityCount = %d, want 12", got.RecentActivityCount)
	}
}

func TestGetSummary_DataCompletenessTransitions(t *testing.T) {
	// portfolioValue=null + events=0 + no filters → syncing
	svc := NewServiceFromDeps(Deps{})
	got, _ := svc.GetSummary(context.Background(), validPortfolioAddr, nil)
	if got.DataCompleteness != "syncing" {
		t.Errorf("got %q, want syncing", got.DataCompleteness)
	}

	// events>0 → partial-live
	svc2 := NewServiceFromDeps(Deps{Events: &stubEventCounter{n: 3}})
	got2, _ := svc2.GetSummary(context.Background(), validPortfolioAddr, nil)
	if got2.DataCompleteness != "partial-live" {
		t.Errorf("got %q, want partial-live", got2.DataCompleteness)
	}
}

func TestGetSummary_DayChangePctAveragesTopMovers(t *testing.T) {
	pm := []float64{10, 20, -6, 0}
	rows := make([]token.TrendingTokenFull, len(pm))
	for i, v := range pm {
		v := v
		rows[i] = token.TrendingTokenFull{ID: "x", Symbol: "X", Name: "X", PriceChange24h: &v}
	}
	svc := NewServiceFromDeps(Deps{Trending: &stubTrendingLister{rows: rows}})
	got, _ := svc.GetSummary(context.Background(), "", nil)
	if got.DayChangePct != 6.0 { // (10+20-6+0)/4 = 6
		t.Errorf("dayChangePct = %v, want 6", got.DayChangePct)
	}
	if len(got.TopMovers) != 4 {
		t.Errorf("topMovers len = %d, want 4", len(got.TopMovers))
	}
}

func TestGetAssets_EmptyAddressReturnsEmptyInventory(t *testing.T) {
	svc := NewService(nil, nil)
	got, err := svc.GetAssets(context.Background(), "", nil)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(got.Items) != 0 || got.FilterSummary.TotalCount != 0 {
		t.Errorf("got %+v, want empty inventory", got)
	}
	if got.FilterSummary.SpamFilterLevel != "standard" {
		t.Errorf("default level = %q, want standard", got.FilterSummary.SpamFilterLevel)
	}
}

func TestGetAssets_InvalidAddressReturnsEmpty(t *testing.T) {
	svc := NewService(nil, nil)
	got, _ := svc.GetAssets(context.Background(), "not-an-address", nil)
	if len(got.Items) != 0 {
		t.Errorf("invalid addr must yield empty items, got %d", len(got.Items))
	}
}

func TestGetAssets_NilDepsDegradeToEmpty(t *testing.T) {
	// All inventory deps nil + a valid address: should return an empty
	// envelope with totalCount=0 and the default spam level.
	svc := NewServiceWithInventory(nil, nil, nil, nil, nil, nil, nil)
	got, err := svc.GetAssets(context.Background(), validPortfolioAddr, nil)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got.FilterSummary.TotalCount != 0 || len(got.Items) != 0 {
		t.Errorf("got %+v, want empty", got)
	}
}

func TestHandler_HappyPathReturnsEnvelope(t *testing.T) {
	tok := token.Token{ID: "tok-1", Address: ptrStr("0xabc"), Symbol: "HONEY", Name: "Honey", ChainID: 1,
		MarketCap: ptrDec("1000000"), Volume24h: ptrDec("2000000"), PriceChange24h: ptrFloat(5)}
	svc := NewService(&fakeTokens{tok: tok}, &fakeApprovals{count: 3})
	mux := Router(svc, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/assets/0xabc", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	var body AssetDetail
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Symbol != "HONEY" || body.ApprovalCount != 3 {
		t.Errorf("body = %+v", body)
	}
}
