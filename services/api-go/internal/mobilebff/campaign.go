package mobilebff

import (
	"context"
	"errors"
	"fmt"
)

var ErrCampaignUnavailable = errors.New("campaign data unavailable")

func (s *Service) activeCampaigns(ctx context.Context) ([]CampaignInfo, error) {
	if s.campaigns == nil {
		return nil, nil
	}
	rows, err := s.campaigns.ActiveCampaigns(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrCampaignUnavailable, err)
	}
	return rows, nil
}

func (s *Service) findCampaign(ctx context.Context, id string) (CampaignInfo, bool, error) {
	if s.campaigns == nil {
		return CampaignInfo{}, false, nil
	}
	row, found, err := s.campaigns.FindCampaign(ctx, id)
	if err != nil {
		return CampaignInfo{}, false, fmt.Errorf("%w: %w", ErrCampaignUnavailable, err)
	}
	return row, found, nil
}
