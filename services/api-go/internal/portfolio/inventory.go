package portfolio

import (
	"context"
	"errors"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/settings"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/staking"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/token"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/trading"
)

func (s *Service) GetAssets(ctx context.Context, address string, chainID *int) (AssetInventory, error) {
	addr, ok := normalizeIfValid(address)
	if !ok {
		return emptyInventory(), nil
	}
	prefs, hiddenKeys, err := s.loadSettings(ctx, addr, chainID)
	if err != nil {
		return AssetInventory{}, err
	}
	sources, err := s.loadAssetSources(ctx, addr, chainID)
	if err != nil {
		return AssetInventory{}, err
	}
	if sources.coverage.Sources.readyCount() == 0 {
		return AssetInventory{}, ErrSourcesUnavailable
	}
	rows := assetRows(addr, chainID, sources.stakes, sources.positions, sources.createdTokens, sources.holdings)
	inventory := applyVisibility(rows, hiddenKeys, prefs.SpamFilterLevel)
	inventory.Coverage = sources.coverage
	inventory.truncated = sources.truncated
	return inventory, nil
}

func (s *Service) loadSettings(ctx context.Context, owner string, chainID *int) (settings.UserPreferences, []string, error) {
	if s.settings == nil {
		return settings.UserPreferences{}, nil, &visibilityReadError{cause: settings.ErrPoolUnavailable}
	}
	prefs, hidden, err := s.settings.GetPortfolioVisibility(ctx, owner, chainID)
	if err != nil {
		return settings.UserPreferences{}, nil, &visibilityReadError{cause: err}
	}
	keys := make([]string, 0, len(hidden))
	for _, h := range hidden {
		keys = append(keys, h.AssetKey)
	}
	return prefs, keys, nil
}

type assetSources struct {
	stakes        []staking.UserStake
	positions     []trading.PerpPositionForAccount
	createdTokens []token.Token
	holdings      []token.HoldingForOwner
	coverage      Coverage
	truncated     bool
}

func (s *Service) loadAssetSources(ctx context.Context, owner string, chainID *int) (assetSources, error) {
	out := assetSources{coverage: newCoverage()}
	var err error
	if s.stakes != nil {
		out.stakes, out.coverage.Sources.Staking, err = readAssetSource(func() ([]staking.UserStake, error) {
			return s.stakes.ListUserStakes(ctx, owner)
		}, staking.ErrPoolUnavailable)
		if err != nil {
			return assetSources{}, err
		}
	}
	if s.positions != nil {
		out.positions, out.coverage.Sources.Perp, err = readAssetSource(func() ([]trading.PerpPositionForAccount, error) {
			return s.positions.ListOpenPositionsForAccount(ctx, owner, chainID)
		}, trading.ErrPoolUnavailable)
		if err != nil {
			return assetSources{}, err
		}
	}
	if s.createdTokens != nil {
		var total int
		out.createdTokens, out.coverage.Sources.CreatedTokens, err = readAssetSource(func() ([]token.Token, error) {
			var rows []token.Token
			var readErr error
			rows, total, readErr = s.createdTokens.ListAll(ctx, token.ListQuery{
				CreatorAddress: owner, ChainID: chainID, Limit: createdTokensLimit, Page: 1,
			})
			return rows, readErr
		}, token.ErrPoolUnavailable)
		if err != nil {
			return assetSources{}, err
		}
		out.truncated = total > len(out.createdTokens)
	}
	if s.holdings != nil {
		out.holdings, out.coverage.Sources.Holdings, err = readAssetSource(func() ([]token.HoldingForOwner, error) {
			return s.holdings.ListHoldingsByOwner(ctx, owner, chainID)
		}, token.ErrPoolUnavailable)
		if err != nil {
			return assetSources{}, err
		}
		// This query has no total count. At its cap we cannot prove that a
		// compatibility total includes every row in the requested scope.
		out.truncated = out.truncated || len(out.holdings) >= holdingsLimit
	}
	return out, nil
}

// Only the known missing-pool sentinel can produce a partial response. Query
// failures must stay failures; even a successful empty read counts as ready.
func readAssetSource[T any](read func() ([]T, error), unavailable error) ([]T, string, error) {
	rows, err := read()
	if errors.Is(err, unavailable) {
		return nil, SourceUnavailable, nil
	}
	if err != nil {
		return nil, SourceUnavailable, err
	}
	return rows, SourceReady, nil
}
