package httpx

import (
	"context"
	"errors"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/mobilebff"
)

type bffCampaignAdapter struct{ svc *campaign.Service }

func (a bffCampaignAdapter) ActiveCampaigns(ctx context.Context) ([]mobilebff.CampaignInfo, error) {
	rows, err := a.svc.Active(ctx, time.Now())
	if err != nil {
		return nil, err
	}
	out := make([]mobilebff.CampaignInfo, 0, len(rows))
	for _, c := range rows {
		out = append(out, toBffCampaign(c))
	}
	return out, nil
}

func (a bffCampaignAdapter) FindCampaign(ctx context.Context, id string) (mobilebff.CampaignInfo, bool, error) {
	c, err := a.svc.FindByID(ctx, id)
	if errors.Is(err, campaign.ErrNotFound) {
		return mobilebff.CampaignInfo{}, false, nil
	}
	if err != nil {
		return mobilebff.CampaignInfo{}, false, err
	}
	return toBffCampaign(c), true, nil
}

func toBffCampaign(c campaign.Campaign) mobilebff.CampaignInfo {
	info := mobilebff.CampaignInfo{ID: c.ID, Title: c.Title}
	if c.Description != nil {
		info.Description = *c.Description
	}
	if c.Banner != nil {
		info.Banner = *c.Banner
	}
	return info
}
