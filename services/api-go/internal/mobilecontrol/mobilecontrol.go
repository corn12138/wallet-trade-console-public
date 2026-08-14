// Package mobilecontrol is the Go port of
// legacy NestJS mobile-control/mobile-control.service.ts + .controller.ts —
// the mobile runtime control plane (8 @Public endpoints under /api/mobile):
//
//	GET  /runtime/bootstrap     runtime config + session context (merged)
//	GET  /session/context       session context
//	POST /session/require-login login gate check
//	GET  /config/runtime        runtime config / flags / kill switches
//	POST /route/resolve         resolve an app/web URL to a runtime surface
//	POST /capability/check      host-capability whitelist for a container
//	GET  /web/bootstrap         first-party H5 controlled bootstrap
//	POST /analytics/ingest      accept runtime analytics events
//
// The logic is deterministic + env-config driven (ConfigService → os.Getenv)
// plus optional bearer-token validation for the session state. It mirrors the
// @wallet-trade/shared MobileRuntime* / MobileSession* / MobileRoute* contracts.
package mobilecontrol

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultBootstrapVersion = "2026.03.full-phase0"
	defaultHostAPIVersion   = "2.1.0"
)

var defaultFirstPartyOrigins = []string{
	"http://localhost:3000", "http://127.0.0.1:3000",
	"http://localhost:3002", "http://127.0.0.1:3002",
}

var defaultRouteRuntimeOverrides = map[string]string{
	"feed-home": "flutter", "article-detail": "flutter", "comment-sheet": "flutter",
	"search-index": "flutter", "message-center": "flutter", "profile-home": "flutter",
	"settings-index": "flutter", "video-feed": "native_media", "video-detail": "native_media",
	"campaign-detail": "web", "topic-landing": "web", "help-detail": "web", "legal-detail": "web",
}

var defaultFlags = map[string]any{
	"route.center.enabled": true, "bridge.gateway.v2": true, "runtime.web.prewarm": true,
	"runtime.web.pooling": false, "runtime.flutter.add_to_app": false,
	"runtime.native_media.enabled": true, "kill_switch.video.autoplay": false,
}

var allowedNamespaces = []string{"route", "session", "device", "ui", "share", "analytics", "media", "comment", "video"}
var allowedContainers = map[string]bool{"browser": true, "native-webview": true, "flutter-webview": true}
var allowedThemes = map[string]bool{"light": true, "dark": true, "system": true}

var supportedCapabilities = []string{
	"route.open", "route.close", "session.getContext", "session.requireLogin",
	"device.getInfo", "device.getNetworkStatus", "ui.showToast", "ui.setNavigationBar",
	"share.open", "analytics.track", "media.pickImage", "media.upload",
	"comment.openSheet", "video.enterFullscreen",
}

var browserSafeCapabilities = map[string]bool{
	"route.open": true, "route.close": true, "session.getContext": true,
	"session.requireLogin": true, "device.getInfo": true, "device.getNetworkStatus": true,
	"analytics.track": true,
}

// TokenValidator validates a bearer token and returns the user id. A nil
// validator means the control plane always reports logged-out (safe default).
type TokenValidator interface {
	ValidateToken(ctx context.Context, token string) (userID string, ok bool)
}

// Service is the control-plane logic. Env-config is read live (like
// ConfigService) so flags/kill-switches can change without a redeploy.
type Service struct {
	validator TokenValidator
}

// NewService builds the service; validator may be nil.
func NewService(validator TokenValidator) *Service { return &Service{validator: validator} }

// SessionContextFor exposes the session snapshot for sibling packages (the
// mobile-bff aggregation reads login/user state through this).
func (s *Service) SessionContextFor(r *http.Request) SessionContext {
	return s.getSessionContext(r, routeQuery{})
}

// ---- response contracts (mirror @wallet-trade/shared) ----------------------

type RuntimeConfig struct {
	BootstrapVersion        string            `json:"bootstrapVersion"`
	DisabledNamespaces      []string          `json:"disabledNamespaces"`
	RouteRuntimeOverrides   map[string]string `json:"routeRuntimeOverrides"`
	KilledRoutes            []string          `json:"killedRoutes"`
	Flags                   map[string]any    `json:"flags"`
	FirstPartyWebOrigins    []string          `json:"firstPartyWebOrigins"`
	ObservabilitySampleRate float64           `json:"observabilitySampleRate"`
}

type SessionContext struct {
	IsLoggedIn         bool     `json:"isLoggedIn"`
	UserID             *string  `json:"userId,omitempty"`
	Locale             string   `json:"locale"`
	Theme              string   `json:"theme"`
	AppVersion         *string  `json:"appVersion,omitempty"`
	Container          string   `json:"container"`
	DisabledNamespaces []string `json:"disabledNamespaces"`
}

// RuntimeBootstrap = RuntimeConfig spread + SessionContext spread (the session
// disabledNamespaces wins, but they're equal). Mirrors `{...cfg, ...session}`.
type RuntimeBootstrap struct {
	BootstrapVersion        string            `json:"bootstrapVersion"`
	RouteRuntimeOverrides   map[string]string `json:"routeRuntimeOverrides"`
	KilledRoutes            []string          `json:"killedRoutes"`
	Flags                   map[string]any    `json:"flags"`
	FirstPartyWebOrigins    []string          `json:"firstPartyWebOrigins"`
	ObservabilitySampleRate float64           `json:"observabilitySampleRate"`
	IsLoggedIn              bool              `json:"isLoggedIn"`
	UserID                  *string           `json:"userId,omitempty"`
	Locale                  string            `json:"locale"`
	Theme                   string            `json:"theme"`
	AppVersion              *string           `json:"appVersion,omitempty"`
	Container               string            `json:"container"`
	DisabledNamespaces      []string          `json:"disabledNamespaces"`
}

type SessionRequirementResult struct {
	Granted bool   `json:"granted"`
	Reason  string `json:"reason,omitempty"`
}

type RouteResolveRequest struct {
	AppURL     string `json:"appUrl"`
	Source     string `json:"source"`
	Container  string `json:"container"`
	AppVersion string `json:"appVersion"`
	Locale     string `json:"locale"`
	Origin     string `json:"origin"`
}

type RouteResolveResult struct {
	AppURL        string `json:"appUrl"`
	SurfaceID     string `json:"surfaceId"`
	RuntimePlane  string `json:"runtimePlane"`
	RequiresLogin bool   `json:"requiresLogin"`
	Fallback      string `json:"fallback,omitempty"`
	HandledBy     string `json:"handledBy"`
	ExternalURL   string `json:"externalUrl,omitempty"`
}

type CapabilityCheckRequest struct {
	Container             string   `json:"container"`
	AppVersion            string   `json:"appVersion"`
	MinimumBridgeVersion  string   `json:"minimumBridgeVersion"`
	RequestedCapabilities []string `json:"requestedCapabilities"`
}

type CapabilityCheckResult struct {
	HostAPIVersion      string          `json:"hostApiVersion"`
	SupportedNamespaces []string        `json:"supportedNamespaces"`
	DisabledNamespaces  []string        `json:"disabledNamespaces"`
	Supports            map[string]bool `json:"supports"`
}

type WebBootstrapUser struct {
	ID          string `json:"id,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
	AvatarURL   string `json:"avatarUrl,omitempty"`
}

type WebBootstrapResponse struct {
	GeneratedAt        string            `json:"generatedAt"`
	BridgeVersion      string            `json:"bridgeVersion"`
	User               *WebBootstrapUser `json:"user,omitempty"`
	SessionExpiresAt   string            `json:"sessionExpiresAt,omitempty"`
	Theme              string            `json:"theme"`
	Locale             string            `json:"locale"`
	Flags              map[string]any    `json:"flags"`
	DisabledNamespaces []string          `json:"disabledNamespaces"`
	Supports           map[string]bool   `json:"supports"`
	PageContext        map[string]any    `json:"pageContext"`
}

type AnalyticsEvent struct {
	Name         string         `json:"name"`
	Source       string         `json:"source"`
	SurfaceID    string         `json:"surfaceId"`
	RuntimePlane string         `json:"runtimePlane"`
	RequestID    string         `json:"requestId"`
	SentAt       string         `json:"sentAt"`
	Payload      map[string]any `json:"payload"`
}

type AnalyticsIngestResult struct {
	Accepted  int    `json:"accepted"`
	Dropped   int    `json:"dropped"`
	RequestID string `json:"requestId,omitempty"`
}

// resolvedRoute is the internal route shape before governance.
type resolvedRoute struct {
	appURL        string
	surfaceID     string
	runtimePlane  string
	requiresLogin bool
	fallback      string
	handledBy     string
	externalURL   string
}

// ---- core logic ------------------------------------------------------------

func (s *Service) getRuntimeConfig(query routeQuery) RuntimeConfig {
	overrides := map[string]string{}
	for k, v := range defaultRouteRuntimeOverrides {
		overrides[k] = v
	}
	for k, v := range s.resolveRouteRuntimeOverrides() {
		overrides[k] = v
	}
	if surface := resolveCurrentWebSurface(query.path); surface != "" {
		overrides[surface] = "web"
	}
	return RuntimeConfig{
		BootstrapVersion:        firstNonEmpty(os.Getenv("MOBILE_BOOTSTRAP_VERSION"), defaultBootstrapVersion),
		DisabledNamespaces:      s.resolveDisabledNamespaces(),
		RouteRuntimeOverrides:   overrides,
		KilledRoutes:            parseCSV(os.Getenv("MOBILE_KILLED_ROUTES")),
		Flags:                   s.resolveFlags(),
		FirstPartyWebOrigins:    s.resolveFirstPartyOrigins(query.origin),
		ObservabilitySampleRate: s.resolveObservabilitySampleRate(),
	}
}

func (s *Service) getSessionContext(r *http.Request, query routeQuery) SessionContext {
	loggedIn, userID := s.resolveAuthState(r)
	var uid *string
	if loggedIn && userID != "" {
		uid = &userID
	}
	var appVer *string
	if v := resolveAppVersion(r, query.appVersion); v != "" {
		appVer = &v
	}
	return SessionContext{
		IsLoggedIn:         loggedIn,
		UserID:             uid,
		Locale:             resolveLocale(r, query.locale),
		Theme:              resolveTheme(r, query.theme),
		AppVersion:         appVer,
		Container:          resolveContainer(r, query.container),
		DisabledNamespaces: s.resolveDisabledNamespaces(),
	}
}

func (s *Service) getBootstrap(r *http.Request, query routeQuery) RuntimeBootstrap {
	cfg := s.getRuntimeConfig(query)
	session := s.getSessionContext(r, query)
	return RuntimeBootstrap{
		BootstrapVersion:        cfg.BootstrapVersion,
		RouteRuntimeOverrides:   cfg.RouteRuntimeOverrides,
		KilledRoutes:            cfg.KilledRoutes,
		Flags:                   cfg.Flags,
		FirstPartyWebOrigins:    cfg.FirstPartyWebOrigins,
		ObservabilitySampleRate: cfg.ObservabilitySampleRate,
		IsLoggedIn:              session.IsLoggedIn,
		UserID:                  session.UserID,
		Locale:                  session.Locale,
		Theme:                   session.Theme,
		AppVersion:              session.AppVersion,
		Container:               session.Container,
		DisabledNamespaces:      session.DisabledNamespaces,
	}
}

func (s *Service) requireLogin(r *http.Request, reason string) SessionRequirementResult {
	session := s.getSessionContext(r, routeQuery{})
	if session.IsLoggedIn {
		return SessionRequirementResult{Granted: true}
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "login-required"
	}
	return SessionRequirementResult{Granted: false, Reason: reason}
}

func (s *Service) resolveRoute(r *http.Request, body RouteResolveRequest) RouteResolveResult {
	container := resolveContainer(r, body.Container)
	origin := body.Origin
	if origin == "" {
		origin = readHeader(r, "origin")
	}
	cfg := s.getRuntimeConfig(routeQuery{origin: origin})
	base := s.resolveRouteDefinition(body.AppURL, origin)
	plane := applyRouteGovernance(base, cfg)
	return RouteResolveResult{
		AppURL:        body.AppURL,
		SurfaceID:     base.surfaceID,
		RuntimePlane:  plane,
		RequiresLogin: base.requiresLogin,
		Fallback:      base.fallback,
		HandledBy:     runtimeHandledBy(plane, container),
		ExternalURL:   base.externalURL,
	}
}

func (s *Service) checkCapabilities(r *http.Request, body CapabilityCheckRequest) CapabilityCheckResult {
	container := resolveContainer(r, body.Container)
	disabled := s.resolveDisabledNamespaces()
	flags := s.resolveFlags()
	requested := body.RequestedCapabilities
	if len(requested) == 0 {
		requested = supportedCapabilities
	}
	supports := map[string]bool{}
	for _, cap := range requested {
		supports[cap] = isCapabilitySupported(cap, container, disabled, flags)
	}
	supported := []string{}
	for _, ns := range allowedNamespaces {
		if !contains(disabled, ns) {
			supported = append(supported, ns)
		}
	}
	return CapabilityCheckResult{
		HostAPIVersion:      defaultHostAPIVersion,
		SupportedNamespaces: supported,
		DisabledNamespaces:  disabled,
		Supports:            supports,
	}
}

func (s *Service) getWebBootstrap(r *http.Request, query routeQuery) WebBootstrapResponse {
	session := s.getSessionContext(r, query)
	cfg := s.getRuntimeConfig(query)
	capResult := s.checkCapabilities(r, CapabilityCheckRequest{Container: session.Container})
	var routeResult *RouteResolveResult
	if query.appURL != "" {
		rr := s.resolveRoute(r, RouteResolveRequest{
			AppURL: query.appURL, Container: query.container, AppVersion: query.appVersion,
			Locale: query.locale, Origin: query.origin,
		})
		routeResult = &rr
	}
	fallbackSurface := query.surfaceID
	if fallbackSurface == "" {
		if cur := resolveCurrentWebSurface(query.path); cur != "" {
			fallbackSurface = cur
		} else {
			fallbackSurface = "web-runtime-entry"
		}
	}
	var user *WebBootstrapUser
	if session.IsLoggedIn {
		id := ""
		if session.UserID != nil {
			id = *session.UserID
		}
		user = &WebBootstrapUser{ID: id, DisplayName: id}
	}
	surfaceID := fallbackSurface
	plane := "web"
	fallback := query.path
	if fallback == "" {
		fallback = "/"
	}
	if routeResult != nil {
		surfaceID = routeResult.SurfaceID
		plane = routeResult.RuntimePlane
		if routeResult.Fallback != "" {
			fallback = routeResult.Fallback
		}
	}
	pathVal := query.path
	if pathVal == "" {
		pathVal = "/"
	}
	return WebBootstrapResponse{
		GeneratedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		BridgeVersion:      defaultHostAPIVersion,
		User:               user,
		SessionExpiresAt:   s.resolveWebSessionExpiresAt(session.IsLoggedIn),
		Theme:              session.Theme,
		Locale:             session.Locale,
		Flags:              cfg.Flags,
		DisabledNamespaces: cfg.DisabledNamespaces,
		Supports:           capResult.Supports,
		PageContext: map[string]any{
			"path":         pathVal,
			"appUrl":       query.appURL,
			"origin":       query.origin,
			"container":    session.Container,
			"surfaceId":    surfaceID,
			"runtimePlane": plane,
			"fallback":     fallback,
		},
	}
}

func ingestAnalytics(r *http.Request, events []AnalyticsEvent) AnalyticsIngestResult {
	requestID := extractRequestID(r)
	// NestJS logs each event; we accept all (no drop) — same observable result.
	return AnalyticsIngestResult{Accepted: len(events), Dropped: 0, RequestID: requestID}
}

// ---- resolution helpers (port of the private methods) ----------------------

func (s *Service) resolveAuthState(r *http.Request) (bool, string) {
	token := extractBearerToken(r)
	if token == "" || s.validator == nil {
		return false, ""
	}
	if userID, ok := s.validator.ValidateToken(r.Context(), token); ok {
		return true, userID
	}
	return false, ""
}

func (s *Service) resolveRouteDefinition(appURL, currentOrigin string) resolvedRoute {
	trimmed := strings.TrimSpace(appURL)
	if trimmed == "" {
		return resolvedRoute{appURL: appURL, surfaceID: "web-runtime-entry", runtimePlane: "web",
			requiresLogin: false, fallback: "/", handledBy: "web-container"}
	}
	parsed, err := url.Parse(trimmed)
	if err == nil {
		switch parsed.Scheme {
		case "app":
			return s.resolveAppSchemeRoute(trimmed, parsed, currentOrigin)
		case "http", "https":
			return s.resolveHTTPRoute(trimmed, parsed, currentOrigin)
		}
	}
	return resolvedRoute{appURL: appURL, surfaceID: "external-link", runtimePlane: "system_browser",
		requiresLogin: false, externalURL: trimmed, handledBy: "system-browser"}
}

func (s *Service) resolveAppSchemeRoute(rawURL string, parsed *url.URL, currentOrigin string) resolvedRoute {
	routeKey := normalizeAppRoute(parsed)
	q := parsed.Query()
	switch routeKey {
	case "feed/home":
		return createResolvedRoute(rawURL, "feed-home", "flutter", nil)
	case "article/detail":
		return createResolvedRoute(rawURL, "article-detail", "flutter", nil)
	case "comment/sheet":
		return createResolvedRoute(rawURL, "comment-sheet", "flutter", nil)
	case "search/index":
		return createResolvedRoute(rawURL, "search-index", "flutter", nil)
	case "message/center":
		return createResolvedRoute(rawURL, "message-center", "flutter", &resolvedRoute{requiresLogin: true})
	case "profile/home":
		return createResolvedRoute(rawURL, "profile-home", "flutter", nil)
	case "settings/index":
		return createResolvedRoute(rawURL, "settings-index", "flutter", &resolvedRoute{requiresLogin: true})
	case "video/feed":
		return createResolvedRoute(rawURL, "video-feed", "native_media", nil)
	case "video/detail":
		return createResolvedRoute(rawURL, "video-detail", "native_media", nil)
	case "native/settings":
		return createResolvedRoute(rawURL, "native-settings", "native", &resolvedRoute{requiresLogin: true})
	case "topic/landing":
		slug := orDefault(q.Get("slug"), "scholar-path")
		return createResolvedRoute(rawURL, "topic-landing", "web", &resolvedRoute{fallback: "/topic/" + slug})
	case "campaign/detail":
		slug := orDefault(q.Get("slug"), "spring-launch-2026")
		return createResolvedRoute(rawURL, "campaign-detail", "web", &resolvedRoute{fallback: "/campaign/" + slug})
	case "help/detail":
		slug := orDefault(q.Get("slug"), "bridge-gateway")
		return createResolvedRoute(rawURL, "help-detail", "web", &resolvedRoute{fallback: "/help/" + slug})
	case "legal/detail":
		slug := orDefault(q.Get("slug"), "privacy")
		return createResolvedRoute(rawURL, "legal-detail", "web", &resolvedRoute{fallback: "/legal/" + slug})
	case "web/open":
		target := q.Get("url")
		if target == "" {
			return createResolvedRoute(rawURL, "external-link", "system_browser", nil)
		}
		return s.resolveRouteDefinition(target, currentOrigin)
	default:
		return createResolvedRoute(rawURL, "external-link", "system_browser", &resolvedRoute{externalURL: rawURL})
	}
}

func (s *Service) resolveHTTPRoute(rawURL string, parsed *url.URL, currentOrigin string) resolvedRoute {
	origin := parsed.Scheme + "://" + parsed.Host
	if !s.isFirstPartyOrigin(origin, currentOrigin) {
		return createResolvedRoute(rawURL, "external-link", "system_browser", &resolvedRoute{externalURL: rawURL})
	}
	fallbackPath := buildPathWithSearch(parsed.Path, parsed.RawQuery)
	switch {
	case strings.HasPrefix(parsed.Path, "/campaign/"):
		return createResolvedRoute(rawURL, "campaign-detail", "web", &resolvedRoute{fallback: fallbackPath})
	case strings.HasPrefix(parsed.Path, "/topic/"):
		return createResolvedRoute(rawURL, "topic-landing", "web", &resolvedRoute{fallback: fallbackPath})
	case strings.HasPrefix(parsed.Path, "/help/"):
		return createResolvedRoute(rawURL, "help-detail", "web", &resolvedRoute{fallback: fallbackPath})
	case strings.HasPrefix(parsed.Path, "/legal/"):
		return createResolvedRoute(rawURL, "legal-detail", "web", &resolvedRoute{fallback: fallbackPath})
	default:
		return createResolvedRoute(rawURL, "web-runtime-entry", "web", &resolvedRoute{fallback: fallbackPath})
	}
}

func createResolvedRoute(appURL, surfaceID, plane string, override *resolvedRoute) resolvedRoute {
	rr := resolvedRoute{appURL: appURL, surfaceID: surfaceID, runtimePlane: plane,
		requiresLogin: false, handledBy: runtimeHandledBy(plane, "native-webview")}
	if override != nil {
		if override.requiresLogin {
			rr.requiresLogin = true
		}
		if override.fallback != "" {
			rr.fallback = override.fallback
		}
		if override.externalURL != "" {
			rr.externalURL = override.externalURL
		}
	}
	return rr
}

func applyRouteGovernance(route resolvedRoute, cfg RuntimeConfig) string {
	plane := route.runtimePlane
	if ov, ok := cfg.RouteRuntimeOverrides[route.surfaceID]; ok && ov != "" {
		plane = ov
	}
	if contains(cfg.KilledRoutes, route.surfaceID) && strings.TrimSpace(route.fallback) != "" {
		return "web"
	}
	return plane
}

func runtimeHandledBy(plane, container string) string {
	switch plane {
	case "system_browser":
		return "system-browser"
	case "web":
		return "web-container"
	case "flutter":
		return "flutter-container"
	case "native_media":
		return "native-media-container"
	case "native", "native_shell":
		if container == "browser" {
			return "host-shell"
		}
		return "native-container"
	default:
		return "host-shell"
	}
}

func isCapabilitySupported(capability, container string, disabled []string, flags map[string]any) bool {
	ns := strings.SplitN(capability, ".", 2)[0]
	if contains(disabled, ns) {
		return false
	}
	if container == "browser" && !browserSafeCapabilities[capability] {
		return false
	}
	switch capability {
	case "comment.openSheet":
		return truthy(flags["runtime.flutter.add_to_app"])
	case "video.enterFullscreen":
		return flags["runtime.native_media.enabled"] != false
	default:
		return true
	}
}

// ---- config/env helpers ----------------------------------------------------

func (s *Service) resolveDisabledNamespaces() []string {
	out := []string{}
	for _, v := range parseCSV(os.Getenv("MOBILE_DISABLED_NAMESPACES")) {
		if contains(allowedNamespaces, v) {
			out = append(out, v)
		}
	}
	return out
}

func (s *Service) resolveFlags() map[string]any {
	out := map[string]any{}
	for k, v := range defaultFlags {
		out[k] = v
	}
	raw := os.Getenv("MOBILE_RUNTIME_FLAGS")
	if raw == "" {
		return out
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err == nil {
		for k, v := range parsed {
			out[k] = v
		}
	}
	return out
}

func (s *Service) resolveRouteRuntimeOverrides() map[string]string {
	raw := os.Getenv("MOBILE_ROUTE_RUNTIME_OVERRIDES")
	out := map[string]string{}
	if raw == "" {
		return out
	}
	var parsed map[string]string
	if err := json.Unmarshal([]byte(raw), &parsed); err == nil {
		return parsed
	}
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.SplitN(entry, ":", 2)
		if len(parts) != 2 {
			continue
		}
		surface := strings.TrimSpace(parts[0])
		plane := strings.TrimSpace(parts[1])
		if surface == "" || plane == "" {
			continue
		}
		out[surface] = plane
	}
	return out
}

func (s *Service) resolveFirstPartyOrigins(currentOrigin string) []string {
	origins := parseCSV(os.Getenv("MOBILE_FIRST_PARTY_WEB_ORIGINS"))
	if len(origins) == 0 {
		origins = append([]string{}, defaultFirstPartyOrigins...)
	}
	if currentOrigin != "" && !contains(origins, currentOrigin) {
		return append(origins, currentOrigin)
	}
	return origins
}

func (s *Service) resolveObservabilitySampleRate() float64 {
	raw := os.Getenv("MOBILE_OBSERVABILITY_SAMPLE_RATE")
	parsed, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 1
	}
	if parsed < 0 {
		return 0
	}
	if parsed > 1 {
		return 1
	}
	return parsed
}

func (s *Service) resolveWebSessionExpiresAt(isLoggedIn bool) string {
	if !isLoggedIn {
		return ""
	}
	ttl := 1800
	if v := os.Getenv("MOBILE_WEB_SESSION_TTL_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			ttl = n
		}
	}
	if ttl <= 0 {
		return ""
	}
	return time.Now().UTC().Add(time.Duration(ttl) * time.Second).Format(time.RFC3339Nano)
}

func (s *Service) isFirstPartyOrigin(origin, currentOrigin string) bool {
	return contains(s.resolveFirstPartyOrigins(currentOrigin), origin)
}
