package httpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/article"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/contractconfig"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/txreview"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/web3auth"
)

// TestRouter_RawResponseContract locks the response contract decided in
// docs/migration/backend-go/adr/0005-response-contract-raw-payloads.md:
//
//	Go serves the RAW payload. It must NOT wrap 2xx bodies in the deployed
//	NestJS global TransformInterceptor envelope {code, message, data, …}.
//
// The frontend consumes Go's raw shape (markets already cut over this way; the
// shared clients tolerate raw via `data.data || data`). If a future change adds
// an envelope middleware to Go it would silently break the live markets cutover
// — this test fails first, forcing a revisit of ADR 0005.
func TestRouter_RawResponseContract(t *testing.T) {
	t.Setenv("NODE_ENV", "production")

	r := NewRouter(Deps{
		MarketsLoader:  emptyLoader{},
		AuthVerifier:   auth.NewVerifier(testWeb3Secret),
		UserVerifier:   auth.NewUserVerifier("test-access-secret", "test-refresh-secret", time.Hour, time.Hour),
		CsrfSigner:     &auth.CsrfSigner{},
		ContractConfig: &contractconfig.Service{},
		TxReview:       &txreview.Service{},
		Web3Auth:       &web3auth.Service{},
		ArticleRepo:    &article.Repository{},
	})

	get := func(path string) (int, []byte) {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Code, rec.Body.Bytes()
	}

	// isEnvelope reports whether a body is a JSON object carrying the NestJS
	// success-envelope triple. Arrays, raw objects, and non-JSON bodies (e.g. a
	// plaintext guard 401) are never envelopes.
	isEnvelope := func(body []byte) bool {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(body, &m); err != nil {
			return false
		}
		_, hasCode := m["code"]
		_, hasMessage := m["message"]
		_, hasData := m["data"]
		return hasCode && hasMessage && hasData
	}

	// No deployed public endpoint may return the NestJS success envelope. These
	// are the degrade-safe public reads the guard-parity test already proves are
	// reachable without a token.
	for _, p := range []string{
		"/healthz",
		"/readyz",
		"/api/markets/symbols?chainId=1",
		"/api/trading/markets?chainId=1",
		"/api/tags",
	} {
		if _, body := get(p); isEnvelope(body) {
			t.Errorf("GET %s returned a NestJS-style {code,message,data} envelope; ADR 0005 requires raw Go payloads", p)
		}
	}

	// Strongest single proof: a list endpoint returns a bare JSON array. An
	// envelope is always a JSON object, so a top-level `[` can never be wrapped.
	if status, body := get("/api/markets/symbols?chainId=1"); status == http.StatusOK {
		if trimmed := strings.TrimSpace(string(body)); !strings.HasPrefix(trimmed, "[") {
			t.Errorf("GET /api/markets/symbols 200 body = %q, want a bare JSON array (raw, un-enveloped)", trimmed)
		}
	}
}
