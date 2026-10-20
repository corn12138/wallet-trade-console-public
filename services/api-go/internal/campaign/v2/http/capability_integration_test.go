//go:build integration

package campaignhttp_test

import (
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
)

// liveCapabilities stands in for the registries the API wires at startup;
// a test withdraws or restores one entry between requests.
type liveCapabilities struct {
	mu            sync.Mutex
	media, market bool
}

func (c *liveCapabilities) set(media, market bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.media, c.market = media, market
}

func (c *liveCapabilities) current() v2.Capabilities {
	c.mu.Lock()
	defer c.mu.Unlock()
	caps := v2.Capabilities{Templates: map[string]bool{"campaignCardV1": true}, MediaAssets: map[string]bool{}, TradeMarkets: map[int64]map[string]bool{}}
	if c.media {
		registry, err := v2.TrustedMedia()
		if err != nil {
			panic(err)
		}
		caps.MediaAssets = registry.AssetIDs()
	}
	if c.market {
		caps.TradeMarkets[11155111] = map[string]bool{"ETH-USD": true}
	}
	return caps
}

// tradeInput is upcoming: later packages share this database without these
// registries, and an upcoming campaign stays out of their active listings.
func tradeInput() v2.RevisionInput {
	input := httpInput()
	input.MaterialTerms.StartAt = time.Now().Add(24 * time.Hour).UTC().Format("2006-01-02T15:04:05Z")
	input.MaterialTerms.EndAt = time.Now().Add(48 * time.Hour).UTC().Format("2006-01-02T15:04:05Z")
	asset, chain, market := "trading-week-banner-v1", int64(11155111), "ETH-USD"
	input.DisplayContent.MediaAssetID = &asset
	input.Cta = v2.CTA{ActionKind: "openTrade", ChainID: &chain, MarketID: &market}
	return input
}

// detailState reads the public detail as a client advertising variants ("" for a base client).
func (f *realHTTPFixture) detailState(t *testing.T, id, variants string) v2.QueryPublicCampaignView {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, f.server.URL+"/api/v2/campaigns/"+id, nil)
	if err != nil {
		t.Fatal(err)
	}
	if variants != "" {
		req.Header.Set(v2.VariantsHeader, variants)
	}
	response, err := f.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	var detail v2.QueryDetailResponse
	if response.StatusCode != http.StatusOK || v2.DecodeContractResponse("QueryDetailResponse", raw, &detail) != nil {
		t.Fatalf("detail: %d %s", response.StatusCode, raw)
	}
	return detail.Item
}

const capable = v2.VariantContentCapabilities

// Media and trade entries are admitted only from the live registries; a
// withdrawal closes new admission (here a reminder) and keeps cancellation
// and history.
func TestRegistryCapabilitiesGateMediaAndTradeEntries(t *testing.T) {
	caps := &liveCapabilities{media: true, market: true}
	f := newRealHTTPWith(t, caps.current)
	unknown := tradeInput()
	other := "not-in-registry"
	unknown.DisplayContent.MediaAssetID = &other
	errorCode(t, f.request(t, "POST", "/campaign-admin/campaigns", f.admin, v2.CreateCommandInput{ClientCommandID: httpID(t), Reason: "unknown media", Revision: unknown}, 422), "CAMPAIGN_UNSUPPORTED")
	created := f.adminCommand(t, "POST", "/campaign-admin/campaigns", v2.CreateCommandInput{ClientCommandID: httpID(t), Reason: "media and trade entry", Revision: tradeInput()}, 201)
	id := created.Original.CampaignID
	f.publish(t, created)
	// A client without its own media and trade registries renders it read-only.
	if item := f.detailState(t, id, ""); item.ContentState != "unsupported" || item.Content != nil {
		t.Fatalf("a base client must not receive admissible media/trade content: %+v", item)
	}
	if item := f.detailState(t, id, capable); item.ContentState != "ready" || item.Content == nil || *item.Content.DisplayContent.MediaAssetID != "trading-week-banner-v1" || item.Content.Cta.ActionKind != "openTrade" {
		t.Fatalf("admitted content: %+v", item)
	}
	zero, one := "0", "1"
	f.command(t, "POST", "/campaigns/"+id+"/reminder", f.owner, v2.PositiveInteractionInput{ClientCommandID: httpID(t), ExpectedStateVersion: zero, ExpectedPublicationVersion: one}, 200)

	for _, withdrawn := range []struct {
		name          string
		media, market bool
	}{{"market withdrawn", true, false}, {"media withdrawn", false, true}} {
		caps.set(withdrawn.media, withdrawn.market)
		if item := f.detailState(t, id, capable); item.ContentState != "unsupported" || item.Content != nil {
			t.Fatalf("%s: content must become read-only: %+v", withdrawn.name, item)
		}
		errorCode(t, f.request(t, "POST", "/campaigns/"+id+"/reminder", f.other, v2.PositiveInteractionInput{ClientCommandID: httpID(t), ExpectedStateVersion: zero, ExpectedPublicationVersion: one}, 422), "CAMPAIGN_UNSUPPORTED")
		errorCode(t, f.request(t, "POST", "/campaign-admin/campaigns/"+id+"/revisions", f.admin, v2.RevisionCommandInput{ClientCommandID: httpID(t), Reason: "reuse withdrawn", ExpectedPublicationVersion: one, Revision: tradeInput()}, 422), "CAMPAIGN_UNSUPPORTED")
	}
	// Cancellation and the accepted-terms snapshot do not depend on the live registry.
	left := f.command(t, "DELETE", "/campaigns/"+id+"/reminder", f.owner, v2.CancelInteractionInput{ClientCommandID: httpID(t), ExpectedStateVersion: one}, 200)
	if left.Original.State == nil || left.Original.State.Status != "removed" {
		t.Fatalf("cancellation after withdrawal: %+v", left)
	}
	var history v2.QueryHistoryResponse
	if err := v2.DecodeContractResponse("QueryHistoryResponse", f.request(t, "GET", "/campaigns/me/history", f.owner, nil, 200), &history); err != nil {
		t.Fatal(err)
	}
	accepted := false
	for _, item := range history.Items {
		if item.CampaignID == id && item.Operation != nil && *item.Operation == "set-reminder" {
			accepted = item.SnapshotState == "ready" && item.AcceptedTerms != nil
		}
	}
	if !accepted {
		t.Fatalf("history keeps the accepted terms after withdrawal: %+v", history.Items)
	}
	caps.set(true, true)
	if item := f.detailState(t, id, capable); item.ContentState != "ready" {
		t.Fatalf("restored registries readmit the content: %+v", item)
	}
	f.command(t, "POST", "/campaigns/"+id+"/reminder", f.other, v2.PositiveInteractionInput{ClientCommandID: httpID(t), ExpectedStateVersion: zero, ExpectedPublicationVersion: one}, 200)
}
