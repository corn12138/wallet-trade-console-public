//go:build integration

package campaignhttp_test

import (
	"testing"

	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
)

// The administrator view includes unpublished drafts and lock facts, so it is
// held to the same private/admin boundary as revision preview.
func TestHTTPAdminCampaignViewIsPrivateAndAdministratorOnly(t *testing.T) {
	f := newRealHTTP(t)
	created := f.create(t)
	id := created.Original.CampaignID
	path := "/campaign-admin/campaigns/" + id
	errorCode(t, f.request(t, "GET", path, "", nil, 401), "UNAUTHORIZED")
	errorCode(t, f.request(t, "GET", path, f.owner, nil, 403), "CAMPAIGN_ADMIN_REQUIRED")
	errorCode(t, f.request(t, "GET", path+"?include=drafts", f.admin, nil, 400), "CAMPAIGN_INVALID_INPUT")
	errorCode(t, f.request(t, "GET", "/campaign-admin/campaigns/absent-"+httpID(t)[:8], f.admin, nil, 404), "CAMPAIGN_NOT_FOUND")

	raw := f.request(t, "GET", path, f.admin, nil, 200)
	var view v2.AdminCampaignResponse
	if err := v2.DecodeContractResponse("AdminCampaignResponse", raw, &view); err != nil {
		t.Fatalf("administrator view contract: %v", err)
	}
	if view.Item.PublicationVersion != "0" || view.Item.EffectivePhase != "unpublished" || len(view.Item.Revisions) != 1 || view.Item.Revisions[0].ID != *created.Original.RevisionID {
		t.Fatalf("draft administrator view invalid: %+v", view.Item)
	}
	f.publish(t, created)
	if err := v2.DecodeContractResponse("AdminCampaignResponse", f.request(t, "GET", path, f.admin, nil, 200), &view); err != nil {
		t.Fatal(err)
	}
	// httpInput starts an hour ago, so the published terms are already locked.
	if view.Item.PublicationVersion != "1" || view.Item.EffectivePhase != "active" || view.Item.MaterialTermsLock != v2.TermsLocked || !view.Item.Revisions[0].Active {
		t.Fatalf("published administrator view invalid: %+v", view.Item)
	}
}
