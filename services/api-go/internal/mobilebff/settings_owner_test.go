package mobilebff

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

const ownerA = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const ownerB = "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

// recordingSettings returns owner-specific values so a leak is visible, and
// records every owner whose private preferences were read.
type recordingSettings struct{ reads []string }

func (s *recordingSettings) NotificationsEnabled(_ context.Context, owner string) bool {
	s.reads = append(s.reads, owner)
	return owner == ownerB
}
func (s *recordingSettings) MarketAlerts(_ context.Context, owner string) bool {
	return owner == ownerB
}

func settingsValues(t *testing.T, router http.Handler, query string) (map[string]any, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/mobile/settings/index"+query, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var payload SettingsIndexResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	values, linked := map[string]any{}, ""
	for _, section := range payload.Sections {
		for _, item := range section.Items {
			switch item.ID {
			case "notifications-enabled", "market-alerts":
				values[item.ID] = item.Value
			case "owner-address":
				linked = item.Subtitle
			}
		}
	}
	return values, linked
}

// Wallet-keyed preferences are read only for the wallet the request proves;
// naming another wallet (or none being proven) yields the public guest shell.
func TestSettingsIndexReadsOnlyTheProvenWallet(t *testing.T) {
	cases := []struct {
		name   string
		proven string
		query  string
		reads  string
		linked string
	}{
		{"anonymous naming A", "", "?ownerAddress=" + ownerA, "", "Not linked"},
		{"anonymous naming B", "", "?ownerAddress=" + ownerB, "", "Not linked"},
		{"A without a hint", ownerA, "", ownerA, ownerA},
		{"A naming B", ownerA, "?ownerAddress=" + ownerB, "", "Not linked"},
		{"A naming itself in checksum case", ownerA, "?ownerAddress=0x" + strings.ToUpper(ownerA[2:]), ownerA, ownerA},
		{"A naming a non-address", ownerA, "?ownerAddress=" + strings.ToUpper(ownerA[2:]), "", "Not linked"},
		{"A naming itself", ownerA, "?ownerAddress=" + ownerA, ownerA, ownerA},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			settings := &recordingSettings{}
			svc := NewService(NewRepository(nil), nil, nil, settings).WithWalletResolver(func(*http.Request) string { return c.proven })
			r := chi.NewRouter()
			r.Route("/api/mobile", func(r chi.Router) { Register(r, svc) })
			values, linked := settingsValues(t, r, c.query)
			if strings.Join(settings.reads, ",") != c.reads || linked != c.linked {
				t.Fatalf("reads %v linked %q", settings.reads, linked)
			}
			if c.reads == "" && (values["notifications-enabled"] != false || values["market-alerts"] != "Manual") {
				t.Fatalf("guest shell must carry only public defaults: %v", values)
			}
		})
	}
	settings := &recordingSettings{}
	r := chi.NewRouter()
	r.Route("/api/mobile", func(r chi.Router) { Register(r, NewService(NewRepository(nil), nil, nil, settings)) })
	settingsValues(t, r, "?ownerAddress="+ownerB)
	if len(settings.reads) != 0 {
		t.Fatal("without a wallet resolver no private preference may be read")
	}
}
