package staking

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
	listFn    func(context.Context, PoolFilters) ([]PoolView, error)
	findFn    func(context.Context, string) (PoolView, error)
	createFn  func(context.Context, CreatePoolInput) (PoolView, error)
	updateFn  func(context.Context, string, UpdatePoolInput) (PoolView, error)
	statsFn   func(context.Context) (PoolStats, error)
	stakesFn  func(context.Context, string) ([]UserStake, error)
	stakeFn   func(context.Context, RecordStakeInput) (UserStake, error)
	unstakeFn func(context.Context, string) (UserStake, error)
}

func (s stubStore) ListAllPools(ctx context.Context, f PoolFilters) ([]PoolView, error) {
	if s.listFn != nil {
		return s.listFn(ctx, f)
	}
	return []PoolView{}, nil
}
func (s stubStore) FindPool(ctx context.Context, id string) (PoolView, error) {
	if s.findFn != nil {
		return s.findFn(ctx, id)
	}
	return PoolView{ID: id}, nil
}
func (s stubStore) CreatePool(ctx context.Context, in CreatePoolInput) (PoolView, error) {
	if s.createFn != nil {
		return s.createFn(ctx, in)
	}
	return PoolView{ID: "new", Name: in.Name, ChainID: in.ChainID, PoolType: in.PoolType, TokenAddress: in.TokenAddress, Status: "active"}, nil
}
func (s stubStore) UpdatePool(ctx context.Context, id string, in UpdatePoolInput) (PoolView, error) {
	if s.updateFn != nil {
		return s.updateFn(ctx, id, in)
	}
	return PoolView{ID: id}, nil
}
func (s stubStore) GetPoolStats(ctx context.Context) (PoolStats, error) {
	if s.statsFn != nil {
		return s.statsFn(ctx)
	}
	return PoolStats{}, nil
}
func (s stubStore) ListActiveUserStakes(ctx context.Context, addr string) ([]UserStake, error) {
	if s.stakesFn != nil {
		return s.stakesFn(ctx, addr)
	}
	return []UserStake{}, nil
}
func (s stubStore) RecordStake(ctx context.Context, in RecordStakeInput) (UserStake, error) {
	if s.stakeFn != nil {
		return s.stakeFn(ctx, in)
	}
	return UserStake{ID: "stake-1", UserAddress: in.UserAddress, PoolID: in.PoolID, Amount: "100"}, nil
}
func (s stubStore) RecordUnstake(ctx context.Context, id string) (UserStake, error) {
	if s.unstakeFn != nil {
		return s.unstakeFn(ctx, id)
	}
	return UserStake{ID: id}, nil
}

func TestRepository_NilPoolReturnsErr(t *testing.T) {
	r := NewRepository(nil)
	checks := []struct {
		name string
		fn   func() error
	}{
		{"ListActivePools", func() error { _, e := r.ListActivePools(context.Background(), nil); return e }},
		{"ListAllPools", func() error { _, e := r.ListAllPools(context.Background(), PoolFilters{}); return e }},
		{"FindPool", func() error { _, e := r.FindPool(context.Background(), "id"); return e }},
		{"CreatePool", func() error { _, e := r.CreatePool(context.Background(), CreatePoolInput{}); return e }},
		{"UpdatePool", func() error { _, e := r.UpdatePool(context.Background(), "id", UpdatePoolInput{}); return e }},
		{"GetPoolStats", func() error { _, e := r.GetPoolStats(context.Background()); return e }},
		{"ListUserStakes", func() error { _, e := r.ListUserStakes(context.Background(), "0xabc"); return e }},
		{"ListActiveUserStakes", func() error { _, e := r.ListActiveUserStakes(context.Background(), "0xabc"); return e }},
		{"RecordStake", func() error { _, e := r.RecordStake(context.Background(), RecordStakeInput{}); return e }},
		{"RecordUnstake", func() error { _, e := r.RecordUnstake(context.Background(), "id"); return e }},
	}
	for _, c := range checks {
		if err := c.fn(); !errors.Is(err, ErrPoolUnavailable) {
			t.Errorf("%s err = %v, want ErrPoolUnavailable", c.name, err)
		}
	}
}

func TestHandler_ListPoolsDegraded200Empty(t *testing.T) {
	mux := Router(NewService(nil), nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/pools", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body []PoolView
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if len(body) != 0 {
		t.Errorf("body = %+v, want empty", body)
	}
}

func TestHandler_ListPoolsPassesFilters(t *testing.T) {
	var captured PoolFilters
	stub := stubStore{
		listFn: func(_ context.Context, f PoolFilters) ([]PoolView, error) {
			captured = f
			return []PoolView{}, nil
		},
	}
	mux := Router(NewService(stub), nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/pools?poolType=staking&status=active&chainId=31337", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if captured.PoolType == nil || *captured.PoolType != "staking" {
		t.Errorf("PoolType = %v, want *staking", captured.PoolType)
	}
	if captured.Status == nil || *captured.Status != "active" {
		t.Errorf("Status = %v, want *active", captured.Status)
	}
	if captured.ChainID == nil || *captured.ChainID != 31337 {
		t.Errorf("ChainID = %v, want *31337", captured.ChainID)
	}
}

func TestHandler_FindPoolNotFound(t *testing.T) {
	stub := stubStore{findFn: func(context.Context, string) (PoolView, error) {
		return PoolView{}, ErrPoolNotFound
	}}
	mux := Router(NewService(stub), nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/pools/missing", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestHandler_FindPoolDegraded503(t *testing.T) {
	mux := Router(NewService(nil), nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/pools/abc", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

func TestHandler_CreatePoolValidation(t *testing.T) {
	mux := Router(NewService(stubStore{}), nil)
	cases := []struct {
		name string
		body string
	}{
		{"missing name", `{"chainId":1,"poolType":"staking","tokenAddress":"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`},
		{"missing poolType", `{"name":"Pool","chainId":1,"tokenAddress":"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`},
		{"missing token addr", `{"name":"Pool","chainId":1,"poolType":"staking"}`},
		{"chainId 0", `{"name":"Pool","chainId":0,"poolType":"staking","tokenAddress":"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`},
		{"bad token addr", `{"name":"Pool","chainId":1,"poolType":"staking","tokenAddress":"not-an-addr"}`},
		{"bad reward token", `{"name":"Pool","chainId":1,"poolType":"staking","tokenAddress":"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","rewardToken":"junk"}`},
		{"bad json", `{`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/pools", strings.NewReader(c.body)))
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestHandler_CreatePoolHappyPath(t *testing.T) {
	mux := Router(NewService(stubStore{}), nil)
	body := `{"name":"P","chainId":31337,"poolType":"staking","tokenAddress":"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/pools", strings.NewReader(body)))
	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201 (body=%s)", rec.Code, rec.Body.String())
	}
}

func TestHandler_UpdatePoolNotFound(t *testing.T) {
	stub := stubStore{updateFn: func(context.Context, string, UpdatePoolInput) (PoolView, error) {
		return PoolView{}, ErrPoolNotFound
	}}
	mux := Router(NewService(stub), nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/pools/missing", strings.NewReader(`{"status":"ended"}`)))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestHandler_UserStakesAddressValidation(t *testing.T) {
	mux := Router(NewService(stubStore{}), nil)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/user/0xnot", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (bad addr)", rec.Code)
	}

	rec = httptest.NewRecorder()
	good := "/user/0x" + strings.Repeat("a", 40)
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, good, nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestHandler_RecordStakeValidation(t *testing.T) {
	mux := Router(NewService(stubStore{}), nil)
	cases := []struct {
		name string
		body string
	}{
		{"bad addr", `{"userAddress":"x","poolId":"p","amount":1}`},
		{"missing pool", `{"userAddress":"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","poolId":"","amount":1}`},
		{"amount <= 0", `{"userAddress":"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","poolId":"p","amount":0}`},
		{"bad json", `{`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/stake", strings.NewReader(c.body)))
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestHandler_RecordStakeHappyPath(t *testing.T) {
	var captured RecordStakeInput
	stub := stubStore{stakeFn: func(_ context.Context, in RecordStakeInput) (UserStake, error) {
		captured = in
		return UserStake{ID: "s1", UserAddress: in.UserAddress, PoolID: in.PoolID, Amount: "1"}, nil
	}}
	mux := Router(NewService(stub), nil)
	body := `{"userAddress":"0xAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","poolId":"p1","amount":1.5}`
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/stake", strings.NewReader(body)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body=%s)", rec.Code, rec.Body.String())
	}
	if captured.UserAddress != "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("UserAddress = %q, want lowercased (handler does the lowercasing)", captured.UserAddress)
	}
}

func TestHandler_RecordUnstakeNotFound(t *testing.T) {
	stub := stubStore{unstakeFn: func(context.Context, string) (UserStake, error) {
		return UserStake{}, ErrPoolNotFound
	}}
	mux := Router(NewService(stub), nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/unstake/missing", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}
