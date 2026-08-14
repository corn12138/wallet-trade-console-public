package campaign

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
)

const testWallet = "0x1111111111111111111111111111111111111111"

// withWallet simulates the SIWE middleware having verified a wallet.
func withWallet(req *http.Request, wallet string) *http.Request {
	return req.WithContext(auth.WithAddress(req.Context(), wallet))
}

func TestInteraction_RequiresWalletIdentity(t *testing.T) {
	svc := NewService(NewRepository(nil))
	r := Router(svc, nil, nil) // guard nil (dev) — the in-handler check must still 401

	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/abc/join"},
		{http.MethodDelete, "/abc/join"},
		{http.MethodPost, "/abc/reminder"},
		{http.MethodDelete, "/abc/reminder"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without wallet = %d, want 401", tc.method, tc.path, rr.Code)
		}
	}
}

func TestInteraction_WritesNeedDatabase(t *testing.T) {
	// A write against a nil pool must surface 503 — never a fake success.
	svc := NewService(NewRepository(nil))
	r := Router(svc, nil, nil)

	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/abc/join"},
		{http.MethodPost, "/abc/reminder"},
	} {
		req := withWallet(httptest.NewRequest(tc.method, tc.path, nil), testWallet)
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		if rr.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s (nil pool) = %d, want 503", tc.method, tc.path, rr.Code)
		}
		if !strings.Contains(rr.Body.String(), "unavailable") {
			t.Errorf("%s %s body = %s", tc.method, tc.path, rr.Body.String())
		}
	}
}

func TestInteraction_GuardWrapsMutations(t *testing.T) {
	deny := func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		})
	}
	svc := NewService(NewRepository(nil))
	r := Router(svc, nil, deny)

	req := withWallet(httptest.NewRequest(http.MethodPost, "/abc/join", nil), testWallet)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("guarded join = %d, want 401 from middleware", rr.Code)
	}

	// Public reads stay reachable under the same router.
	listReq := httptest.NewRequest(http.MethodGet, "/", nil)
	listRR := httptest.NewRecorder()
	r.ServeHTTP(listRR, listReq)
	if listRR.Code != http.StatusOK {
		t.Errorf("public list with guard wired = %d, want 200", listRR.Code)
	}
}

func TestEnrichForWallet_DegradesWithoutPool(t *testing.T) {
	svc := NewService(NewRepository(nil))
	campaigns := []Campaign{{ID: "c1"}, {ID: "c2"}}
	out := svc.enrichForWallet(t.Context(), campaigns, testWallet)
	if len(out) != 2 {
		t.Fatalf("len = %d", len(out))
	}
	for _, c := range out {
		if c.ParticipantCount != 0 {
			t.Errorf("count = %d, want 0 (degraded)", c.ParticipantCount)
		}
		if c.IsParticipating == nil || *c.IsParticipating {
			t.Errorf("isParticipating = %v, want present+false for authed wallet", c.IsParticipating)
		}
	}
	// Anonymous requests must not get the per-wallet fields at all.
	anon := svc.enrichForWallet(t.Context(), []Campaign{{ID: "c1"}}, "")
	if anon[0].IsParticipating != nil || anon[0].HasReminder != nil {
		t.Errorf("anonymous enrichment leaked wallet fields: %+v", anon[0])
	}
}
