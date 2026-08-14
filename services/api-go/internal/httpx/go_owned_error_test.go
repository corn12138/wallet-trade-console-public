package httpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestGoOwnedUnmatchedRouteIdentity locks in the final-wave contract: after
// the generic /api/ nginx fallback points at Go, an unmatched path at ANY
// depth must answer with the Go-owned JSON body ({statusCode,message,source:
// "api-go"}) and never the NestJS {code,message,data} envelope or chi's
// text/plain default. production-cutover-stability-smoke.sh's built-in 404
// probe depends on exactly this shape.
func TestGoOwnedUnmatchedRouteIdentity(t *testing.T) {
	r := NewRouter(Deps{MarketsLoader: emptyLoader{}})

	cases := []struct {
		name   string
		method string
		path   string
		want   int
	}{
		{"top-level unknown", http.MethodGet, "/__no-such-route", http.StatusNotFound},
		{"api-level unknown (generic fallback probe)", http.MethodGet, "/api/__wallet-trade-cutover-404-probe", http.StatusNotFound},
		{"deep module unknown", http.MethodGet, "/api/markets/__no-such-subroute", http.StatusNotFound},
		{"method not allowed", http.MethodPut, "/healthz", http.StatusMethodNotAllowed},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if rec.Code != tc.want {
				t.Fatalf("%s %s: status = %d, want %d", tc.method, tc.path, rec.Code, tc.want)
			}
			if ct := rec.Header().Get("Content-Type"); ct == "" || ct[:16] != "application/json" {
				t.Fatalf("%s %s: Content-Type = %q, want application/json", tc.method, tc.path, ct)
			}

			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("%s %s: body is not JSON: %v (%q)", tc.method, tc.path, err, rec.Body.String())
			}
			if got, _ := body["source"].(string); got != "api-go" {
				t.Fatalf("%s %s: source = %q, want \"api-go\"", tc.method, tc.path, got)
			}
			if got, _ := body["statusCode"].(float64); int(got) != tc.want {
				t.Fatalf("%s %s: statusCode = %v, want %d", tc.method, tc.path, body["statusCode"], tc.want)
			}
			// Never the NestJS envelope: the cutover smoke treats top-level
			// code+message+data as "NestJS answered" and fails the cut.
			if _, hasCode := body["code"]; hasCode {
				if _, hasData := body["data"]; hasData {
					t.Fatalf("%s %s: body carries the NestJS {code,...,data} envelope: %v", tc.method, tc.path, body)
				}
			}
		})
	}
}
