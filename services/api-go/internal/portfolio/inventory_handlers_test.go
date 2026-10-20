package portfolio

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/settings"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/staking"
)

func TestInventoryHandlers_VisibilityCodeSurvivesEarlierSummaryCounterFailure(t *testing.T) {
	f := &inventoryFixture{
		settingsErr:  settings.ErrPoolUnavailable,
		settingsWait: func(ctx context.Context) { <-ctx.Done() },
	}
	svc := fixtureService(f)
	svc.events = &stubEventCounter{err: errors.New("counter failed")}
	rec := portfolioRequest(t, svc, "/summary")
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "PORTFOLIO_VISIBILITY_UNAVAILABLE") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
	}
}

func portfolioRequest(t *testing.T, svc *Service, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req = req.WithContext(auth.WithAddress(req.Context(), validPortfolioAddr))
	rec := httptest.NewRecorder()
	Router(svc, nil, nil).ServeHTTP(rec, req)
	return rec
}

func TestInventoryHandlers_BareVersionedJSONWithNullValues(t *testing.T) {
	for _, path := range []string{"/assets", "/summary"} {
		t.Run(path, func(t *testing.T) {
			svc := fixtureService(&inventoryFixture{stakes: []staking.UserStake{{ID: "s", Amount: "100", PoolChainID: 1}}})
			rec := portfolioRequest(t, svc, path)
			if rec.Code != 200 {
				t.Fatalf("status %d: %s", rec.Code, rec.Body)
			}
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if _, wrapped := body["data"]; wrapped {
				t.Fatal("unexpected data wrapper")
			}
			if body["valuationVersion"] != float64(1) {
				t.Fatalf("body=%v", body)
			}
			valuation := body["valuation"].(map[string]any)
			if valuation["state"] != "unavailable" || valuation["knownSubtotalUsd"] != nil || valuation["unpricedCount"] != float64(1) {
				t.Fatalf("valuation=%v", valuation)
			}
			if path == "/assets" {
				item := body["items"].([]any)[0].(map[string]any)
				v, exists := item["valueUsd"]
				if !exists || v != nil || item["balanceUnit"] != "token" || item["valuationReason"] != ReasonMissingUSDPrice {
					t.Fatalf("item=%v", item)
				}
			} else if v, exists := body["portfolioValueUsd"]; !exists || v != nil {
				t.Fatal("compatibility value must be explicit null")
			}
		})
	}
}

func TestInventoryHandlers_FailureCodesAndNoAssetPayload(t *testing.T) {
	for _, test := range []struct {
		name                     string
		visibilityErr, sourceErr error
		missingSources           bool
		status                   int
		code                     string
	}{
		{"visibility unavailable", settings.ErrPoolUnavailable, nil, false, 503, "PORTFOLIO_VISIBILITY_UNAVAILABLE"},
		{"visibility error", errors.New("private database detail"), nil, false, 500, "PORTFOLIO_VISIBILITY_UNAVAILABLE"},
		{"all sources unavailable", nil, nil, true, 503, "PORTFOLIO_SOURCES_UNAVAILABLE"},
		{"source error", nil, errors.New("private database detail"), false, 500, "PORTFOLIO_READ_FAILED"},
	} {
		for _, path := range []string{"/assets", "/summary"} {
			t.Run(test.name+path, func(t *testing.T) {
				f := &inventoryFixture{settingsErr: test.visibilityErr, stakesErr: test.sourceErr, stakes: []staking.UserStake{{ID: "secret"}}}
				svc := fixtureService(f)
				if test.missingSources {
					svc = NewServiceFromDeps(Deps{Settings: f})
				}
				rec := portfolioRequest(t, svc, path)
				var body map[string]any
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if rec.Code != test.status || body["code"] != test.code {
					t.Fatalf("status=%d body=%v", rec.Code, body)
				}
				if strings.Contains(rec.Body.String(), "private database detail") || body["items"] != nil || body["valuation"] != nil {
					t.Fatalf("failure exposed asset data: %v", body)
				}
			})
		}
	}
}

func TestInventoryHandlers_OwnerGuardRemainsIntact(t *testing.T) {
	svc := fixtureService(&inventoryFixture{})
	for _, path := range []string{"/assets", "/summary"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		Router(svc, nil, nil).ServeHTTP(rec, req)
		if rec.Code != 401 {
			t.Fatalf("anonymous %s status=%d", path, rec.Code)
		}
		rec = portfolioRequest(t, svc, path+"?address=0x000000000000000000000000000000000000bEEF")
		if rec.Code != 403 {
			t.Fatalf("mismatch %s status=%d", path, rec.Code)
		}
	}
}
