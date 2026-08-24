package campaign

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/go-chi/chi/v5"
)

// A wallet on the allowlist, so the validation tests below exercise validation
// rather than stopping at the guard.
const testAdminWallet = "0x00000000000000000000000000000000000000ad"

func injectWallet(addr string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(auth.WithAddress(r.Context(), addr)))
		})
	}
}

// authorizedRouter satisfies both layers: a SIWE middleware that resolves a
// wallet, and an allowlist containing it.
func authorizedRouter(t *testing.T, svc *Service) chi.Router {
	t.Helper()
	t.Setenv(AdminWalletsEnv, testAdminWallet)
	return Router(svc, nil, injectWallet(testAdminWallet))
}

func TestRepository_NilPoolReturnsErr(t *testing.T) {
	r := NewRepository(nil)
	ctx := context.Background()
	if _, err := r.ListAll(ctx, ""); err != ErrPoolUnavailable {
		t.Errorf("ListAll err = %v, want ErrPoolUnavailable", err)
	}
	if _, err := r.GetActive(ctx, time.Now()); err != ErrPoolUnavailable {
		t.Errorf("GetActive err = %v, want ErrPoolUnavailable", err)
	}
	if _, err := r.FindByID(ctx, "x"); err != ErrPoolUnavailable {
		t.Errorf("FindByID err = %v, want ErrPoolUnavailable", err)
	}
	// The write must surface the error too (never silently "succeed").
	if _, err := r.Create(ctx, CreateInput{Title: "x"}); err != ErrPoolUnavailable {
		t.Errorf("Create err = %v, want ErrPoolUnavailable", err)
	}
}

func TestHandler_CreateRequiresTitle(t *testing.T) {
	svc := NewService(NewRepository(nil))
	body := `{"startDate":"2026-06-01T00:00:00Z","endDate":"2026-06-30T00:00:00Z"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	rr := httptest.NewRecorder()
	authorizedRouter(t, svc).ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rr.Code, rr.Body.String())
	}
}

func TestHandler_CreateRejectsBadDate(t *testing.T) {
	svc := NewService(NewRepository(nil))
	body := `{"title":"Launch","startDate":"not-a-date","endDate":"2026-06-30T00:00:00Z"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	rr := httptest.NewRecorder()
	authorizedRouter(t, svc).ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", rr.Code, rr.Body.String())
	}
}

func TestHandler_Create503WhenDegraded(t *testing.T) {
	// Valid body but nil pool: the write must 503, not pretend success.
	svc := NewService(NewRepository(nil))
	body := `{"title":"Launch","startDate":"2026-06-01T00:00:00Z","endDate":"2026-06-30T00:00:00Z"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	rr := httptest.NewRecorder()
	authorizedRouter(t, svc).ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body = %s", rr.Code, rr.Body.String())
	}
}

func TestParseDate(t *testing.T) {
	ok := []string{
		"2026-06-01T00:00:00Z",
		"2026-06-01T12:30:00.123456789Z",
		"2026-06-01",
		"2026-06-01T12:30:00+08:00",
	}
	for _, s := range ok {
		if _, err := parseDate(s); err != nil {
			t.Errorf("parseDate(%q) errored: %v", s, err)
		}
	}
	for _, s := range []string{"", "not-a-date", "06/01/2026"} {
		if _, err := parseDate(s); err == nil {
			t.Errorf("parseDate(%q) should have errored", s)
		}
	}
}

func TestService_ListDegradesOnPoolUnavailable(t *testing.T) {
	svc := NewService(NewRepository(nil))
	out, err := svc.List(context.Background(), "")
	if err != nil {
		t.Fatalf("expected nil err on degraded mode; got %v", err)
	}
	if len(out) != 0 {
		t.Errorf("expected empty slice; got %d", len(out))
	}
}

func TestService_FindByIDDegradesAsNotFound(t *testing.T) {
	// Pool-unavailable should surface as ErrNotFound so the handler
	// returns 404 not 500 when DB envs are missing.
	svc := NewService(NewRepository(nil))
	_, err := svc.FindByID(context.Background(), "abc")
	if err != ErrNotFound {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestHandler_ListReturnsEmptyArrayWhenDegraded(t *testing.T) {
	svc := NewService(NewRepository(nil))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	authorizedRouter(t, svc).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var out []Campaign
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("expected empty array; got %+v", out)
	}
}

func TestHandler_ActiveReturnsEmptyArrayWhenDegraded(t *testing.T) {
	svc := NewService(NewRepository(nil))
	req := httptest.NewRequest(http.MethodGet, "/active", nil)
	rr := httptest.NewRecorder()
	authorizedRouter(t, svc).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var out []Campaign
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("expected empty array; got %+v", out)
	}
}

func TestHandler_ByID404OnDegradedMode(t *testing.T) {
	svc := NewService(NewRepository(nil))
	req := httptest.NewRequest(http.MethodGet, "/somecuid", nil)
	rr := httptest.NewRecorder()
	authorizedRouter(t, svc).ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404 on degraded mode; got %d", rr.Code)
	}
}

func TestDecodeMetadata(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want any
	}{
		{"nil", nil, nil},
		{"empty", []byte{}, nil},
		{"bad json", []byte("{not json"), nil},
		{"valid", []byte(`{"k":1}`), map[string]any{"k": float64(1)}},
	}
	for _, tc := range cases {
		got := decodeMetadata(tc.in)
		switch want := tc.want.(type) {
		case nil:
			if got != nil {
				t.Errorf("%s: got %v, want nil", tc.name, got)
			}
		case map[string]any:
			gotMap, ok := got.(map[string]any)
			if !ok {
				t.Errorf("%s: got %T, want map", tc.name, got)
				continue
			}
			if gotMap["k"] != want["k"] {
				t.Errorf("%s: got %v, want %v", tc.name, gotMap, want)
			}
		}
	}
}

// ── write guard ─────────────────────────────────────────────────────────────
// These are the tests that would have caught the hole. Before the guard, an
// anonymous POST reached the database and answered 503 — an outage-shaped
// reply to a missing-credential problem — while against a live database it
// answered 201 and published the row to every visitor of /api/campaign/active.

func TestWrites_RejectAnonymous(t *testing.T) {
	t.Setenv(AdminWalletsEnv, testAdminWallet)
	svc := NewService(NewRepository(nil))
	valid := `{"title":"Launch","startDate":"2026-06-01T00:00:00Z","endDate":"2026-06-30T00:00:00Z"}`

	for _, tc := range []struct {
		name, method, target, body string
	}{
		{"create", http.MethodPost, "/", valid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.target, strings.NewReader(tc.body))
			rr := httptest.NewRecorder()
			// No SIWE middleware at all: the deny-all stand-in must answer.
			Router(svc, nil, nil).ServeHTTP(rr, req)
			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401; body = %s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestWrites_RejectWalletOutsideAllowlist(t *testing.T) {
	t.Setenv(AdminWalletsEnv, testAdminWallet)
	svc := NewService(NewRepository(nil))
	body := `{"title":"Launch","startDate":"2026-06-01T00:00:00Z","endDate":"2026-06-30T00:00:00Z"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	rr := httptest.NewRecorder()
	Router(svc, nil, injectWallet("0x000000000000000000000000000000000000dead")).ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body = %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "campaign administrator") {
		t.Errorf("403 body should name the reason, got %s", rr.Body.String())
	}
}

// The contract that matters most: an unconfigured deployment denies everyone.
// Absence of configuration must never grant access.
func TestWrites_EmptyAllowlistDeniesEveryone(t *testing.T) {
	t.Setenv(AdminWalletsEnv, "")
	svc := NewService(NewRepository(nil))
	body := `{"title":"Launch","startDate":"2026-06-01T00:00:00Z","endDate":"2026-06-30T00:00:00Z"}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	rr := httptest.NewRecorder()
	Router(svc, nil, injectWallet(testAdminWallet)).ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 with an empty allowlist; body = %s", rr.Code, rr.Body.String())
	}
}

func TestParseAdminWallets(t *testing.T) {
	got := ParseAdminWallets(" 0x00000000000000000000000000000000000000AD ,,not-an-address,0xBEEF")
	if len(got) != 1 || !got[testAdminWallet] {
		t.Fatalf("ParseAdminWallets = %v, want only the normalized valid address", got)
	}
	if len(ParseAdminWallets("")) != 0 {
		t.Error("empty config must yield an empty allowlist, not a permissive one")
	}
}

// Reads must stay public — the guard must not over-block.
func TestReads_StayPublic(t *testing.T) {
	t.Setenv(AdminWalletsEnv, testAdminWallet)
	svc := NewService(NewRepository(nil))
	for _, target := range []string{"/", "/active"} {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		rr := httptest.NewRecorder()
		Router(svc, nil, nil).ServeHTTP(rr, req)
		if rr.Code == http.StatusUnauthorized || rr.Code == http.StatusForbidden {
			t.Errorf("GET %s = %d; public reads must not require auth", target, rr.Code)
		}
	}
}
