import { vi } from 'vitest';
import type { AtlasPortfolioAsset, AtlasPortfolioAssetsResponse, AtlasPortfolioSummary } from '@/lib/api/portfolio';
import type { PortfolioQueryView, usePortfolioData } from '@/hooks/usePortfolioData';

export const PORTFOLIO_OWNER = '0x1111111111111111111111111111111111111111';
export const OTHER_PORTFOLIO_OWNER = '0x2222222222222222222222222222222222222222';

export function portfolioAsset(overrides: Partial<AtlasPortfolioAsset> = {}): AtlasPortfolioAsset {
  return {
    id: 'stake:one', assetType: 'earn', symbol: 'Pool name', name: 'STK pool', chainId: 11155111,
    rawBalance: '100', balanceUnit: 'token', valueUsd: null, valuationStatus: 'unavailable',
    valuationReason: 'missing-usd-price', status: 'active', source: 'Atlas earn',
    address: '0x3333333333333333333333333333333333333333', walletAddress: PORTFOLIO_OWNER,
    updatedAt: '2026-10-03T00:00:00Z', ...overrides,
  };
}

export function portfolioInventory(items: AtlasPortfolioAsset[] = [portfolioAsset()]): AtlasPortfolioAssetsResponse {
  const known = items.filter((row) => row.valuationStatus === 'known');
  return {
    valuationVersion: 1,
    valuation: { state: !items.length ? 'empty' : !known.length ? 'unavailable' : known.length === items.length ? 'complete' : 'partial',
      knownSubtotalUsd: known.length ? known.reduce((sum, row) => sum + row.valueUsd!, 0) : null,
      valuedCount: known.length, unpricedCount: items.length - known.length },
    coverage: { scope: 'indexed-protocol-assets', sources: { staking: 'ready', perp: 'ready', createdTokens: 'ready', holdings: 'ready' },
      limits: { createdTokens: 20, holdings: 40 } },
    items,
    filterSummary: { spamFilterLevel: 'standard', totalCount: items.length, visibleCount: items.length,
      filteredCount: 0, hiddenConfiguredCount: 0, hiddenMatchedCount: 0, spamFilteredCount: 0 },
  };
}

export function portfolioSummary(inventory: AtlasPortfolioAssetsResponse): AtlasPortfolioSummary {
  return {
    valuationVersion: inventory.valuationVersion, valuation: inventory.valuation, coverage: inventory.coverage,
    address: PORTFOLIO_OWNER, portfolioValueUsd: null, dayChangePct: 0, trackedWallets: 1, recentActivityCount: 0,
    pendingTxCount: 0, securityAlertCount: 0, dataCompleteness: 'partial-live',
    assetMix: { earnPositions: 1, perpPositions: 0 }, assetFilters: inventory.filterSummary,
    topMovers: [], marketSnapshot: { activeMarkets: 0, totalVolume: '0', totalOpenInterest: '0' },
  };
}

export function portfolioQuery<T>(data?: T, overrides: Partial<PortfolioQueryView<T>> = {}): PortfolioQueryView<T> {
  return { data, isLoading: false, isFetching: false, isError: false, error: null, isStale: false,
    dataUpdatedAt: data ? Date.parse('2026-10-03T00:00:00Z') : 0, refetch: vi.fn().mockResolvedValue(undefined), ...overrides };
}

export function portfolioState(inventory = portfolioInventory()): ReturnType<typeof usePortfolioData> {
  return { isAuthorized: true, accessState: 'ready', scope: 'chain', assetsQuery: portfolioQuery(inventory),
    summaryQuery: portfolioQuery(portfolioSummary(inventory)), refetchAll: vi.fn().mockResolvedValue([]) };
}
