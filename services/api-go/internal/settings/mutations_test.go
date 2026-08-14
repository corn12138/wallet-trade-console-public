package settings

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
)

func TestBuildHiddenAssetKey(t *testing.T) {
	// Truth-table check against common/utils/web3-hidden-asset.ts.
	// Format: "<chainId>:<scope>:<identity>"
	cases := []struct {
		name    string
		chainID int
		asset   *string
		symbol  *string
		wallet  *string
		want    string
	}{
		{
			name:    "asset address wins over symbol",
			chainID: 1,
			asset:   ptrStr("0x000000000000000000000000000000000000aaaa"),
			symbol:  ptrStr("ETH"),
			want:    "1:all-wallets:0x000000000000000000000000000000000000aaaa",
		},
		{
			name:    "symbol fallback when no asset address",
			chainID: 11155111,
			symbol:  ptrStr("eth"), // lowercase input
			want:    "11155111:all-wallets:symbol:ETH",
		},
		{
			name:    "wallet scope",
			chainID: 1,
			asset:   ptrStr("0x000000000000000000000000000000000000aaaa"),
			wallet:  ptrStr("0x000000000000000000000000000000000000beef"),
			want:    "1:0x000000000000000000000000000000000000beef:0x000000000000000000000000000000000000aaaa",
		},
		{
			name:    "empty symbol → UNKNOWN",
			chainID: 1,
			symbol:  ptrStr("   "),
			want:    "1:all-wallets:symbol:UNKNOWN",
		},
		{
			name:    "neither asset nor symbol → UNKNOWN identity",
			chainID: 137,
			want:    "137:all-wallets:symbol:UNKNOWN",
		},
	}
	for _, tc := range cases {
		got := buildHiddenAssetKey(tc.chainID, tc.asset, tc.symbol, tc.wallet)
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestNormalizeOptionalAddress(t *testing.T) {
	// nil + empty + whitespace all return (nil, nil)
	for _, in := range []*string{nil, ptrStr(""), ptrStr("   ")} {
		got, err := normalizeOptionalAddress(in)
		if got != nil || err != nil {
			t.Errorf("got (%v, %v), want (nil, nil)", got, err)
		}
	}
	// invalid shape → error
	if _, err := normalizeOptionalAddress(ptrStr("0xnotanaddress")); err == nil {
		t.Errorf("expected error on bad address")
	}
	// happy path lowercases
	got, err := normalizeOptionalAddress(ptrStr("0xABCD000000000000000000000000000000000000"))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got == nil || *got != "0xabcd000000000000000000000000000000000000" {
		t.Errorf("got %v, want lowercase 0xabcd...", got)
	}
}

func TestNormalizeCurrency(t *testing.T) {
	// uppercase + trim
	got := normalizeCurrency(ptrStr("  usd  "))
	if got == nil || *got != "USD" {
		t.Errorf("got %v, want USD", got)
	}
	// nil passthrough
	if got := normalizeCurrency(nil); got != nil {
		t.Errorf("got %v, want nil", got)
	}
	// empty after trim → nil
	if got := normalizeCurrency(ptrStr("   ")); got != nil {
		t.Errorf("got %v, want nil", got)
	}
}

func TestUpsertPreferences_NilPoolReturnsErr(t *testing.T) {
	r := NewRepository(nil)
	_, err := r.UpsertPreferences(context.Background(), UpdatePreferencesRequest{
		OwnerAddress: "0x000000000000000000000000000000000000bEEF",
	})
	if err != ErrMutationRequiresPool {
		t.Errorf("err = %v, want ErrMutationRequiresPool", err)
	}
}

func TestUpsertPreferences_InvalidOwnerAddress(t *testing.T) {
	r := NewRepository(nil)
	_, err := r.UpsertPreferences(context.Background(), UpdatePreferencesRequest{
		OwnerAddress: "bad",
	})
	if err != ErrInvalidOwnerAddr {
		t.Errorf("err = %v, want ErrInvalidOwnerAddr", err)
	}
}

func TestUpsertHiddenAsset_RequiresSymbolOrAsset(t *testing.T) {
	r := NewRepository(nil)
	_, err := r.UpsertHiddenAsset(context.Background(), HideAssetRequest{
		OwnerAddress: "0x000000000000000000000000000000000000bEEF",
		ChainID:      1,
	})
	if err != ErrSymbolOrAssetReqd {
		t.Errorf("err = %v, want ErrSymbolOrAssetReqd", err)
	}
}

func TestUpsertHiddenAsset_BadWalletAddress(t *testing.T) {
	r := NewRepository(nil)
	_, err := r.UpsertHiddenAsset(context.Background(), HideAssetRequest{
		OwnerAddress:  "0x000000000000000000000000000000000000bEEF",
		ChainID:       1,
		Symbol:        "ETH",
		WalletAddress: ptrStr("bad"),
	})
	if err != ErrInvalidWalletAddr {
		t.Errorf("err = %v, want ErrInvalidWalletAddr", err)
	}
}

func TestUpsertHiddenAsset_BadAssetAddress(t *testing.T) {
	r := NewRepository(nil)
	_, err := r.UpsertHiddenAsset(context.Background(), HideAssetRequest{
		OwnerAddress: "0x000000000000000000000000000000000000bEEF",
		ChainID:      1,
		Symbol:       "ETH",
		AssetAddress: ptrStr("bad"),
	})
	if err != ErrInvalidAssetAddr {
		t.Errorf("err = %v, want ErrInvalidAssetAddr", err)
	}
}

func TestDeleteHiddenAsset_NilPoolReturnsErr(t *testing.T) {
	r := NewRepository(nil)
	err := r.DeleteHiddenAsset(context.Background(), "0x000000000000000000000000000000000000bEEF", "abc")
	if err != ErrMutationRequiresPool {
		t.Errorf("err = %v, want ErrMutationRequiresPool", err)
	}
}

func TestDeleteHiddenAsset_BadOwner(t *testing.T) {
	r := NewRepository(nil)
	err := r.DeleteHiddenAsset(context.Background(), "bad-owner", "abc")
	if err != ErrInvalidOwnerAddr {
		t.Errorf("err = %v, want ErrInvalidOwnerAddr", err)
	}
}

// ---------- handler tests ----------

func mountMutationRouter(t *testing.T) http.Handler {
	t.Helper()
	svc := NewService(NewRepository(nil))
	openAuth := func(next http.Handler) http.Handler { return next }
	return Router(svc, openAuth)
}

func TestHandler_PutPreferencesUnauthorizedWithoutContext(t *testing.T) {
	mux := mountMutationRouter(t)
	req := httptest.NewRequest(http.MethodPut, "/preferences", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestHandler_PutPreferencesOwnerMismatchIs403(t *testing.T) {
	mux := mountMutationRouter(t)
	body := `{"ownerAddress":"0x0000000000000000000000000000000000000001"}`
	req := httptest.NewRequest(http.MethodPut, "/preferences", strings.NewReader(body))
	req = req.WithContext(auth.WithAddress(req.Context(), "0x000000000000000000000000000000000000bEEF"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestHandler_PutPreferencesNilPoolIs503(t *testing.T) {
	mux := mountMutationRouter(t)
	body := `{"theme":"dark"}`
	req := httptest.NewRequest(http.MethodPut, "/preferences", strings.NewReader(body))
	req = req.WithContext(auth.WithAddress(req.Context(), "0x000000000000000000000000000000000000bEEF"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandler_PostHiddenAssetsMissingSymbolAndAsset(t *testing.T) {
	mux := mountMutationRouter(t)
	body := `{"chainId":1}`
	req := httptest.NewRequest(http.MethodPost, "/hidden-assets", strings.NewReader(body))
	req = req.WithContext(auth.WithAddress(req.Context(), "0x000000000000000000000000000000000000bEEF"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "symbol or assetAddress") {
		t.Errorf("body = %q, want 'symbol or assetAddress'", rec.Body.String())
	}
}

func TestHandler_DeleteHiddenAssetNilPoolIs503(t *testing.T) {
	mux := mountMutationRouter(t)
	req := httptest.NewRequest(http.MethodDelete, "/hidden-assets/abc", nil)
	req = req.WithContext(auth.WithAddress(req.Context(), "0x000000000000000000000000000000000000bEEF"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandler_MutationsDontMountWhenAuthMiddlewareIsNil(t *testing.T) {
	// Same belt-and-braces as the wallets mutations: nil middleware =
	// guarded routes don't mount. PUT /preferences should land on chi's
	// method-not-allowed / not-found, not on the handler.
	svc := NewService(NewRepository(nil))
	mux := Router(svc, nil)
	req := httptest.NewRequest(http.MethodPut, "/preferences", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404/405 (route should not mount)", rec.Code)
	}
}

func ptrStr(s string) *string { return &s }
