package httpx

import (
	"net/http"
	"os"
	"strings"
)

// defaultDevClientOrigins mirrors DEFAULT_DEV_CLIENT_ORIGINS in
// legacy NestJS bootstrap/cors-origins.ts.
var defaultDevClientOrigins = []string{
	"http://localhost:3000",
	"http://127.0.0.1:3000",
	"http://localhost:3002",
	"http://127.0.0.1:3002",
}

// resolveAllowedOrigins mirrors resolveAllowedOrigins() in cors-origins.ts:
// CORS_ALLOWED_ORIGINS (CSV) wins; otherwise [] in production (fail closed,
// never fall back to dev origins); otherwise the dev client origins. This lets
// the documented local "web -> local Go" flow work cross-origin while staying
// locked down in production (where FE + Go are same-origin behind nginx).
func resolveAllowedOrigins() []string {
	if configured := parseCsvEnv(os.Getenv("CORS_ALLOWED_ORIGINS")); len(configured) > 0 {
		return configured
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("NODE_ENV")), "production") {
		return nil
	}
	return defaultDevClientOrigins
}

func parseCsvEnv(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// corsMiddleware mirrors the NestJS enableCors origin policy: same-origin /
// non-browser requests (no Origin header) pass through; an allowlisted Origin
// gets the CORS headers + credentials; a disallowed Origin gets no CORS headers
// (the browser blocks the response). An allowed-origin OPTIONS preflight
// short-circuits with 204.
func corsMiddleware(allowed []string) func(http.Handler) http.Handler {
	allowSet := make(map[string]struct{}, len(allowed))
	for _, o := range allowed {
		allowSet[o] = struct{}{}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" {
				if _, ok := allowSet[origin]; ok {
					h := w.Header()
					h.Set("Access-Control-Allow-Origin", origin)
					h.Set("Access-Control-Allow-Credentials", "true")
					h.Add("Vary", "Origin")
					if r.Method == http.MethodOptions {
						h.Set("Access-Control-Allow-Methods", "GET,HEAD,PUT,PATCH,POST,DELETE,OPTIONS")
						reqHeaders := r.Header.Get("Access-Control-Request-Headers")
						if reqHeaders == "" {
							reqHeaders = "Authorization,Content-Type,X-Wallet-Address,X-Requested-With"
						}
						h.Set("Access-Control-Allow-Headers", reqHeaders)
						h.Set("Access-Control-Max-Age", "600")
						w.WriteHeader(http.StatusNoContent)
						return
					}
				} else if r.Method == http.MethodOptions {
					// Disallowed-origin preflight: end without CORS headers so the
					// browser blocks; don't fall through to a 404/405 handler.
					w.WriteHeader(http.StatusNoContent)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
