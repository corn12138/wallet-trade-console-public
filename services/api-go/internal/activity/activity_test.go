package activity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
)

// testAddr is a valid 40-hex wallet for the owner-pinning handler tests.
const testAddr = "0x00000000000000000000000000000000000000ab"

func TestRepository_NilPoolReturnsErr(t *testing.T) {
	r := NewRepository(nil)
	if _, err := r.listEvents(context.Background(), Query{}); err != ErrPoolUnavailable {
		t.Errorf("listEvents err = %v, want ErrPoolUnavailable", err)
	}
	if _, err := r.listPerpOrders(context.Background(), Query{}); err != ErrPoolUnavailable {
		t.Errorf("listPerpOrders err = %v, want ErrPoolUnavailable", err)
	}
	if _, err := r.listPerpTrades(context.Background(), Query{}); err != ErrPoolUnavailable {
		t.Errorf("listPerpTrades err = %v, want ErrPoolUnavailable", err)
	}
}

func TestListTransactions_EmptyAddressShortCircuits(t *testing.T) {
	// nil pool + empty address must NOT error — NestJS short-circuits
	// to an empty array before the prisma call.
	r := NewRepository(nil)
	got, err := r.listTransactions(context.Background(), Query{Address: ""})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestListUserStakes_EmptyAddressShortCircuits(t *testing.T) {
	r := NewRepository(nil)
	got, err := r.listUserStakes(context.Background(), Query{Address: ""})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestService_GetActivityDegradesOnPoolUnavailable(t *testing.T) {
	svc := NewService(NewRepository(nil))
	// With an address set, the tx/stake short-circuits are bypassed
	// and the repo's nil-pool error propagates — service must degrade.
	got, err := svc.GetActivity(context.Background(), Query{Address: "0xabc"})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(got.Items) != 0 || got.Total != 0 {
		t.Errorf("got %+v, want empty", got)
	}
}

func TestHandler_DegradedReturnsEnvelope(t *testing.T) {
	svc := NewService(NewRepository(nil))
	mux := Router(svc)
	req := httptest.NewRequest(http.MethodGet, "/?address="+testAddr+"&limit=5", nil)
	req = req.WithContext(auth.WithAddress(req.Context(), testAddr))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body Response
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Items) != 0 || body.Total != 0 {
		t.Errorf("body = %+v, want empty envelope", body)
	}
}

func TestClampLimit(t *testing.T) {
	cases := map[int]int{
		0:     30,
		-1:    30,
		1:     1,
		30:    30,
		100:   100,
		101:   100,
		10000: 100,
	}
	for in, want := range cases {
		if got := clampLimit(in); got != want {
			t.Errorf("clampLimit(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestNormalizeAddress(t *testing.T) {
	if got := normalizeAddress(""); got != "" {
		t.Errorf("empty → %q, want empty", got)
	}
	if got := normalizeAddress("0xABCDEF"); got != "0xabcdef" {
		t.Errorf("uppercase → %q, want lowercase", got)
	}
}

func TestBuildTransactionTitle(t *testing.T) {
	approve := "approve"
	swap := "swap"
	open := "open-position"
	weird := "Unknown-Op"
	cases := []struct {
		in   *string
		want string
	}{
		{nil, "Onchain transaction submitted"},
		{&approve, "Token approval submitted"},
		{&swap, "Swap submitted"},
		{&open, "Open position submitted"},
		{&weird, "Onchain transaction submitted"},
	}
	for _, tc := range cases {
		if got := buildTransactionTitle(tc.in); got != tc.want {
			t.Errorf("title(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNormalizeEventType(t *testing.T) {
	if got := normalizeEventType("Token Created"); got != "token-created" {
		t.Errorf("got %q, want token-created", got)
	}
	if got := normalizeEventType(""); got != "" {
		t.Errorf("empty event → %q, want empty", got)
	}
}

func TestHasIndexedMetadata(t *testing.T) {
	cases := []struct {
		name string
		raw  []byte
		want bool
	}{
		{"nil", nil, false},
		{"empty", []byte{}, false},
		{"not json", []byte("{not json"), false},
		{"no indexing", []byte(`{"foo":"bar"}`), false},
		{"indexing not object", []byte(`{"indexing":"yes"}`), false},
		{"indexed", []byte(`{"indexing":{"indexedAt":"2026-05-21T00:00:00Z"}}`), true},
		{"indexing missing indexedAt", []byte(`{"indexing":{}}`), false},
	}
	for _, tc := range cases {
		if got := hasIndexedMetadata(tc.raw); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestDeriveTxDisplayStatus(t *testing.T) {
	if got := deriveTxDisplayStatus("CONFIRMED", nil); got != "confirmed" {
		t.Errorf("got %q, want confirmed", got)
	}
	if got := deriveTxDisplayStatus("", nil); got != "pending" {
		t.Errorf("empty status → %q, want pending", got)
	}
	if got := deriveTxDisplayStatus("confirmed", []byte(`{"indexing":{"indexedAt":"x"}}`)); got != "indexed" {
		t.Errorf("indexed → %q, want indexed", got)
	}
}

// Ensure the handler short-circuit path returns 200 (not 500) with an
// anonymous, no-address request — important because the FE renders
// the page even before SIWE sign-in.
// No ?address= defaults to the authenticated wallet (NestJS
// web3-request-owner parity) — never the unfiltered global feed.
func TestHandler_NoAddressUsesAuthenticatedWallet(t *testing.T) {
	svc := NewService(NewRepository(nil))
	mux := Router(svc)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = req.WithContext(auth.WithAddress(req.Context(), testAddr))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"items"`) {
		t.Errorf("missing items field: %q", rec.Body.String())
	}
}

func TestHandler_NoAuthenticatedWallet401(t *testing.T) {
	svc := NewService(NewRepository(nil))
	mux := Router(svc)
	req := httptest.NewRequest(http.MethodGet, "/?address="+testAddr, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%q", rec.Code, rec.Body.String())
	}
}

func TestHandler_AddressMismatch403(t *testing.T) {
	svc := NewService(NewRepository(nil))
	mux := Router(svc)
	req := httptest.NewRequest(http.MethodGet, "/?address=0x00000000000000000000000000000000000000ff", nil)
	req = req.WithContext(auth.WithAddress(req.Context(), testAddr))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%q", rec.Code, rec.Body.String())
	}
}
