export type PortfolioBalanceUnit = 'token' | 'base-unit' | 'usd30';
export type PortfolioValuationReason =
  | 'missing-usd-price'
  | 'position-equity-unavailable'
  | 'holder-valuation-unavailable';

export interface PortfolioValuation {
  state: 'empty' | 'unavailable' | 'partial' | 'complete';
  knownSubtotalUsd: number | null;
  valuedCount: number;
  unpricedCount: number;
}

export interface PortfolioCoverage {
  scope: 'indexed-protocol-assets';
  sources: Record<'staking' | 'perp' | 'createdTokens' | 'holdings', 'ready' | 'unavailable'>;
  limits: { createdTokens: 20; holdings: 40 };
}

export interface PortfolioMetadata {
  /** Null means the response cannot establish the current valuation contract. */
  valuationVersion: 1 | null;
  valuation: PortfolioValuation;
  coverage: PortfolioCoverage | null;
}

export interface AtlasPortfolioAsset {
  id: string;
  assetType: string;
  symbol: string;
  name: string;
  chainId: number;
  rawBalance: string;
  balanceUnit: PortfolioBalanceUnit | null;
  valueUsd: number | null;
  valuationStatus: 'known' | 'unavailable';
  valuationReason: PortfolioValuationReason | null;
  status: string;
  source: string;
  address: string | null;
  walletAddress: string | null;
  updatedAt: string;
}

export interface AtlasPortfolioAssetFilters {
  spamFilterLevel: string;
  totalCount: number;
  visibleCount: number;
  filteredCount: number;
  hiddenConfiguredCount: number;
  hiddenMatchedCount: number;
  spamFilteredCount: number;
}

export interface AtlasPortfolioAssetsResponse extends PortfolioMetadata {
  items: AtlasPortfolioAsset[];
  filterSummary: AtlasPortfolioAssetFilters;
}

export interface AtlasPortfolioSummary extends PortfolioMetadata {
  address: string | null;
  portfolioValueUsd: number | null;
  dayChangePct: number;
  trackedWallets: number;
  recentActivityCount: number;
  pendingTxCount: number;
  securityAlertCount: number;
  dataCompleteness: string;
  assetMix: { earnPositions: number; perpPositions: number };
  assetFilters: AtlasPortfolioAssetFilters;
  topMovers: Array<{ id: string; symbol: string; name: string; address: string | null; priceChange24h: number }>;
  marketSnapshot: { activeMarkets: number; totalVolume: string; totalOpenInterest: string };
}
