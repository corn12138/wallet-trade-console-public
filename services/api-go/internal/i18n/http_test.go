package i18n

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
)

// authAs simulates the SIWE middleware placing a verified wallet on the
// context (the real middleware is exercised in internal/auth's own tests).
func authAs(wallet string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if wallet == "" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"statusCode":401,"message":"Missing or invalid web3 authorization"}`))
				return
			}
			next.ServeHTTP(w, r.WithContext(auth.WithAddress(r.Context(), wallet)))
		})
	}
}

func TestParseAdminWallets(t *testing.T) {
	m := ParseAdminWallets(" 0xE2cd26322A87d2b6D8312DBcB79c38b7a226aD81 , 0xabcdef0123456789abcdef0123456789abcdef01,bogus,0xshort")
	if len(m) != 2 {
		t.Fatalf("want 2 normalized admins, got %d: %v", len(m), m)
	}
	if !m["0xe2cd26322a87d2b6d8312dbcb79c38b7a226ad81"] {
		t.Fatal("checksummed admin must normalize to lowercase")
	}
	if len(ParseAdminWallets("")) != 0 {
		t.Fatal("empty env must produce an empty (deny-all) allowlist")
	}
}

func TestAdminAuthorizationMatrix(t *testing.T) {
	admin := "0xE2cd26322A87d2b6D8312DBcB79c38b7a226aD81"
	t.Setenv(AdminWalletsEnv, admin) // checksummed on purpose — must normalize

	// Unauthenticated → 401.
	r := Router(NewService(newFakeStore()), authAs(""))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/admin/locales", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: want 401, got %d", rec.Code)
	}

	// Authenticated non-admin → 403.
	r = Router(NewService(newFakeStore()), authAs("0x1111111111111111111111111111111111111111"))
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/admin/locales", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin: want 403, got %d", rec.Code)
	}

	// Allowlisted admin (lowercased at both ends) → 200.
	r = Router(NewService(newFakeStore()), authAs(strings.ToLower(admin)))
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/admin/locales", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin: want 200, got %d (%s)", rec.Code, rec.Body.String())
	}

	// Empty allowlist denies even an authenticated wallet.
	t.Setenv(AdminWalletsEnv, "")
	r = Router(NewService(newFakeStore()), authAs(strings.ToLower(admin)))
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/admin/locales", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("empty allowlist: want 403, got %d", rec.Code)
	}
}

func TestActorComesFromContextNotBody(t *testing.T) {
	admin := "0xe2cd26322a87d2b6d8312dbcb79c38b7a226ad81"
	t.Setenv(AdminWalletsEnv, admin)
	fs := newFakeStore()
	r := Router(NewService(fs), authAs(admin))

	body := strings.NewReader(`{"value":"你好 {name}","expectedVersion":1,"actor":"0x9999999999999999999999999999999999999999"}`)
	req := httptest.NewRequest("PATCH", "/admin/messages/zh/app/hello", body)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("draft update: %d (%s)", rec.Code, rec.Body.String())
	}
	last := fs.audits[len(fs.audits)-1]
	if last.Actor != admin {
		t.Fatalf("audit actor must be the verified wallet, got %q", last.Actor)
	}
}

func TestPublicCatalogHTTP(t *testing.T) {
	fs := newFakeStore()
	svc := NewService(fs)
	if _, err := svc.Publish(context.Background(), "en", "0xe2cd26322a87d2b6d8312dbcb79c38b7a226ad81"); err != nil {
		t.Fatal(err)
	}
	r := Router(svc, authAs(""))

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/catalog/en", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("catalog: %d", rec.Code)
	}
	if src := rec.Header().Get("X-I18n-Source"); src != "database" {
		t.Fatalf("source header = %q", src)
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("missing ETag")
	}
	if body := rec.Body.String(); strings.Contains(body, "postgres") || strings.Contains(body, "DATABASE_URL") {
		t.Fatal("public catalog must not leak internals")
	}

	req := httptest.NewRequest("GET", "/catalog/en", nil)
	req.Header.Set("If-None-Match", etag)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Fatalf("conditional: want 304, got %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/catalog/fr", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("disabled locale: want 404, got %d", rec.Code)
	}

	// Public locale metadata carries no admin fields.
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/locales", nil))
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "updatedBy") {
		t.Fatalf("public locales leaked admin metadata: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"fr"`) {
		t.Fatal("disabled locales must not be public")
	}
}

func TestVersionConflictMapsTo409(t *testing.T) {
	admin := "0xe2cd26322a87d2b6d8312dbcb79c38b7a226ad81"
	t.Setenv(AdminWalletsEnv, admin)
	r := Router(NewService(newFakeStore()), authAs(admin))
	body := strings.NewReader(`{"value":"好的 {name}","expectedVersion":7}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("PATCH", "/admin/messages/zh/app/hello", body))
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale draft: want 409, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestValidationMapsTo422(t *testing.T) {
	admin := "0xe2cd26322a87d2b6d8312dbcb79c38b7a226ad81"
	t.Setenv(AdminWalletsEnv, admin)
	r := Router(NewService(newFakeStore()), authAs(admin))
	body := strings.NewReader(`{"value":"{broken","expectedVersion":1}`)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("PATCH", "/admin/messages/zh/app/hello", body))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid ICU: want 422, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "icu-syntax") {
		t.Fatalf("422 body must carry issues: %s", rec.Body.String())
	}
}
