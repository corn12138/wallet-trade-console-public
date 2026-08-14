package mobilecontrol

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

type stubValidator struct{ userID string }

func (s stubValidator) ValidateToken(context.Context, string) (string, bool) {
	if s.userID == "" {
		return "", false
	}
	return s.userID, true
}

func router(svc *Service) http.Handler {
	r := chi.NewRouter()
	r.Route("/api/mobile", func(m chi.Router) { Register(m, svc) })
	return r
}

func post(t *testing.T, h http.Handler, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func get(t *testing.T, h http.Handler, path string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRouteResolveAppScheme(t *testing.T) {
	h := router(NewService(nil))
	cases := []struct {
		appURL        string
		wantSurface   string
		wantPlane     string
		wantHandledBy string
		wantLogin     bool
	}{
		{"app://feed/home", "feed-home", "flutter", "flutter-container", false},
		{"app://video/feed", "video-feed", "native_media", "native-media-container", false},
		{"app://message/center", "message-center", "flutter", "flutter-container", true},
		{"https://evil.example.com/x", "external-link", "system_browser", "system-browser", false},
	}
	for _, c := range cases {
		rec := post(t, h, "/api/mobile/route/resolve", `{"appUrl":"`+c.appURL+`"}`, nil)
		if rec.Code != http.StatusCreated {
			t.Fatalf("%s: code %d want 201 (%s)", c.appURL, rec.Code, rec.Body.String())
		}
		var res RouteResolveResult
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatalf("%s decode: %v", c.appURL, err)
		}
		if res.SurfaceID != c.wantSurface || res.RuntimePlane != c.wantPlane || res.HandledBy != c.wantHandledBy || res.RequiresLogin != c.wantLogin {
			t.Errorf("%s: got %+v", c.appURL, res)
		}
	}
}

func TestRouteResolveTopicFallback(t *testing.T) {
	h := router(NewService(nil))
	rec := post(t, h, "/api/mobile/route/resolve", `{"appUrl":"app://topic/landing?slug=scholar"}`, nil)
	var res RouteResolveResult
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if res.SurfaceID != "topic-landing" || res.RuntimePlane != "web" || res.Fallback != "/topic/scholar" {
		t.Fatalf("topic resolve wrong: %+v", res)
	}
}

func TestRouteResolveRequiresAppUrl(t *testing.T) {
	h := router(NewService(nil))
	rec := post(t, h, "/api/mobile/route/resolve", `{}`, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing appUrl got %d want 400", rec.Code)
	}
}

func TestCapabilityCheckBrowserSafety(t *testing.T) {
	h := router(NewService(nil))
	// default container (browser): media.upload is NOT browser-safe -> false;
	// analytics.track IS -> true.
	rec := post(t, h, "/api/mobile/capability/check", `{}`, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("code %d", rec.Code)
	}
	var res CapabilityCheckResult
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if res.HostAPIVersion != "2.1.0" {
		t.Fatalf("hostApiVersion %q", res.HostAPIVersion)
	}
	if res.Supports["media.upload"] {
		t.Errorf("media.upload must be false on browser container")
	}
	if !res.Supports["analytics.track"] {
		t.Errorf("analytics.track must be true")
	}
}

func TestRuntimeConfigDefaults(t *testing.T) {
	h := router(NewService(nil))
	rec := get(t, h, "/api/mobile/config/runtime", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d", rec.Code)
	}
	var cfg RuntimeConfig
	_ = json.Unmarshal(rec.Body.Bytes(), &cfg)
	if cfg.BootstrapVersion != defaultBootstrapVersion {
		t.Errorf("bootstrapVersion %q", cfg.BootstrapVersion)
	}
	if cfg.RouteRuntimeOverrides["feed-home"] != "flutter" {
		t.Errorf("overrides missing feed-home")
	}
	if v, ok := cfg.Flags["bridge.gateway.v2"].(bool); !ok || !v {
		t.Errorf("flags missing bridge.gateway.v2")
	}
}

func TestSessionLoginDetection(t *testing.T) {
	h := router(NewService(stubValidator{userID: "u-1"}))
	// no token -> logged out
	rec := get(t, h, "/api/mobile/session/context", nil)
	var ctx SessionContext
	_ = json.Unmarshal(rec.Body.Bytes(), &ctx)
	if ctx.IsLoggedIn {
		t.Fatalf("no token must be logged out")
	}
	// with token -> logged in, userId set
	rec = get(t, h, "/api/mobile/session/context", map[string]string{"Authorization": "Bearer xyz"})
	_ = json.Unmarshal(rec.Body.Bytes(), &ctx)
	if !ctx.IsLoggedIn || ctx.UserID == nil || *ctx.UserID != "u-1" {
		t.Fatalf("token must log in as u-1: %+v", ctx)
	}
}

func TestRequireLoginGate(t *testing.T) {
	h := router(NewService(nil))
	rec := post(t, h, "/api/mobile/session/require-login", `{"reason":"checkout"}`, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("code %d", rec.Code)
	}
	var res SessionRequirementResult
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if res.Granted || res.Reason != "checkout" {
		t.Fatalf("expected denied with reason: %+v", res)
	}
}

func TestBootstrapMergesConfigAndSession(t *testing.T) {
	h := router(NewService(nil))
	rec := get(t, h, "/api/mobile/runtime/bootstrap", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d", rec.Code)
	}
	var bs RuntimeBootstrap
	if err := json.Unmarshal(rec.Body.Bytes(), &bs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if bs.BootstrapVersion == "" || bs.Container == "" || bs.Locale == "" {
		t.Fatalf("bootstrap missing merged fields: %+v", bs)
	}
}
