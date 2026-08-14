package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResolveAllowedOrigins(t *testing.T) {
	t.Setenv("CORS_ALLOWED_ORIGINS", "")
	t.Setenv("NODE_ENV", "development")
	if got := resolveAllowedOrigins(); len(got) == 0 {
		t.Fatal("development should return the dev client origins")
	}

	t.Setenv("CORS_ALLOWED_ORIGINS", "https://a.example, https://b.example")
	got := resolveAllowedOrigins()
	if len(got) != 2 || got[0] != "https://a.example" || got[1] != "https://b.example" {
		t.Errorf("configured origins should win: %v", got)
	}

	t.Setenv("CORS_ALLOWED_ORIGINS", "")
	t.Setenv("NODE_ENV", "production")
	if got := resolveAllowedOrigins(); len(got) != 0 {
		t.Errorf("production should fail closed (no dev fallback): %v", got)
	}
}

func TestCorsMiddleware(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := corsMiddleware([]string{"http://localhost:3002"})(next)

	// allowed origin GET -> ACAO + credentials, request proceeds
	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.Header.Set("Origin", "http://localhost:3002")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Access-Control-Allow-Origin") != "http://localhost:3002" {
		t.Errorf("allowed origin missing ACAO")
	}
	if rec.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Errorf("missing credentials header")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("allowed GET should reach next, code %d", rec.Code)
	}

	// allowed origin OPTIONS preflight -> 204 + methods/headers
	req = httptest.NewRequest(http.MethodOptions, "/api/x", nil)
	req.Header.Set("Origin", "http://localhost:3002")
	req.Header.Set("Access-Control-Request-Method", "POST")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Errorf("preflight should 204, got %d", rec.Code)
	}
	if rec.Header().Get("Access-Control-Allow-Methods") == "" {
		t.Errorf("preflight missing Allow-Methods")
	}

	// disallowed origin -> no ACAO
	req = httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.Header.Set("Origin", "http://evil.example")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("disallowed origin should not get ACAO")
	}

	// no Origin (same-origin / non-browser) -> passes through
	req = httptest.NewRequest(http.MethodGet, "/api/x", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("no-origin request should pass through, code %d", rec.Code)
	}
}
