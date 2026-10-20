package campaign

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/go-chi/chi/v5"
)

func TestEnrichmentSeparatesWalletStateFromSharedRows(t *testing.T) {
	reader := &stubCampaignReader{counts: map[string]int{"c": 3}, states: map[string]WalletState{"c": {IsParticipating: true, HasReminder: true}}}
	svc := &Service{repo: reader}
	rows := []Campaign{{ID: "c"}}
	mine, err := svc.enrichForWallet(t.Context(), rows, testWallet)
	if err != nil {
		t.Fatal(err)
	}
	anonymous, err := svc.enrichForWallet(t.Context(), rows, "")
	if err != nil {
		t.Fatal(err)
	}
	if mine[0].ParticipantCount != 3 || mine[0].IsParticipating == nil || !*mine[0].IsParticipating || mine[0].HasReminder == nil || !*mine[0].HasReminder {
		t.Fatalf("wallet state missing: %+v", mine[0])
	}
	if anonymous[0].IsParticipating != nil || anonymous[0].HasReminder != nil || rows[0].IsParticipating != nil || rows[0].HasReminder != nil {
		t.Fatal("wallet fields leaked into shared or anonymous rows")
	}
	reader.stateErr = errors.New("private state unavailable")
	if out, err := svc.enrichForWallet(t.Context(), rows, testWallet); err == nil || out != nil {
		t.Fatal("private state failure became false")
	}
}

func TestInteractionRouteHelperAlsoRetiresWrites(t *testing.T) {
	v := campaignAuth.Verifier
	for _, guard := range []func(http.Handler) http.Handler{nil, auth.StrictMiddleware(v)} {
		router := chi.NewRouter()
		RegisterInteractionRoutes(router, nil, guard)
		for _, method := range []string{"POST", "DELETE"} {
			for _, path := range []string{"/c/join", "/c/reminder"} {
				req := httptest.NewRequest(method, path, nil)
				req.Header.Set("Authorization", "Bearer "+campaignToken(t))
				out := httptest.NewRecorder()
				router.ServeHTTP(out, req)
				want := http.StatusConflict
				if guard == nil {
					want = http.StatusUnauthorized
				}
				if out.Code != want {
					t.Fatalf("%s %s status=%d", method, path, out.Code)
				}
			}
		}
	}
}

func TestRepositoryReadErrorsDoNotClaimNotFound(t *testing.T) {
	svc := NewService(NewRepository(nil))
	if _, err := svc.FindByID(t.Context(), "c"); !errors.Is(err, ErrPoolUnavailable) {
		t.Fatal(err)
	}
}
