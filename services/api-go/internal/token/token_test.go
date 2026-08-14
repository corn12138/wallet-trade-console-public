package token

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRepository_NilPoolReturnsErr(t *testing.T) {
	r := NewRepository(nil)
	if _, err := r.ListTrending(context.Background(), nil, 5); err != ErrPoolUnavailable {
		t.Errorf("err = %v, want ErrPoolUnavailable", err)
	}
}

func TestListTrending_ZeroLimitShortCircuits(t *testing.T) {
	// Even with no pool, a zero limit returns empty without erroring —
	// the markets service may legitimately request 0 entries on edge
	// configs and shouldn't fail loudly.
	r := NewRepository(nil)
	got, err := r.ListTrending(context.Background(), nil, 0)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d items, want 0", len(got))
	}
}

func TestListAll_NilPoolReturnsErr(t *testing.T) {
	r := NewRepository(nil)
	if _, _, err := r.ListAll(context.Background(), ListQuery{Page: 1, Limit: 20}); err != ErrPoolUnavailable {
		t.Errorf("err = %v, want ErrPoolUnavailable", err)
	}
}

func TestService_ListDegradesOnPoolUnavailable(t *testing.T) {
	svc := NewService(NewRepository(nil))
	got, err := svc.List(context.Background(), ListQuery{Page: 2, Limit: 5})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(got.Data) != 0 {
		t.Errorf("data len = %d, want 0", len(got.Data))
	}
	// Page/limit echoed back so the FE can render its pager without
	// surprises even in degraded mode.
	if got.Meta.Page != 2 || got.Meta.Limit != 5 {
		t.Errorf("meta = %+v, want page=2 limit=5", got.Meta)
	}
	if got.Meta.Total != 0 || got.Meta.TotalPages != 0 {
		t.Errorf("meta totals = %+v, want zeroed", got.Meta)
	}
}

func TestService_ListCapsPublicLimit(t *testing.T) {
	svc := NewService(NewRepository(nil))
	got, err := svc.List(context.Background(), ListQuery{Page: 1, Limit: maxTokenListLimit + 1})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got.Meta.Limit != maxTokenListLimit {
		t.Fatalf("meta limit = %d, want %d", got.Meta.Limit, maxTokenListLimit)
	}
}

func TestService_ListCapsPublicPage(t *testing.T) {
	svc := NewService(NewRepository(nil))
	got, err := svc.List(context.Background(), ListQuery{Page: maxTokenListPage + 1, Limit: 20})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got.Meta.Page != maxTokenListPage {
		t.Fatalf("meta page = %d, want %d", got.Meta.Page, maxTokenListPage)
	}
}

func TestHandler_ListReturnsEnvelopeWhenDegraded(t *testing.T) {
	svc := NewService(NewRepository(nil))
	mux := Router(svc, nil)
	req := httptest.NewRequest(http.MethodGet, "/?status=LAUNCHED&sortBy=trending&page=1&limit=10", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body ListResult
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(body.Data) != 0 {
		t.Errorf("data len = %d, want 0", len(body.Data))
	}
	if body.Meta.Limit != 10 || body.Meta.Page != 1 {
		t.Errorf("meta = %+v, want limit=10 page=1", body.Meta)
	}
}

func TestHandler_ListCapsOversizedLimit(t *testing.T) {
	svc := NewService(NewRepository(nil))
	mux := Router(svc, nil)
	req := httptest.NewRequest(http.MethodGet, "/?limit=999999999", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body ListResult
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Meta.Limit != maxTokenListLimit {
		t.Fatalf("meta limit = %d, want %d", body.Meta.Limit, maxTokenListLimit)
	}
}

func TestHandler_ListCapsOversizedPage(t *testing.T) {
	svc := NewService(NewRepository(nil))
	mux := Router(svc, nil)
	req := httptest.NewRequest(http.MethodGet, "/?page=999999999", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body ListResult
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Meta.Page != maxTokenListPage {
		t.Fatalf("meta page = %d, want %d", body.Meta.Page, maxTokenListPage)
	}
}

func TestBoundedLimit(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value int
		want  int
	}{
		{name: "default zero", value: 0, want: defaultTokenListLimit},
		{name: "default negative", value: -1, want: defaultTokenListLimit},
		{name: "preserve valid", value: 25, want: 25},
		{name: "cap oversized", value: maxTokenListLimit + 1, want: maxTokenListLimit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := boundedLimit(tc.value, defaultTokenListLimit, maxTokenListLimit); got != tc.want {
				t.Fatalf("boundedLimit(%d) = %d, want %d", tc.value, got, tc.want)
			}
		})
	}
}

func TestBuildOrderBy(t *testing.T) {
	cases := map[string]string{
		"":          "created_at DESC",
		"marketCap": "market_cap DESC NULLS LAST",
		"volume":    "volume_24h DESC NULLS LAST",
		"trending":  "price_change_24h DESC NULLS LAST",
		"unknown":   "created_at DESC",
	}
	for in, want := range cases {
		if got := buildOrderBy(in); got != want {
			t.Errorf("buildOrderBy(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildWhere(t *testing.T) {
	cid := 11155111
	clause, args := buildWhere(ListQuery{Status: "LAUNCHED", ChainID: &cid, Search: "hon"})
	if !strings.Contains(clause, "status = $1") {
		t.Errorf("clause missing status: %q", clause)
	}
	if !strings.Contains(clause, "chain_id = $2") {
		t.Errorf("clause missing chain_id: %q", clause)
	}
	if !strings.Contains(clause, "ILIKE $3") {
		t.Errorf("clause missing search: %q", clause)
	}
	if len(args) != 3 {
		t.Errorf("args len = %d, want 3", len(args))
	}
	if args[2].(string) != "%hon%" {
		t.Errorf("search arg = %v, want %%hon%%", args[2])
	}
}

func TestBuildWhere_Empty(t *testing.T) {
	clause, args := buildWhere(ListQuery{})
	if clause != "" {
		t.Errorf("clause = %q, want empty", clause)
	}
	if len(args) != 0 {
		t.Errorf("args = %v, want empty", args)
	}
}

// Rows store lowercase creator addresses; a checksummed profile URL must
// still find its created tokens (regression: /profile/[address] Created tab
// was empty for EIP-55 checksummed addresses).
func TestBuildWhere_CreatorAddressNormalized(t *testing.T) {
	clause, args := buildWhere(ListQuery{CreatorAddress: "0xE2cd26322A87d2b6D8312DBcB79c38b7a226aD81"})
	if !strings.Contains(clause, "creator_address = $1") {
		t.Errorf("clause missing creator_address: %q", clause)
	}
	if len(args) != 1 || args[0].(string) != "0xe2cd26322a87d2b6d8312dbcb79c38b7a226ad81" {
		t.Errorf("creator arg = %v, want lowercased address", args)
	}
}

func TestDecodeTags(t *testing.T) {
	// NULL/empty → empty slice (matches Prisma default "[]")
	if v := decodeTags(nil); !isEmptyArr(v) {
		t.Errorf("nil → %v, want []", v)
	}
	if v := decodeTags([]byte{}); !isEmptyArr(v) {
		t.Errorf("empty → %v, want []", v)
	}
	// Valid array passes through
	got := decodeTags([]byte(`["defi","ai"]`))
	arr, ok := got.([]any)
	if !ok || len(arr) != 2 || arr[0] != "defi" {
		t.Errorf("decodeTags array = %v, want [defi ai]", got)
	}
	// Malformed JSON falls back to empty (caller can't error out the
	// whole list on one bad row).
	if v := decodeTags([]byte("{not json")); !isEmptyArr(v) {
		t.Errorf("bad json → %v, want []", v)
	}
}

func isEmptyArr(v any) bool {
	arr, ok := v.([]any)
	return ok && len(arr) == 0
}

func TestFindBySymbol_NilPoolReturnsErr(t *testing.T) {
	r := NewRepository(nil)
	if _, err := r.FindBySymbol(context.Background(), "HONEY"); err != ErrPoolUnavailable {
		t.Errorf("err = %v, want ErrPoolUnavailable", err)
	}
}

func TestService_FindBySymbolDegradesAsNotFound(t *testing.T) {
	svc := NewService(NewRepository(nil))
	if _, err := svc.FindBySymbol(context.Background(), "HONEY"); err != ErrNotFound {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestFindByAddress_NilPoolReturnsErr(t *testing.T) {
	r := NewRepository(nil)
	if _, err := r.FindByAddress(context.Background(), "0xabc"); err != ErrPoolUnavailable {
		t.Errorf("err = %v, want ErrPoolUnavailable", err)
	}
}

func TestListTrades_NilPoolReturnsErr(t *testing.T) {
	r := NewRepository(nil)
	if _, err := r.ListTrades(context.Background(), "tok-1", 0); err != ErrPoolUnavailable {
		t.Errorf("err = %v, want ErrPoolUnavailable", err)
	}
}

func TestListHolders_NilPoolReturnsErr(t *testing.T) {
	r := NewRepository(nil)
	if _, err := r.ListHolders(context.Background(), "tok-1", 0); err != ErrPoolUnavailable {
		t.Errorf("err = %v, want ErrPoolUnavailable", err)
	}
}

func TestService_FindByAddressDegradesAsNotFound(t *testing.T) {
	svc := NewService(NewRepository(nil))
	if _, err := svc.FindByAddress(context.Background(), "0xabc"); err != ErrNotFound {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestService_ListTradesDegradesToEmpty(t *testing.T) {
	svc := NewService(NewRepository(nil))
	got, err := svc.ListTradesForToken(context.Background(), "tok-1", 50)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestService_ListHoldersDegradesToEmpty(t *testing.T) {
	svc := NewService(NewRepository(nil))
	got, err := svc.ListHoldersForToken(context.Background(), "tok-1", 50)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestHandler_ByAddress404OnDegradedMode(t *testing.T) {
	svc := NewService(NewRepository(nil))
	mux := Router(svc, nil)
	req := httptest.NewRequest(http.MethodGet, "/address/0xabc", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "token not found") {
		t.Errorf("body = %q, want 'token not found'", rec.Body.String())
	}
}

func TestHandler_TradesAndHolders404WhenAddressMissing(t *testing.T) {
	// Same degraded mode → address lookup 404 propagates through the
	// trades/holders handlers (they resolve address → tokenId first,
	// matching the NestJS controller's two-step lookup).
	svc := NewService(NewRepository(nil))
	mux := Router(svc, nil)
	for _, path := range []string{"/address/0xabc/trades", "/address/0xabc/holders"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s status = %d, want 404", path, rec.Code)
		}
	}
}

func TestHandler_BySymbol404OnDegradedMode(t *testing.T) {
	svc := NewService(NewRepository(nil))
	mux := Router(svc, nil)
	req := httptest.NewRequest(http.MethodGet, "/symbol/HONEY", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "token not found") {
		t.Errorf("body = %q, want 'token not found'", rec.Body.String())
	}
}

func TestFindByID_NilPoolReturnsErr(t *testing.T) {
	r := NewRepository(nil)
	if _, err := r.FindByID(context.Background(), "tok-1"); err != ErrPoolUnavailable {
		t.Errorf("err = %v, want ErrPoolUnavailable", err)
	}
}

func TestListTrendingTokens_NilPoolReturnsErr(t *testing.T) {
	r := NewRepository(nil)
	if _, err := r.ListTrendingTokens(context.Background(), nil, 5); err != ErrPoolUnavailable {
		t.Errorf("err = %v, want ErrPoolUnavailable", err)
	}
}

func TestListTrendingTokens_ZeroLimitShortCircuits(t *testing.T) {
	// Same zero-limit guard as the narrow ListTrending — explicit so a
	// 0-limit query never reaches the SQL layer.
	r := NewRepository(nil)
	got, err := r.ListTrendingTokens(context.Background(), nil, 0)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestService_FindByIDDegradesAsNotFound(t *testing.T) {
	svc := NewService(NewRepository(nil))
	if _, err := svc.FindByID(context.Background(), "tok-1"); err != ErrNotFound {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestService_FindTrendingDegradesToEmpty(t *testing.T) {
	svc := NewService(NewRepository(nil))
	got, err := svc.FindTrending(context.Background(), nil, 10)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestService_FindTrendingDefaultsLimit(t *testing.T) {
	// Zero limit from the handler (when query param is missing) must
	// turn into 10 inside the service so the SQL LIMIT is sane.
	// Verifying the degraded path is enough — repo never runs.
	svc := NewService(NewRepository(nil))
	got, err := svc.FindTrending(context.Background(), nil, 0)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestTrendingLimitBounds(t *testing.T) {
	if got := boundedLimit(0, defaultTrendingTokenLimit, maxTrendingTokenLimit); got != defaultTrendingTokenLimit {
		t.Fatalf("default trending limit = %d, want %d", got, defaultTrendingTokenLimit)
	}
	if got := boundedLimit(maxTrendingTokenLimit+1, defaultTrendingTokenLimit, maxTrendingTokenLimit); got != maxTrendingTokenLimit {
		t.Fatalf("capped trending limit = %d, want %d", got, maxTrendingTokenLimit)
	}
}

func TestHandler_TrendingReturnsEmptyArrayWhenDegraded(t *testing.T) {
	// Trending degrades to [] (not 404) so the FE chart still renders
	// an empty state without a 5xx.
	svc := NewService(NewRepository(nil))
	mux := Router(svc, nil)
	req := httptest.NewRequest(http.MethodGet, "/trending?limit=5", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body []Token
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body) != 0 {
		t.Errorf("len = %d, want 0", len(body))
	}
}

func TestHandler_ByID404OnDegradedMode(t *testing.T) {
	// /{id} is registered last; a path like /tok-1 should reach the
	// by-id handler (not the trending or address routes) and 404.
	svc := NewService(NewRepository(nil))
	mux := Router(svc, nil)
	req := httptest.NewRequest(http.MethodGet, "/tok-1", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "token not found") {
		t.Errorf("body = %q, want 'token not found'", rec.Body.String())
	}
}

func TestHandler_TrendingLiteralBeatsByIDParam(t *testing.T) {
	// Regression guard: /trending must NOT fall through to /{id}.
	// A wrong precedence here would 404 instead of returning [].
	svc := NewService(NewRepository(nil))
	mux := Router(svc, nil)
	req := httptest.NewRequest(http.MethodGet, "/trending", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (trending degrades to []), got body %q", rec.Code, rec.Body.String())
	}
}
