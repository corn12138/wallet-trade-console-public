package portfolio

import (
	"context"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/settings"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/staking"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/token"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/trading"
)

type inventoryFixture struct {
	stakes                                                        []staking.UserStake
	positions                                                     []trading.PerpPositionForAccount
	created                                                       []token.Token
	holdings                                                      []token.HoldingForOwner
	createdTotal                                                  int
	settingsErr, stakesErr, positionsErr, createdErr, holdingsErr error
	hidden                                                        []settings.HiddenAsset
	spamLevel                                                     string
	calls                                                         []string
	owner                                                         string
	chainID                                                       *int
	createdQuery                                                  token.ListQuery
	settingsWait                                                  func(context.Context)
}

func (f *inventoryFixture) GetPortfolioVisibility(ctx context.Context, owner string, chainID *int) (settings.UserPreferences, []settings.HiddenAsset, error) {
	if f.settingsWait != nil {
		f.settingsWait(ctx)
	}
	f.calls = append(f.calls, "settings")
	f.owner, f.chainID = owner, chainID
	return settings.UserPreferences{SpamFilterLevel: f.spamLevel}, f.hidden, f.settingsErr
}

func (f *inventoryFixture) ListUserStakes(_ context.Context, _ string) ([]staking.UserStake, error) {
	f.calls = append(f.calls, "staking")
	return f.stakes, f.stakesErr
}

func (f *inventoryFixture) ListOpenPositionsForAccount(_ context.Context, _ string, _ *int) ([]trading.PerpPositionForAccount, error) {
	f.calls = append(f.calls, "perp")
	return f.positions, f.positionsErr
}

func (f *inventoryFixture) ListAll(_ context.Context, q token.ListQuery) ([]token.Token, int, error) {
	f.calls = append(f.calls, "createdTokens")
	f.createdQuery = q
	total := f.createdTotal
	if total == 0 {
		total = len(f.created)
	}
	return f.created, total, f.createdErr
}

func (f *inventoryFixture) ListHoldingsByOwner(_ context.Context, _ string, _ *int) ([]token.HoldingForOwner, error) {
	f.calls = append(f.calls, "holdings")
	return f.holdings, f.holdingsErr
}

func fixtureService(f *inventoryFixture) *Service {
	return NewServiceFromDeps(Deps{Settings: f, Stakes: f, Positions: f, CreatedTokens: f, Holdings: f})
}
