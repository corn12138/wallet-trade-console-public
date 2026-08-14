package web3events

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

type stubReader struct {
	listFn     func(context.Context, ListEventsQuery) (ListEventsResult, error)
	statsFn    func(context.Context, int) (FullStats, error)
	byTxFn     func(context.Context, string, int) ([]EventRow, error)
	byUserFn   func(context.Context, string, int, int) ([]EventRow, error)
	recentTxFn func(context.Context, int, int) ([]TransactionRow, error)
}

func (s stubReader) ListEvents(ctx context.Context, q ListEventsQuery) (ListEventsResult, error) {
	if s.listFn != nil {
		return s.listFn(ctx, q)
	}
	return ListEventsResult{Data: []EventRow{}, Pagination: Pagination{Page: q.Page, Limit: q.Limit}}, nil
}
func (s stubReader) GetFullStats(ctx context.Context, chainID int) (FullStats, error) {
	if s.statsFn != nil {
		return s.statsFn(ctx, chainID)
	}
	return FullStats{EventCounts: []EventCount{}, LatestBlock: "0", IndexerBlock: "0"}, nil
}
func (s stubReader) EventsByTxHash(ctx context.Context, txHash string, chainID int) ([]EventRow, error) {
	if s.byTxFn != nil {
		return s.byTxFn(ctx, txHash, chainID)
	}
	return []EventRow{}, nil
}
func (s stubReader) UserEvents(ctx context.Context, addr string, chainID, limit int) ([]EventRow, error) {
	if s.byUserFn != nil {
		return s.byUserFn(ctx, addr, chainID, limit)
	}
	return []EventRow{}, nil
}
func (s stubReader) RecentTransactions(ctx context.Context, chainID, limit int) ([]TransactionRow, error) {
	if s.recentTxFn != nil {
		return s.recentTxFn(ctx, chainID, limit)
	}
	return []TransactionRow{}, nil
}

func TestRepository_NilPoolReturnsErr(t *testing.T) {
	r := NewRepository(nil)
	if _, err := r.GetStats(context.Background(), 11155111); !errors.Is(err, ErrPoolUnavailable) {
		t.Errorf("GetStats err = %v, want ErrPoolUnavailable", err)
	}
	if _, err := r.GetFullStats(context.Background(), 11155111); !errors.Is(err, ErrPoolUnavailable) {
		t.Errorf("GetFullStats err = %v, want ErrPoolUnavailable", err)
	}
	if _, err := r.ListEvents(context.Background(), ListEventsQuery{ChainID: 11155111}); !errors.Is(err, ErrPoolUnavailable) {
		t.Errorf("ListEvents err = %v, want ErrPoolUnavailable", err)
	}
	if _, err := r.EventsByTxHash(context.Background(), "0xdead", 11155111); !errors.Is(err, ErrPoolUnavailable) {
		t.Errorf("EventsByTxHash err = %v, want ErrPoolUnavailable", err)
	}
	if _, err := r.UserEvents(context.Background(), "0xabc", 11155111, 10); !errors.Is(err, ErrPoolUnavailable) {
		t.Errorf("UserEvents err = %v, want ErrPoolUnavailable", err)
	}
	if _, err := r.RecentTransactions(context.Background(), 11155111, 10); !errors.Is(err, ErrPoolUnavailable) {
		t.Errorf("RecentTransactions err = %v, want ErrPoolUnavailable", err)
	}
}

func TestHandler_ListEventsDegradedReader(t *testing.T) {
	mux := Router(NewService(nil))
	req := httptest.NewRequest(http.MethodGet, "/?limit=5", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body ListEventsResult
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Pagination.Limit != 5 || body.Pagination.Page != 1 || body.Pagination.Total != 0 {
		t.Errorf("pagination = %+v, want page=1 limit=5 total=0", body.Pagination)
	}
}

func TestHandler_ListEventsHonoursFilters(t *testing.T) {
	var captured ListEventsQuery
	stub := stubReader{
		listFn: func(_ context.Context, q ListEventsQuery) (ListEventsResult, error) {
			captured = q
			return ListEventsResult{Data: []EventRow{{ID: 1, EventName: "Swap"}}, Pagination: Pagination{Page: q.Page, Limit: q.Limit, Total: 1, TotalPages: 1}}, nil
		},
	}
	mux := Router(NewService(stub))
	req := httptest.NewRequest(http.MethodGet, "/?page=2&limit=10&eventName=Swap&actorAddress=0xABC&chainId=31337&fromBlock=100&toBlock=200&contractAddress=0xDEF", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if captured.Page != 2 || captured.Limit != 10 {
		t.Errorf("page/limit = %d/%d, want 2/10", captured.Page, captured.Limit)
	}
	if captured.EventName != "Swap" {
		t.Errorf("EventName = %q, want Swap", captured.EventName)
	}
	if captured.ActorAddress != "0xABC" {
		t.Errorf("ActorAddress = %q, want 0xABC (handler does not lowercase)", captured.ActorAddress)
	}
	if captured.ChainID != 31337 {
		t.Errorf("ChainID = %d, want 31337", captured.ChainID)
	}
	if captured.FromBlock == nil || *captured.FromBlock != 100 {
		t.Errorf("FromBlock = %v, want *100", captured.FromBlock)
	}
	if captured.ToBlock == nil || *captured.ToBlock != 200 {
		t.Errorf("ToBlock = %v, want *200", captured.ToBlock)
	}
}

func TestHandler_ListEventsRejectsBadQuery(t *testing.T) {
	mux := Router(NewService(stubReader{}))
	cases := []struct {
		name string
		url  string
	}{
		{"bad page", "/?page=zero"},
		{"page=0", "/?page=0"},
		{"limit too big", "/?limit=10000"},
		{"bad chainId", "/?chainId=abc"},
		{"chainId=0", "/?chainId=0"},
		{"bad fromBlock", "/?fromBlock=NaN"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tc.url, nil)
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestHandler_StatsDegradedReader(t *testing.T) {
	mux := Router(NewService(nil))
	req := httptest.NewRequest(http.MethodGet, "/stats", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body FullStats
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.LatestBlock != "0" || body.IndexerBlock != "0" {
		t.Errorf("body = %+v, want zeroed strings", body)
	}
	if body.EventCounts == nil {
		t.Errorf("EventCounts should be [] not nil for empty stats")
	}
}

func TestHandler_TxHashValidation(t *testing.T) {
	mux := Router(NewService(stubReader{}))

	// Valid 32-byte hash should reach handler.
	good := "/tx/0x" + strRepeat("a", 64)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, good, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for valid hash (body=%s)", rec.Code, rec.Body.String())
	}

	// Too short.
	bad := "/tx/0xabcd"
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, bad, nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for short hash", rec.Code)
	}
}

func TestHandler_UserAddressValidation(t *testing.T) {
	mux := Router(NewService(stubReader{}))

	good := "/user/0x" + strRepeat("a", 40)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, good, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}

	bad := "/user/not-an-address"
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, bad, nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for bad address", rec.Code)
	}
}

func TestHandler_RecentTxsDegraded(t *testing.T) {
	mux := Router(NewService(nil))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/transactions", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body []TransactionRow
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body) != 0 {
		t.Errorf("body = %+v, want empty", body)
	}
}

func TestHandler_UserEventsPassesLimit(t *testing.T) {
	var capturedLimit int
	var capturedAddr string
	stub := stubReader{
		byUserFn: func(_ context.Context, addr string, _ int, limit int) ([]EventRow, error) {
			capturedAddr = addr
			capturedLimit = limit
			return []EventRow{}, nil
		},
	}
	mux := Router(NewService(stub))
	addr := "0x" + strRepeat("a", 40)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/user/"+addr+"?limit=7", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if capturedLimit != 7 {
		t.Errorf("limit = %d, want 7", capturedLimit)
	}
	if capturedAddr != addr {
		t.Errorf("addr = %q, want %q (handler preserves casing; repo lowercases)", capturedAddr, addr)
	}
}

func TestHandler_DegradesOnPoolErr(t *testing.T) {
	stub := stubReader{
		listFn: func(context.Context, ListEventsQuery) (ListEventsResult, error) {
			return ListEventsResult{}, ErrPoolUnavailable
		},
		statsFn: func(context.Context, int) (FullStats, error) {
			return FullStats{}, ErrPoolUnavailable
		},
	}
	mux := Router(NewService(stub))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("list status = %d, want 200 (degraded)", rec.Code)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/stats", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("stats status = %d, want 200 (degraded)", rec.Code)
	}
}

func strRepeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}
