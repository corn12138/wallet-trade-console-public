package papertrade

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEnabledDefaultsOff(t *testing.T) {
	t.Setenv("PAPER_TRADE_ENABLED", "")
	if Enabled() {
		t.Fatalf("paper trading enabled without the env flag")
	}
	t.Setenv("PAPER_TRADE_ENABLED", "0")
	if Enabled() {
		t.Fatalf("PAPER_TRADE_ENABLED=0 still enabled")
	}
	t.Setenv("PAPER_TRADE_ENABLED", "1")
	if !Enabled() {
		t.Fatalf("PAPER_TRADE_ENABLED=1 not enabled")
	}
}

func TestDisabledRouterRefusesReadsAndWrites(t *testing.T) {
	r := DisabledRouter()
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		req := httptest.NewRequest(method, "/", strings.NewReader(`{}`))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s status = %d, want 503", method, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "disabled") {
			t.Errorf("%s body lacks explicit disabled message: %s", method, rec.Body.String())
		}
	}
}
