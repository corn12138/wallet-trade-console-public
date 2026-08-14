package security

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
)

func TestNormalizeSiteOrigin(t *testing.T) {
	cases := []struct {
		name       string
		in         string
		wantOrigin string
		wantDomain string
		wantErr    error
	}{
		{
			name:       "https URL is canonicalized",
			in:         "https://Foo.Example.com/path",
			wantOrigin: "https://foo.example.com",
			wantDomain: "foo.example.com",
		},
		{
			name:       "http URL keeps scheme",
			in:         "http://localhost:3000",
			wantOrigin: "http://localhost:3000",
			wantDomain: "localhost",
		},
		{
			name:       "missing scheme defaults to https",
			in:         "example.com",
			wantOrigin: "https://example.com",
			wantDomain: "example.com",
		},
		{
			name:    "blank → ErrSiteOriginRequired",
			in:      "   ",
			wantErr: ErrSiteOriginRequired,
		},
		{
			// Inputs starting with "http" (but not "http://" / "https://")
			// reach url.Parse with an explicit non-http(s) scheme — the
			// protocol-check guard exists for this path. Inputs missing
			// a scheme get prepended with https:// and pass cleanly, so
			// "ftp://..." would actually parse as host="ftp:" under
			// "https://ftp://..." and slip through (same as NestJS).
			name:    "non-http http*-prefix scheme rejected",
			in:      "httpx://files.example.com",
			wantErr: ErrUnsupportedScheme,
		},
	}
	for _, tc := range cases {
		origin, domain, err := normalizeSiteOrigin(tc.in)
		if tc.wantErr != nil {
			if err != tc.wantErr {
				t.Errorf("%s: err = %v, want %v", tc.name, err, tc.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: err = %v, want nil", tc.name, err)
			continue
		}
		if origin != tc.wantOrigin {
			t.Errorf("%s: origin = %q, want %q", tc.name, origin, tc.wantOrigin)
		}
		if domain != tc.wantDomain {
			t.Errorf("%s: domain = %q, want %q", tc.name, domain, tc.wantDomain)
		}
	}
}

func TestNormalizeRiskLevel(t *testing.T) {
	if got := normalizeRiskLevel(nil); got != "unknown" {
		t.Errorf("got %q, want unknown", got)
	}
	v := ""
	if got := normalizeRiskLevel(&v); got != "unknown" {
		t.Errorf("blank got %q, want unknown", got)
	}
	v2 := "  HIGH  "
	if got := normalizeRiskLevel(&v2); got != "high" {
		t.Errorf("got %q, want high", got)
	}
}

func TestDedupePermissions(t *testing.T) {
	got := dedupePermissions([]string{"read", "  read  ", "", "write", "read"})
	want := []string{"read", "write"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d (got %v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestDecodePermissions(t *testing.T) {
	// Mixed-type input: only string entries survive — matches the
	// NestJS .filter((entry): entry is string => typeof entry === 'string').
	got := decodePermissions([]byte(`["read", 42, "write", null]`))
	if len(got) != 2 || got[0] != "read" || got[1] != "write" {
		t.Errorf("got %v, want [read write]", got)
	}
	// Empty / invalid → empty slice, never nil (FE relies on array).
	if got := decodePermissions(nil); got == nil || len(got) != 0 {
		t.Errorf("nil input got %v, want empty []", got)
	}
	if got := decodePermissions([]byte("not json")); got == nil || len(got) != 0 {
		t.Errorf("invalid JSON got %v, want empty []", got)
	}
}

func TestUpsertConnectedSite_NilPoolReturnsErr(t *testing.T) {
	// Validation passes first (origin + siteName valid) → reaches the
	// pool gate → returns ErrMutationRequiresPool.
	r := NewRepository(nil)
	_, err := r.UpsertConnectedSite(context.Background(), UpsertConnectedSiteRequest{
		OwnerAddress: "0x000000000000000000000000000000000000bEEF",
		Origin:       "example.com",
		SiteName:     "Example",
	})
	if err != ErrMutationRequiresPool {
		t.Errorf("err = %v, want ErrMutationRequiresPool", err)
	}
}

func TestUpsertConnectedSite_BlankSiteName(t *testing.T) {
	r := NewRepository(nil)
	_, err := r.UpsertConnectedSite(context.Background(), UpsertConnectedSiteRequest{
		OwnerAddress: "0x000000000000000000000000000000000000bEEF",
		Origin:       "example.com",
		SiteName:     "  ",
	})
	if err != ErrSiteNameRequired {
		t.Errorf("err = %v, want ErrSiteNameRequired", err)
	}
}

func TestUpsertConnectedSite_BlankOrigin(t *testing.T) {
	r := NewRepository(nil)
	_, err := r.UpsertConnectedSite(context.Background(), UpsertConnectedSiteRequest{
		OwnerAddress: "0x000000000000000000000000000000000000bEEF",
		Origin:       "",
		SiteName:     "Example",
	})
	if err != ErrSiteOriginRequired {
		t.Errorf("err = %v, want ErrSiteOriginRequired", err)
	}
}

func TestUpsertConnectedSite_BadScheme(t *testing.T) {
	r := NewRepository(nil)
	_, err := r.UpsertConnectedSite(context.Background(), UpsertConnectedSiteRequest{
		OwnerAddress: "0x000000000000000000000000000000000000bEEF",
		Origin:       "httpx://files.example.com",
		SiteName:     "Example",
	})
	if err != ErrUnsupportedScheme {
		t.Errorf("err = %v, want ErrUnsupportedScheme", err)
	}
}

func TestDeleteConnectedSite_NilPoolReturnsErr(t *testing.T) {
	r := NewRepository(nil)
	err := r.DeleteConnectedSite(context.Background(), "0x000000000000000000000000000000000000bEEF", "abc")
	if err != ErrMutationRequiresPool {
		t.Errorf("err = %v, want ErrMutationRequiresPool", err)
	}
}

func TestDeleteConnectedSite_BadOwner(t *testing.T) {
	r := NewRepository(nil)
	err := r.DeleteConnectedSite(context.Background(), "bad", "abc")
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

func TestHandler_PostConnectedSiteUnauthorizedWithoutContext(t *testing.T) {
	mux := mountMutationRouter(t)
	req := httptest.NewRequest(http.MethodPost, "/connected-sites", strings.NewReader(`{"origin":"example.com","siteName":"X"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestHandler_PostConnectedSiteOwnerMismatchIs403(t *testing.T) {
	mux := mountMutationRouter(t)
	body := `{"ownerAddress":"0x0000000000000000000000000000000000000001","origin":"example.com","siteName":"X"}`
	req := httptest.NewRequest(http.MethodPost, "/connected-sites", strings.NewReader(body))
	req = req.WithContext(auth.WithAddress(req.Context(), "0x000000000000000000000000000000000000bEEF"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestHandler_PostConnectedSiteBlankNameIs400(t *testing.T) {
	mux := mountMutationRouter(t)
	body := `{"origin":"example.com","siteName":""}`
	req := httptest.NewRequest(http.MethodPost, "/connected-sites", strings.NewReader(body))
	req = req.WithContext(auth.WithAddress(req.Context(), "0x000000000000000000000000000000000000bEEF"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestHandler_PostConnectedSiteNilPoolIs503(t *testing.T) {
	mux := mountMutationRouter(t)
	body := `{"origin":"example.com","siteName":"Example"}`
	req := httptest.NewRequest(http.MethodPost, "/connected-sites", strings.NewReader(body))
	req = req.WithContext(auth.WithAddress(req.Context(), "0x000000000000000000000000000000000000bEEF"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

func TestHandler_DeleteConnectedSiteNilPoolIs503(t *testing.T) {
	mux := mountMutationRouter(t)
	req := httptest.NewRequest(http.MethodDelete, "/connected-sites/abc", nil)
	req = req.WithContext(auth.WithAddress(req.Context(), "0x000000000000000000000000000000000000bEEF"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

func TestHandler_MutationsDontMountWhenAuthMiddlewareIsNil(t *testing.T) {
	svc := NewService(NewRepository(nil))
	mux := Router(svc, nil)
	req := httptest.NewRequest(http.MethodPost, "/connected-sites", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404/405 (route should not mount without auth)", rec.Code)
	}
}
