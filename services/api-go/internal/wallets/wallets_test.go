package wallets

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
)

const validAddr = "0x1234567890abcdef1234567890abcdef12345678"

func TestRepository_NilPoolReturnsErr(t *testing.T) {
	r := NewRepository(nil)
	if _, err := r.listWallets(context.Background(), "", nil); err != ErrPoolUnavailable {
		t.Errorf("listWallets err = %v, want ErrPoolUnavailable", err)
	}
	if _, err := r.listManagedWallets(context.Background(), validAddr, nil); err != ErrPoolUnavailable {
		t.Errorf("listManagedWallets err = %v, want ErrPoolUnavailable", err)
	}
	if _, err := r.listWalletGroups(context.Background(), validAddr); err != ErrPoolUnavailable {
		t.Errorf("listWalletGroups err = %v, want ErrPoolUnavailable", err)
	}
	if _, err := r.listHiddenAssets(context.Background(), validAddr, nil); err != ErrPoolUnavailable {
		t.Errorf("listHiddenAssets err = %v, want ErrPoolUnavailable", err)
	}
}

func TestService_GetWalletsDegradesAndIgnoresInvalidAddress(t *testing.T) {
	svc := NewService(NewRepository(nil))
	// Invalid address: short-circuits to [] before any DB call.
	got, err := svc.GetWallets(context.Background(), "not-an-address", nil)
	if err != nil || len(got) != 0 {
		t.Errorf("invalid addr: err=%v len=%d", err, len(got))
	}
	// Valid address + nil pool: degrades to [].
	got, err = svc.GetWallets(context.Background(), validAddr, nil)
	if err != nil || len(got) != 0 {
		t.Errorf("valid addr nil-pool: err=%v len=%d", err, len(got))
	}
	// No address (filter omitted) + nil pool: still degrades to [].
	got, err = svc.GetWallets(context.Background(), "", nil)
	if err != nil || len(got) != 0 {
		t.Errorf("no addr nil-pool: err=%v len=%d", err, len(got))
	}
}

func TestService_GetManagerEmptyOwnerReturnsNullEnvelope(t *testing.T) {
	svc := NewService(NewRepository(nil))
	got, err := svc.GetWalletManager(context.Background(), "", nil)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got.OwnerAddress != nil {
		t.Errorf("ownerAddress = %v, want nil", got.OwnerAddress)
	}
	if len(got.Wallets) != 0 || len(got.Groups) != 0 || len(got.HiddenAssets) != 0 {
		t.Errorf("got non-empty envelope: %+v", got)
	}
	if got.Summary.TotalWallets != 0 {
		t.Errorf("summary not zeroed: %+v", got.Summary)
	}
}

func TestService_GetManagerDegradesWithOwnerEchoed(t *testing.T) {
	svc := NewService(NewRepository(nil))
	got, err := svc.GetWalletManager(context.Background(), validAddr, nil)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got.OwnerAddress == nil || *got.OwnerAddress != validAddr {
		t.Errorf("ownerAddress = %v, want %q (degraded but echoed)", got.OwnerAddress, validAddr)
	}
	if len(got.Wallets) != 0 {
		t.Errorf("wallets not empty under degraded: %+v", got.Wallets)
	}
}

func TestDeriveWalletTypeAndAuth(t *testing.T) {
	ts := time.Now()
	cases := []struct {
		name               string
		lastLogin          *time.Time
		isImported         bool
		wantType, wantAuth string
	}{
		{"connected", &ts, false, "connected", "siwe-authenticated"},
		{"connected wins over isImported", &ts, true, "connected", "siwe-authenticated"},
		{"imported", nil, true, "imported", "imported-local"},
		{"watch-only", nil, false, "watch-only", "tracked"},
	}
	for _, tc := range cases {
		gotType, gotAuth := deriveWalletTypeAndAuth(tc.lastLogin, tc.isImported)
		if gotType != tc.wantType || gotAuth != tc.wantAuth {
			t.Errorf("%s: got %q/%q, want %q/%q", tc.name, gotType, gotAuth, tc.wantType, tc.wantAuth)
		}
	}
}

func TestNormalizeIfValid(t *testing.T) {
	if _, ok := normalizeIfValid("not-an-addr"); ok {
		t.Errorf("invalid input must return ok=false")
	}
	got, ok := normalizeIfValid("  0xABCDEF1234567890ABCDEF1234567890ABCDEF12  ")
	if !ok || got != "0xabcdef1234567890abcdef1234567890abcdef12" {
		t.Errorf("got (%q, %v), want lowered+trimmed", got, ok)
	}
}

// Wallets reads are web3-guarded + owner-pinned (parity with the NestJS
// class-level Web3AuthGuard + resolveAuthenticatedOwnerAddress). openAuth stands
// in for the configured middleware; the handler still owner-pins via
// auth.ResolveOwner, so the auth address must be in the request context.
// These replace the earlier public-handler tests (Router(svc, nil) expecting
// 200), which asserted the over-exposed behavior that this cut closed.
func TestHandler_WalletsListGuardedAndOwnerPinned(t *testing.T) {
	svc := NewService(NewRepository(nil))
	openAuth := func(next http.Handler) http.Handler { return next }
	mux := Router(svc, openAuth)

	// No authenticated address → 401 (must not list wallets publicly).
	req := httptest.NewRequest(http.MethodGet, "/?address="+validAddr+"&chainId=11155111", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no-auth GET / = %d, want 401", rec.Code)
	}

	// Authenticated, no ?address → scoped to the JWT wallet (200; [] under nil pool).
	req = httptest.NewRequest(http.MethodGet, "/?chainId=11155111", nil)
	req = req.WithContext(auth.WithAddress(req.Context(), validAddr))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("authed GET / = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var body []WalletRecord
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body) != 0 {
		t.Errorf("len = %d, want 0", len(body))
	}

	// Authenticated but ?address mismatches the JWT wallet → 403 (no IDOR).
	other := "0x000000000000000000000000000000000000bEEF"
	req = httptest.NewRequest(http.MethodGet, "/?address="+other, nil)
	req = req.WithContext(auth.WithAddress(req.Context(), validAddr))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("owner-mismatch GET / = %d, want 403", rec.Code)
	}
}

func TestHandler_WalletManagerGuardedAndOwnerPinned(t *testing.T) {
	svc := NewService(NewRepository(nil))
	openAuth := func(next http.Handler) http.Handler { return next }
	mux := Router(svc, openAuth)

	// No auth → 401.
	req := httptest.NewRequest(http.MethodGet, "/manager?ownerAddress="+validAddr, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no-auth GET /manager = %d, want 401", rec.Code)
	}

	// Authed, no ?ownerAddress → pinned to the JWT wallet (200; the degraded
	// nil-pool envelope echoes the resolved owner).
	req = httptest.NewRequest(http.MethodGet, "/manager", nil)
	req = req.WithContext(auth.WithAddress(req.Context(), validAddr))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("authed GET /manager = %d, want 200", rec.Code)
	}
	var body WalletManager
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.OwnerAddress == nil || *body.OwnerAddress != validAddr {
		t.Errorf("ownerAddress = %v, want %q (pinned to JWT wallet)", body.OwnerAddress, validAddr)
	}

	// Authed but ?ownerAddress mismatches the JWT wallet → 403 (no IDOR).
	other := "0x000000000000000000000000000000000000bEEF"
	req = httptest.NewRequest(http.MethodGet, "/manager?ownerAddress="+other, nil)
	req = req.WithContext(auth.WithAddress(req.Context(), validAddr))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("owner-mismatch GET /manager = %d, want 403", rec.Code)
	}
}
