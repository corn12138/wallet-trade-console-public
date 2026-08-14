package papertrade

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type stubStore struct {
	createFn func(context.Context, CreateInput) (Order, error)
	listFn   func(context.Context, ListQuery) ([]Order, error)
}

func (s stubStore) Create(ctx context.Context, in CreateInput) (Order, error) {
	if s.createFn != nil {
		return s.createFn(ctx, in)
	}
	return Order{ID: "ord-1", Account: in.Account, Token: in.Token, Status: "NEW"}, nil
}

func (s stubStore) List(ctx context.Context, q ListQuery) ([]Order, error) {
	if s.listFn != nil {
		return s.listFn(ctx, q)
	}
	return []Order{}, nil
}

func TestRepository_NilPoolReturnsErr(t *testing.T) {
	r := NewRepository(nil)
	if _, err := r.Create(context.Background(), CreateInput{}); !errors.Is(err, ErrPoolUnavailable) {
		t.Errorf("Create err = %v, want ErrPoolUnavailable", err)
	}
	if _, err := r.List(context.Background(), ListQuery{}); !errors.Is(err, ErrPoolUnavailable) {
		t.Errorf("List err = %v, want ErrPoolUnavailable", err)
	}
}

func TestHandler_CreateRejectsInvalidInput(t *testing.T) {
	mux := Router(NewService(stubStore{}))
	cases := []struct {
		name string
		body string
	}{
		{"bad json", `{`},
		{"bad account", `{"account":"not-an-addr","token":"ETH","sizeDelta":"1","type":"MARKET","isLong":true}`},
		{"missing token", `{"account":"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","sizeDelta":"1","type":"MARKET","isLong":true}`},
		{"missing sizeDelta", `{"account":"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","token":"ETH","type":"MARKET","isLong":true}`},
		{"bad type", `{"account":"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","token":"ETH","sizeDelta":"1","type":"WEIRD","isLong":true}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestHandler_CreateUppercasesType(t *testing.T) {
	// Note: the handler trims+validates the address (regex allows mixed
	// case) and uppercases Type. Account-lowercasing happens inside
	// Repository.Create so that the DB normalizes — the handler keeps
	// the original casing on the wire. Verified here via the stub.
	var captured CreateInput
	stub := stubStore{
		createFn: func(_ context.Context, in CreateInput) (Order, error) {
			captured = in
			return Order{ID: "ok", Account: in.Account, Token: in.Token, Type: in.Type, Status: "NEW"}, nil
		},
	}
	mux := Router(NewService(stub))

	body := `{"account":"0xABBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB","token":"ETH","sizeDelta":"100","type":"limit","isLong":true}`
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body=%s)", rec.Code, rec.Body.String())
	}
	if captured.Type != "LIMIT" {
		t.Errorf("Type = %q, want LIMIT (handler uppercases)", captured.Type)
	}
	if captured.Account != "0xABBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB" {
		t.Errorf("Account = %q, handler should pass through (repo lowercases)", captured.Account)
	}
}

func TestHandler_CreateDegradedReturns503(t *testing.T) {
	mux := Router(NewService(nil))
	body := `{"account":"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","token":"ETH","sizeDelta":"1","type":"MARKET","isLong":true}`
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}

	// Also when the store returns ErrPoolUnavailable from inside.
	stub := stubStore{
		createFn: func(context.Context, CreateInput) (Order, error) {
			return Order{}, ErrPoolUnavailable
		},
	}
	mux = Router(NewService(stub))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 from store", rec.Code)
	}
}

func TestHandler_ListDegradedReturnsEmpty200(t *testing.T) {
	mux := Router(NewService(nil))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body []Order
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body) != 0 {
		t.Errorf("body = %+v, want empty", body)
	}
}

func TestHandler_ListPassesFilters(t *testing.T) {
	var captured ListQuery
	stub := stubStore{
		listFn: func(_ context.Context, q ListQuery) ([]Order, error) {
			captured = q
			return []Order{}, nil
		},
	}
	mux := Router(NewService(stub))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/?account=0xABC&symbol=ETH&chainId=31337", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if captured.Account != "0xABC" {
		t.Errorf("Account = %q, want 0xABC (handler preserves casing; repo lowercases)", captured.Account)
	}
	if captured.Symbol != "ETH" {
		t.Errorf("Symbol = %q, want ETH", captured.Symbol)
	}
	if captured.ChainID == nil || *captured.ChainID != 31337 {
		t.Errorf("ChainID = %v, want *31337", captured.ChainID)
	}
}

func TestHandler_ListBadChainIdNotFatal(t *testing.T) {
	stub := stubStore{}
	mux := Router(NewService(stub))
	// NestJS parseNumberParam returns undefined for "abc" — not 400.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/?chainId=abc", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (bad chainId is ignored not rejected)", rec.Code)
	}
}
