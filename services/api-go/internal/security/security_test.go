package security

import (
	"bytes"
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
)

const validAddr = "0x1234567890abcdef1234567890abcdef12345678"

// openAuth is a pass-through middleware standing in for the configured web3
// guard; the read handlers still owner-pin via auth.ResolveOwner, so the
// authenticated address must be injected on the request context.
func openAuth(next http.Handler) http.Handler { return next }

func TestRepository_NilPoolReturnsErr(t *testing.T) {
	r := NewRepository(nil)
	if _, err := r.listApprovalEvents(context.Background(), validAddr, nil); err != ErrPoolUnavailable {
		t.Errorf("listApprovalEvents err = %v, want ErrPoolUnavailable", err)
	}
	if _, err := r.countRecentApprovalEvents(context.Background(), validAddr, nil, time.Now()); err != ErrPoolUnavailable {
		t.Errorf("countRecentApprovalEvents err = %v, want ErrPoolUnavailable", err)
	}
	if _, err := r.listConnectedSites(context.Background(), validAddr, nil); err != ErrPoolUnavailable {
		t.Errorf("listConnectedSites err = %v, want ErrPoolUnavailable", err)
	}
}

func TestService_EmptyAddressReturnsEmptyEverywhere(t *testing.T) {
	svc := NewService(NewRepository(nil))
	if got, _ := svc.GetApprovals(context.Background(), "", nil); len(got) != 0 {
		t.Errorf("approvals empty addr: got %d, want 0", len(got))
	}
	if got, _ := svc.GetAlerts(context.Background(), "", nil); len(got) != 0 {
		t.Errorf("alerts empty addr: got %d, want 0", len(got))
	}
	if got, _ := svc.GetConnectedSites(context.Background(), "", nil); len(got) != 0 {
		t.Errorf("sites empty addr: got %d, want 0", len(got))
	}
}

func TestService_NilPoolDegradesAcrossAll(t *testing.T) {
	svc := NewService(NewRepository(nil))
	approvals, err := svc.GetApprovals(context.Background(), validAddr, nil)
	if err != nil || len(approvals) != 0 {
		t.Errorf("approvals nil-pool: err=%v len=%d", err, len(approvals))
	}
	alerts, err := svc.GetAlerts(context.Background(), validAddr, nil)
	if err != nil || len(alerts) != 0 {
		t.Errorf("alerts nil-pool: err=%v len=%d", err, len(alerts))
	}
	sites, err := svc.GetConnectedSites(context.Background(), validAddr, nil)
	if err != nil || len(sites) != 0 {
		t.Errorf("sites nil-pool: err=%v len=%d", err, len(sites))
	}
}

func TestParseBigIntLike(t *testing.T) {
	cases := map[string]string{
		"":                    "0",
		"abc":                 "0",
		"-1":                  "0", // sign disallowed by /^\d+$/
		"0":                   "0",
		"100":                 "100",
		"1000000000000000000": "1000000000000000000",
	}
	for in, want := range cases {
		got := parseBigIntLike(in).String()
		if got != want {
			t.Errorf("parseBigIntLike(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsUnlimited(t *testing.T) {
	// MAX_UINT256 / 100
	threshold := unlimitedThreshold
	if !isUnlimited(threshold) {
		t.Errorf("threshold itself must count as unlimited (>=)")
	}
	if !isUnlimited(new(big.Int).Add(threshold, big.NewInt(1))) {
		t.Errorf("above threshold must be unlimited")
	}
	if isUnlimited(new(big.Int).Sub(threshold, big.NewInt(1))) {
		t.Errorf("just below threshold must NOT be unlimited")
	}
	if isUnlimited(big.NewInt(0)) {
		t.Errorf("zero must NOT be unlimited")
	}
}

func TestAgeInDays(t *testing.T) {
	now := time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC)
	if got := ageInDays(now.Add(-30*24*time.Hour).Format(time.RFC3339Nano), now); got != 30 {
		t.Errorf("30-day ago = %d, want 30", got)
	}
	if got := ageInDays(now.Format(time.RFC3339Nano), now); got != 0 {
		t.Errorf("now = %d, want 0", got)
	}
	if got := ageInDays("not-a-date", now); got != 0 {
		t.Errorf("bad input = %d, want 0", got)
	}
	if got := ageInDays(now.Add(24*time.Hour).Format(time.RFC3339Nano), now); got != 0 {
		t.Errorf("future = %d, want 0 (clamped)", got)
	}
}

func TestDedupeAndSortAlerts(t *testing.T) {
	a := Alert{ID: "x", OccurredAt: "2026-01-01T00:00:00Z"}
	b := Alert{ID: "y", OccurredAt: "2026-05-21T00:00:00Z"}
	c := Alert{ID: "x", OccurredAt: "2099-01-01T00:00:00Z"} // dup of a
	out := dedupeAndSortAlerts([]Alert{a, b, c})
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2", len(out))
	}
	if out[0].ID != "y" || out[1].ID != "x" {
		t.Errorf("got %v, want [y, x] (sorted DESC by occurredAt; first-seen x wins dedupe)", []string{out[0].ID, out[1].ID})
	}
}

func TestAsAddress(t *testing.T) {
	if got := asAddress(validAddr); got != validAddr {
		t.Errorf("got %q, want %q", got, validAddr)
	}
	if got := asAddress("0xABCDEF1234567890ABCDEF1234567890ABCDEF12"); got != strings.ToLower("0xABCDEF1234567890ABCDEF1234567890ABCDEF12") {
		t.Errorf("uppercase not lowered: %q", got)
	}
	if got := asAddress("not-an-address"); got != "" {
		t.Errorf("invalid address must return empty, got %q", got)
	}
	if got := asAddress(123); got != "" {
		t.Errorf("non-string must return empty, got %q", got)
	}
}

func TestStringifyBigIntLike(t *testing.T) {
	// Table-driven with a slice (not a map) because some inputs
	// (e.g. map[string]any) are not hashable and would panic if used
	// as map keys.
	cases := []struct {
		in   any
		want string
	}{
		{"100", "100"},
		{float64(42), "42"},
		{int64(7), "7"},
		{nil, "0"},
		{map[string]any{}, "0"},
	}
	for _, tc := range cases {
		if got := stringifyBigIntLike(tc.in); got != tc.want {
			t.Errorf("stringifyBigIntLike(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDecodeStringArray(t *testing.T) {
	if got := decodeStringArray(nil); len(got) != 0 {
		t.Errorf("nil → %v, want []", got)
	}
	if got := decodeStringArray([]byte(`["a","b","c"]`)); len(got) != 3 || got[0] != "a" {
		t.Errorf("got %v, want [a b c]", got)
	}
	if got := decodeStringArray([]byte(`[1, "ok", null]`)); len(got) != 1 || got[0] != "ok" {
		t.Errorf("mixed types filtered: got %v, want [ok]", got)
	}
	if got := decodeStringArray([]byte("not json")); len(got) != 0 {
		t.Errorf("bad json → %v, want []", got)
	}
}

// Security reads are web3-guarded + owner-pinned (parity with the NestJS
// class-level Web3AuthGuard + resolveAuthenticatedOwnerAddress). The handler
// owner-pins via auth.ResolveOwner, so the auth address must be on the request
// context. These replace the earlier public-read tests (Router(svc,nil)
// expecting 200), which asserted the over-exposed behavior this closes.
func TestHandler_ApprovalsGuardedAndOwnerPinned(t *testing.T) {
	svc := NewService(NewRepository(nil))
	mux := Router(svc, openAuth)

	// No authenticated address → 401 (must not serve owner-private data).
	req := httptest.NewRequest(http.MethodGet, "/approvals?address="+validAddr+"&chainId=11155111", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no-auth GET /approvals = %d, want 401", rec.Code)
	}

	// Authenticated, no ?address → pinned to the JWT wallet (200; [] under nil pool).
	req = httptest.NewRequest(http.MethodGet, "/approvals?chainId=11155111", nil)
	req = req.WithContext(auth.WithAddress(req.Context(), validAddr))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("authed GET /approvals = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	var body []ApprovalRecord
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body) != 0 {
		t.Errorf("len = %d, want 0 (degraded)", len(body))
	}

	// Authenticated but ?address mismatches the JWT wallet → 403 (no IDOR).
	req = httptest.NewRequest(http.MethodGet, "/approvals?address=0x000000000000000000000000000000000000bEEF", nil)
	req = req.WithContext(auth.WithAddress(req.Context(), validAddr))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("owner-mismatch GET /approvals = %d, want 403", rec.Code)
	}
}

func TestHandler_AlertsAndSitesGuardedAndOwnerPinned(t *testing.T) {
	svc := NewService(NewRepository(nil))
	mux := Router(svc, openAuth)
	for _, path := range []string{"/alerts", "/connected-sites"} {
		// No auth → 401.
		req := httptest.NewRequest(http.MethodGet, path+"?address="+validAddr, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("no-auth GET %s = %d, want 401", path, rec.Code)
		}

		// Authed, pinned → 200.
		req = httptest.NewRequest(http.MethodGet, path, nil)
		req = req.WithContext(auth.WithAddress(req.Context(), validAddr))
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("authed GET %s = %d, want 200; body=%q", path, rec.Code, rec.Body.String())
		}

		// Mismatch → 403.
		req = httptest.NewRequest(http.MethodGet, path+"?address=0x000000000000000000000000000000000000bEEF", nil)
		req = req.WithContext(auth.WithAddress(req.Context(), validAddr))
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("owner-mismatch GET %s = %d, want 403", path, rec.Code)
		}
	}
}

func TestBuildRevokeTx_HappyPath(t *testing.T) {
	svc := NewService(NewRepository(nil))
	got, err := svc.BuildRevokeTx(RevokeTxRequest{
		TokenAddress: "0x000000000000000000000000000000000000DEAD",
		Spender:      "0x000000000000000000000000000000000000BEEF",
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got.ChainID != 11155111 {
		t.Errorf("chainId = %d, want 11155111 (Sepolia default)", got.ChainID)
	}
	if got.To != "0x000000000000000000000000000000000000DEAD" {
		t.Errorf("to = %q", got.To)
	}
	// approve(address,uint256) = 0x095ea7b3 + spender (left-padded) + 0
	want := "0x095ea7b3" +
		"000000000000000000000000000000000000000000000000000000000000beef" +
		"0000000000000000000000000000000000000000000000000000000000000000"
	if got.Data != want {
		t.Errorf("data:\n got  %s\nwant %s", got.Data, want)
	}
}

func TestBuildRevokeTx_CustomChainID(t *testing.T) {
	svc := NewService(NewRepository(nil))
	chain := 1
	got, _ := svc.BuildRevokeTx(RevokeTxRequest{
		TokenAddress: "0x000000000000000000000000000000000000DEAD",
		Spender:      "0x000000000000000000000000000000000000BEEF",
		ChainID:      &chain,
	})
	if got.ChainID != 1 {
		t.Errorf("chainId = %d, want 1", got.ChainID)
	}
}

func TestBuildRevokeTx_InvalidAddress(t *testing.T) {
	svc := NewService(NewRepository(nil))
	_, err := svc.BuildRevokeTx(RevokeTxRequest{
		TokenAddress: "not-an-address",
		Spender:      "0x000000000000000000000000000000000000BEEF",
	})
	if err == nil {
		t.Errorf("expected error on bad token address")
	}
	_, err = svc.BuildRevokeTx(RevokeTxRequest{
		TokenAddress: "0x000000000000000000000000000000000000DEAD",
		Spender:      "0x123",
	})
	if err == nil {
		t.Errorf("expected error on bad spender")
	}
}

func TestHandler_RevokeBuildTx(t *testing.T) {
	svc := NewService(NewRepository(nil))
	mux := Router(svc, nil)
	body, _ := json.Marshal(RevokeTxRequest{
		TokenAddress: "0x000000000000000000000000000000000000DEAD",
		Spender:      "0x000000000000000000000000000000000000BEEF",
	})
	req := httptest.NewRequest(http.MethodPost, "/revoke/build-tx", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	var resp RevokeTxResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ChainID != 11155111 {
		t.Errorf("chainId = %d", resp.ChainID)
	}
}

func TestHandler_RevokeBuildTxBadInputReturns400(t *testing.T) {
	svc := NewService(NewRepository(nil))
	mux := Router(svc, nil)
	body, _ := json.Marshal(RevokeTxRequest{TokenAddress: "bad", Spender: "0x000000000000000000000000000000000000BEEF"})
	req := httptest.NewRequest(http.MethodPost, "/revoke/build-tx", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestHandler_ConnectedSitesAcceptsOwnerAddressAlias(t *testing.T) {
	svc := NewService(NewRepository(nil))
	mux := Router(svc, openAuth)
	// FE may pass `ownerAddress` instead of `address` (see lib/api/atlas.ts
	// connected-sites helpers). With the wallet authenticated, the alias still
	// resolves to the owner and owner-pins (matching wallet → 200).
	req := httptest.NewRequest(http.MethodGet, "/connected-sites?ownerAddress="+validAddr, nil)
	req = req.WithContext(auth.WithAddress(req.Context(), validAddr))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}
