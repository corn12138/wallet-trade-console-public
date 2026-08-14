package mobilecontrol

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
)

// routeQuery carries the parsed query params used across the endpoints
// (RuntimeBootstrapQueryDto + WebBootstrapQueryDto fields).
type routeQuery struct {
	path       string
	appURL     string
	origin     string
	locale     string
	theme      string
	container  string
	appVersion string
	surfaceID  string
}

func queryFrom(r *http.Request) routeQuery {
	v := r.URL.Query()
	return routeQuery{
		path:       v.Get("path"),
		appURL:     v.Get("appUrl"),
		origin:     v.Get("origin"),
		locale:     v.Get("locale"),
		theme:      v.Get("theme"),
		container:  v.Get("container"),
		appVersion: v.Get("appVersion"),
		surfaceID:  v.Get("surfaceId"),
	}
}

// Register mounts the 8 control-plane routes on a router the caller mounts at
// /api/mobile (alongside the mobiledoc /docs + /v1 routes). All @Public.
func Register(r chi.Router, s *Service) {
	r.Get("/runtime/bootstrap", s.handleBootstrap)
	r.Get("/session/context", s.handleSessionContext)
	r.Post("/session/require-login", s.handleRequireLogin)
	r.Get("/config/runtime", s.handleRuntimeConfig)
	r.Post("/route/resolve", s.handleRouteResolve)
	r.Post("/capability/check", s.handleCapabilityCheck)
	r.Get("/web/bootstrap", s.handleWebBootstrap)
	r.Post("/analytics/ingest", s.handleAnalyticsIngest)
}

func (s *Service) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.getBootstrap(r, queryFrom(r)))
}

func (s *Service) handleSessionContext(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.getSessionContext(r, queryFrom(r)))
}

func (s *Service) handleRequireLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	// NestJS @Post default status is 201.
	writeJSON(w, http.StatusCreated, s.requireLogin(r, body.Reason))
}

func (s *Service) handleRuntimeConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.getRuntimeConfig(queryFrom(r)))
}

func (s *Service) handleRouteResolve(w http.ResponseWriter, r *http.Request) {
	var body RouteResolveRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.AppURL) == "" {
		writeError(w, http.StatusBadRequest, "appUrl is required")
		return
	}
	writeJSON(w, http.StatusCreated, s.resolveRoute(r, body))
}

func (s *Service) handleCapabilityCheck(w http.ResponseWriter, r *http.Request) {
	var body CapabilityCheckRequest
	_ = json.NewDecoder(r.Body).Decode(&body)
	writeJSON(w, http.StatusCreated, s.checkCapabilities(r, body))
}

func (s *Service) handleWebBootstrap(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.getWebBootstrap(r, queryFrom(r)))
}

func (s *Service) handleAnalyticsIngest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Events []AnalyticsEvent `json:"events"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	writeJSON(w, http.StatusCreated, ingestAnalytics(r, body.Events))
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Error("mobilecontrol response encode failed", "err", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"statusCode": status, "message": message})
}

// ---- header / value helpers ------------------------------------------------

func readHeader(r *http.Request, key string) string {
	return strings.TrimSpace(r.Header.Get(key))
}

func extractBearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
}

func extractRequestID(r *http.Request) string {
	return strings.TrimSpace(r.Header.Get("x-request-id"))
}

func resolveLocale(r *http.Request, override string) string {
	if v := firstNonEmpty(override, readHeader(r, "x-mobile-locale"), readHeader(r, "x-locale")); v != "" {
		return v
	}
	if al := strings.TrimSpace(r.Header.Get("accept-language")); al != "" {
		first := strings.TrimSpace(strings.SplitN(al, ",", 2)[0])
		if first != "" {
			return first
		}
	}
	return "zh-CN"
}

func resolveTheme(r *http.Request, override string) string {
	t := firstNonEmpty(override, readHeader(r, "x-mobile-theme"), readHeader(r, "x-theme"))
	if t != "" && allowedThemes[t] {
		return t
	}
	return "system"
}

func resolveContainer(r *http.Request, override string) string {
	c := firstNonEmpty(override, readHeader(r, "x-mobile-container"), readHeader(r, "x-container"))
	if c != "" && allowedContainers[c] {
		return c
	}
	return "browser"
}

func resolveAppVersion(r *http.Request, override string) string {
	return firstNonEmpty(override, readHeader(r, "x-mobile-app-version"), readHeader(r, "x-app-version"))
}

func normalizeAppRoute(parsed *url.URL) string {
	host := strings.TrimSpace(parsed.Host)
	path := strings.Trim(parsed.Path, "/")
	parts := []string{}
	if host != "" {
		parts = append(parts, host)
	}
	if path != "" {
		parts = append(parts, path)
	}
	return strings.Join(parts, "/")
}

func buildPathWithSearch(path, rawQuery string) string {
	if strings.TrimSpace(path) == "" {
		path = "/"
	}
	if rawQuery != "" {
		return path + "?" + rawQuery
	}
	return path
}

func resolveCurrentWebSurface(path string) string {
	p := strings.TrimSpace(path)
	if p == "" || p == "/" {
		return "web-runtime-entry"
	}
	switch {
	case strings.HasPrefix(p, "/campaign/"):
		return "campaign-page"
	case strings.HasPrefix(p, "/topic/"):
		return "topic-page"
	case strings.HasPrefix(p, "/help/"):
		return "help-page"
	case strings.HasPrefix(p, "/legal/"):
		return "legal-page"
	default:
		return ""
	}
}

func parseCSV(raw string) []string {
	out := []string{}
	if raw == "" {
		return out
	}
	for _, p := range strings.Split(raw, ",") {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func contains(slice []string, v string) bool {
	for _, x := range slice {
		if x == v {
			return true
		}
	}
	return false
}

func orDefault(v, def string) string {
	if v != "" {
		return v
	}
	return def
}

func truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case float64:
		return t != 0
	case int:
		return t != 0
	case string:
		return t != ""
	default:
		return v != nil
	}
}
