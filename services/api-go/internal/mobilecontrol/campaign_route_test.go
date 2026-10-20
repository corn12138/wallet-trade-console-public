package mobilecontrol

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestCampaignRouteUsesStableIDAndKeepsLegacyLinks(t *testing.T) {
	h := router(NewService(nil))
	for _, tc := range []struct{ appURL, fallback string }{
		{"app://campaign/detail?id=stable-id", "/campaign/stable-id"},
		{"app://campaign/detail?id=stable-id&slug=mutable-title", "/campaign/stable-id"},
		{"app://campaign/detail?id=campaign%2Fwith%3Freserved%23fragment", "/campaign/campaign%2Fwith%3Freserved%23fragment"},
		{"app://campaign/detail?slug=legacy-link", "/campaign/legacy-link"},
		{"app://campaign/detail", "/campaign/spring-launch-2026"},
	} {
		t.Run(tc.appURL, func(t *testing.T) {
			body, err := json.Marshal(map[string]string{"appUrl": tc.appURL})
			if err != nil {
				t.Fatal(err)
			}
			rec := post(t, h, "/api/mobile/route/resolve", string(body), nil)
			var got RouteResolveResult
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if rec.Code != http.StatusCreated || got.SurfaceID != "campaign-detail" || got.RuntimePlane != "web" || got.Fallback != tc.fallback {
				t.Fatalf("campaign route drifted: status=%d response=%+v", rec.Code, got)
			}
		})
	}
}
