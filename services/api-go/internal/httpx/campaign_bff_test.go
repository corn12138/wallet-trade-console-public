package httpx

import (
	"context"
	"errors"
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign"
)

func TestBFFLegacyCampaignAdapterPropagatesUnavailable(t *testing.T) {
	for _, svc := range []*campaign.Service{nil, campaign.NewService(nil), campaign.NewService(campaign.NewRepository(nil))} {
		adapter := bffCampaignAdapter{svc: svc}
		if rows, err := adapter.ActiveCampaigns(context.Background()); !errors.Is(err, campaign.ErrPoolUnavailable) || rows != nil {
			t.Fatalf("list outage must remain failure: rows=%v err=%v", rows, err)
		}
		if _, found, err := adapter.FindCampaign(context.Background(), "campaign-id"); found || !errors.Is(err, campaign.ErrPoolUnavailable) {
			t.Fatalf("detail outage must not become not found: found=%v err=%v", found, err)
		}
	}
}
