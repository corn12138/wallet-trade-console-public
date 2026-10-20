package settings

import "context"

type portfolioVisibilityReader interface {
	FindUserPreferences(context.Context, string) (*UserPreferences, error)
	ListHiddenAssets(context.Context, string, *int) ([]HiddenAsset, error)
}

// Portfolio must not expose assets under default rules when the user's saved
// visibility rules could not be read. The settings HTTP fallback is separate.
func (s *Service) GetPortfolioVisibility(ctx context.Context, owner string, chainID *int) (UserPreferences, []HiddenAsset, error) {
	if s == nil || s.repo == nil {
		return UserPreferences{}, nil, ErrPoolUnavailable
	}
	return readPortfolioVisibility(ctx, s.repo, owner, chainID)
}

func readPortfolioVisibility(ctx context.Context, reader portfolioVisibilityReader, owner string, chainID *int) (UserPreferences, []HiddenAsset, error) {
	addr, ok := normalizeIfValid(owner)
	if !ok {
		return UserPreferences{}, nil, ErrInvalidOwnerAddr
	}
	prefs, err := reader.FindUserPreferences(ctx, addr)
	if err != nil {
		return UserPreferences{}, nil, err
	}
	hidden, err := reader.ListHiddenAssets(ctx, addr, chainID)
	if err != nil {
		return UserPreferences{}, nil, err
	}
	if prefs == nil {
		defaults := defaultUserPreferences(&addr)
		prefs = &defaults
	}
	if hidden == nil {
		hidden = []HiddenAsset{}
	}
	return *prefs, hidden, nil
}
