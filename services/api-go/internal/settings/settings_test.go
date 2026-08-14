package settings

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
)

const validAddr = "0x1234567890abcdef1234567890abcdef12345678"

func TestRepository_NilPoolReturnsErr(t *testing.T) {
	r := NewRepository(nil)
	if _, err := r.FindUserPreferences(context.Background(), validAddr); err != ErrPoolUnavailable {
		t.Errorf("FindUserPreferences err = %v, want ErrPoolUnavailable", err)
	}
	if _, err := r.FindNotificationPreferences(context.Background(), validAddr); err != ErrPoolUnavailable {
		t.Errorf("FindNotificationPreferences err = %v, want ErrPoolUnavailable", err)
	}
	if _, err := r.ListHiddenAssets(context.Background(), validAddr, nil); err != ErrPoolUnavailable {
		t.Errorf("ListHiddenAssets err = %v, want ErrPoolUnavailable", err)
	}
}

func TestNormalizeIfValid(t *testing.T) {
	cases := map[string]struct {
		want string
		ok   bool
	}{
		"":                      {"", false},
		"0x":                    {"", false},
		"not-an-addr":           {"", false},
		"0xZZZZ":                {"", false},
		"0x1234":                {"", false},
		strings.Repeat("0", 40): {"", false}, // missing 0x
		validAddr:               {validAddr, true},
		"  " + validAddr:        {validAddr, true}, // trimmed
		"0xABCDEF1234567890ABCDEF1234567890ABCDEF12": {"0xabcdef1234567890abcdef1234567890abcdef12", true}, // lowered
	}
	for in, want := range cases {
		got, ok := normalizeIfValid(in)
		if ok != want.ok || got != want.want {
			t.Errorf("normalizeIfValid(%q) = (%q, %v), want (%q, %v)", in, got, ok, want.want, want.ok)
		}
	}
}

func TestService_GetPreferencesEmptyAddressReturnsDefaults(t *testing.T) {
	svc := NewService(NewRepository(nil))
	got, err := svc.GetPreferences(context.Background(), "")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got.OwnerAddress != nil {
		t.Errorf("ownerAddress = %v, want nil for empty input", got.OwnerAddress)
	}
	if got.Source != "default" {
		t.Errorf("source = %q, want default", got.Source)
	}
	if got.Theme != "system" || got.FiatCurrency != "USD" {
		t.Errorf("defaults wrong: %+v", got)
	}
}

func TestService_GetPreferencesInvalidAddressReturnsDefaults(t *testing.T) {
	svc := NewService(NewRepository(nil))
	got, err := svc.GetPreferences(context.Background(), "not-an-address")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got.OwnerAddress != nil || got.Source != "default" {
		t.Errorf("got %+v, want default response with nil owner", got)
	}
}

func TestService_GetPreferencesPoolUnavailableDegradesWithAddress(t *testing.T) {
	svc := NewService(NewRepository(nil))
	// Valid address + nil pool: degrade to defaults but echo the
	// normalized address back so the FE can still render the owner
	// context.
	got, err := svc.GetPreferences(context.Background(), validAddr)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got.OwnerAddress == nil || *got.OwnerAddress != validAddr {
		t.Errorf("ownerAddress = %v, want %q", got.OwnerAddress, validAddr)
	}
	if got.Source != "default" {
		t.Errorf("source = %q, want default", got.Source)
	}
}

func TestService_GetNotificationPreferencesDefaults(t *testing.T) {
	svc := NewService(NewRepository(nil))
	got, err := svc.GetNotificationPreferences(context.Background(), validAddr)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got.Source != "default" {
		t.Errorf("source = %q, want default", got.Source)
	}
	if !got.SecurityAlerts || !got.TxUpdates || !got.MarketAlerts || got.ProductUpdates {
		t.Errorf("notification defaults wrong: %+v", got)
	}
}

func TestService_ListHiddenAssetsEmptyAddressReturnsEmpty(t *testing.T) {
	svc := NewService(NewRepository(nil))
	got, err := svc.ListHiddenAssets(context.Background(), "", nil)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestService_ListHiddenAssetsDegradesToEmpty(t *testing.T) {
	svc := NewService(NewRepository(nil))
	got, err := svc.ListHiddenAssets(context.Background(), validAddr, nil)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

// Settings reads are web3-guarded + owner-pinned (parity with the NestJS
// class-level Web3AuthGuard + resolveAuthenticatedOwnerAddress). openAuth stands
// in for the configured middleware; the handler still owner-pins via
// auth.ResolveOwner, so the auth address must be in the request context.
func TestHandler_PreferencesGuardedAndOwnerPinned(t *testing.T) {
	svc := NewService(NewRepository(nil))
	openAuth := func(next http.Handler) http.Handler { return next }
	mux := Router(svc, openAuth)

	// No authenticated address → 401 (must not serve owner-private data publicly).
	req := httptest.NewRequest(http.MethodGet, "/preferences?ownerAddress="+validAddr, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no-auth GET /preferences = %d, want 401", rec.Code)
	}

	// Authenticated, no ?ownerAddress → defaults pinned to the JWT wallet (200).
	req = httptest.NewRequest(http.MethodGet, "/preferences", nil)
	req = req.WithContext(auth.WithAddress(req.Context(), validAddr))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("authed GET /preferences = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body UserPreferences
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Source != "default" {
		t.Errorf("source = %q, want default", body.Source)
	}
	if body.OwnerAddress == nil || *body.OwnerAddress != validAddr {
		t.Errorf("ownerAddress = %v, want %q (pinned to JWT wallet)", body.OwnerAddress, validAddr)
	}

	// Authenticated but ?ownerAddress mismatches the JWT wallet → 403 (no IDOR).
	other := "0x000000000000000000000000000000000000bEEF"
	req = httptest.NewRequest(http.MethodGet, "/preferences?ownerAddress="+other, nil)
	req = req.WithContext(auth.WithAddress(req.Context(), validAddr))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("owner-mismatch GET /preferences = %d, want 403", rec.Code)
	}
}

func TestHandler_HiddenAssetsGuardedEmptyArray(t *testing.T) {
	svc := NewService(NewRepository(nil))
	openAuth := func(next http.Handler) http.Handler { return next }
	mux := Router(svc, openAuth)

	// No auth → 401.
	req := httptest.NewRequest(http.MethodGet, "/hidden-assets?ownerAddress="+validAddr+"&chainId=11155111", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no-auth GET /hidden-assets = %d, want 401", rec.Code)
	}

	// Authed → 200 empty array (nil pool degrades to []).
	req = httptest.NewRequest(http.MethodGet, "/hidden-assets?chainId=11155111", nil)
	req = req.WithContext(auth.WithAddress(req.Context(), validAddr))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("authed GET /hidden-assets = %d, want 200", rec.Code)
	}
	var body []HiddenAsset
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body) != 0 {
		t.Errorf("len = %d, want 0", len(body))
	}
}
