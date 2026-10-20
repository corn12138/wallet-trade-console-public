import { fetchApi } from './auth-fetch';
import type {
  AtlasPortfolioAsset, AtlasPortfolioAssetFilters, AtlasPortfolioAssetsResponse, AtlasPortfolioSummary,
  PortfolioBalanceUnit, PortfolioCoverage, PortfolioMetadata, PortfolioValuation, PortfolioValuationReason,
} from './portfolio.types';

export type * from './portfolio.types';

export class PortfolioApiError extends Error {
  constructor(message: string, readonly status: number, readonly code: string | null = null) {
    super(message);
    this.name = 'PortfolioApiError';
  }
}

type RecordValue = Record<string, unknown>;
const object = (value: unknown): RecordValue => value && typeof value === 'object' && !Array.isArray(value) ? value as RecordValue : {};
const string = (value: unknown): string => typeof value === 'string' ? value : '';
const nullableString = (value: unknown): string | null => typeof value === 'string' ? value : null;
const count = (value: unknown): number => typeof value === 'number' && Number.isSafeInteger(value) && value >= 0 ? value : 0;
const isCount = (value: unknown): value is number => typeof value === 'number' && Number.isSafeInteger(value) && value >= 0;
const isUsd = (value: unknown): value is number => typeof value === 'number' && Number.isFinite(value) && value >= 0;
const finite = (value: unknown): number => typeof value === 'number' && Number.isFinite(value) ? value : 0;
const units: readonly string[] = ['token', 'base-unit', 'usd30'];
const reasons: readonly string[] = ['missing-usd-price', 'position-equity-unavailable', 'holder-valuation-unavailable'];
const sourceKeys = ['staking', 'perp', 'createdTokens', 'holdings'] as const;

function filters(value: unknown): AtlasPortfolioAssetFilters {
  const input = object(value);
  return {
    spamFilterLevel: string(input.spamFilterLevel), totalCount: count(input.totalCount),
    visibleCount: count(input.visibleCount), filteredCount: count(input.filteredCount),
    hiddenConfiguredCount: count(input.hiddenConfiguredCount), hiddenMatchedCount: count(input.hiddenMatchedCount),
    spamFilteredCount: count(input.spamFilteredCount),
  };
}

function readCoverage(value: unknown): PortfolioCoverage | null {
  const input = object(value), sources = object(input.sources), limits = object(input.limits);
  if (input.scope !== 'indexed-protocol-assets' || limits.createdTokens !== 20 || limits.holdings !== 40
    || sourceKeys.some((key) => sources[key] !== 'ready' && sources[key] !== 'unavailable')) return null;
  return { scope: 'indexed-protocol-assets', sources: sources as PortfolioCoverage['sources'], limits: { createdTokens: 20, holdings: 40 } };
}

function readValuation(value: unknown): PortfolioValuation | null {
  const input = object(value);
  if (!isCount(input.valuedCount) || !isCount(input.unpricedCount)
    || !Number.isSafeInteger(input.valuedCount + input.unpricedCount)) return null;
  const expected = input.valuedCount === 0
    ? input.unpricedCount === 0 ? 'empty' : 'unavailable'
    : input.unpricedCount === 0 ? 'complete' : 'partial';
  if (input.state !== expected
    || (input.valuedCount > 0 ? !isUsd(input.knownSubtotalUsd) : input.knownSubtotalUsd !== null)) return null;
  return { state: expected, knownSubtotalUsd: input.knownSubtotalUsd as number | null,
    valuedCount: input.valuedCount, unpricedCount: input.unpricedCount };
}

function metadata(input: RecordValue, unpricedCount: number): PortfolioMetadata {
  const coverage = readCoverage(input.coverage), valuation = readValuation(input.valuation);
  if (input.valuationVersion === 1 && coverage && valuation) return { valuationVersion: 1, coverage, valuation };
  // Legacy numbers combine incompatible units. A missing/malformed contract
  // must never acquire credibility merely because one USD field is positive.
  return { valuationVersion: null, coverage: null,
    valuation: { state: 'unavailable', knownSubtotalUsd: null, valuedCount: 0, unpricedCount } };
}

function asset(value: unknown, trusted: boolean): AtlasPortfolioAsset {
  const input = object(value);
  const known = trusted && input.valuationStatus === 'known' && isUsd(input.valueUsd);
  return {
    id: string(input.id), assetType: string(input.assetType), symbol: string(input.symbol), name: string(input.name),
    chainId: count(input.chainId), rawBalance: string(input.rawBalance),
    balanceUnit: trusted && units.includes(string(input.balanceUnit)) ? input.balanceUnit as PortfolioBalanceUnit : null,
    valueUsd: known ? input.valueUsd as number : null,
    valuationStatus: known ? 'known' : 'unavailable',
    valuationReason: !known && trusted && reasons.includes(string(input.valuationReason)) ? input.valuationReason as PortfolioValuationReason : null,
    status: string(input.status), source: string(input.source), address: nullableString(input.address),
    walletAddress: nullableString(input.walletAddress), updatedAt: string(input.updatedAt),
  };
}

export function normalizePortfolioAssets(value: unknown): AtlasPortfolioAssetsResponse {
  const input = object(value);
  if (!Array.isArray(input.items)) throw new PortfolioApiError('Invalid portfolio response', 200, 'PORTFOLIO_RESPONSE_INVALID');
  let meta = metadata(input, input.items.length);
  let items = input.items.map((item) => asset(item, meta.valuationVersion === 1));
  const valuedCount = items.filter((item) => item.valuationStatus === 'known').length;
  const subtotal = items.reduce((sum, item) => sum + (item.valueUsd ?? 0), 0);
  // This only validates display-number consistency; the API remains the source
  // of the subtotal. Allow summation-order roundoff, never a different valuation.
  const tolerance = Number.EPSILON * Math.max(1, subtotal) * Math.max(1, valuedCount);
  const subtotalMatches = valuedCount === 0 ? meta.valuation.knownSubtotalUsd === null
    : meta.valuation.knownSubtotalUsd !== null && Number.isFinite(subtotal)
      && Math.abs(subtotal - meta.valuation.knownSubtotalUsd) <= tolerance;
  if (meta.valuationVersion === 1 && (meta.valuation.valuedCount !== valuedCount
    || meta.valuation.unpricedCount !== items.length - valuedCount
    || !subtotalMatches
    || items.some((item) => item.balanceUnit === null))) {
    meta = metadata({}, items.length);
    items = input.items.map((item) => asset(item, false));
  }
  return { ...meta, items, filterSummary: filters(input.filterSummary) };
}

export function normalizePortfolioSummary(value: unknown): AtlasPortfolioSummary {
  const input = object(value), assetFilters = filters(input.assetFilters);
  let meta = metadata(input, assetFilters.visibleCount);
  if (meta.valuationVersion === 1 && (!isCount(object(input.assetFilters).visibleCount)
    || meta.valuation.valuedCount + meta.valuation.unpricedCount !== assetFilters.visibleCount)) {
    meta = metadata({}, assetFilters.visibleCount);
  }
  const mix = object(input.assetMix), snapshot = object(input.marketSnapshot);
  return {
    ...meta, address: nullableString(input.address),
    portfolioValueUsd: meta.valuationVersion === 1 && meta.valuation.state === 'complete'
      && sourceKeys.every((key) => meta.coverage?.sources[key] === 'ready') && isUsd(input.portfolioValueUsd)
      && input.portfolioValueUsd === meta.valuation.knownSubtotalUsd
      ? input.portfolioValueUsd : null,
    dayChangePct: finite(input.dayChangePct), trackedWallets: count(input.trackedWallets),
    recentActivityCount: count(input.recentActivityCount), pendingTxCount: count(input.pendingTxCount),
    securityAlertCount: count(input.securityAlertCount), dataCompleteness: string(input.dataCompleteness), assetFilters,
    assetMix: { earnPositions: count(mix.earnPositions), perpPositions: count(mix.perpPositions) },
    topMovers: Array.isArray(input.topMovers) ? input.topMovers.map((value) => {
      const mover = object(value);
      return { id: string(mover.id), symbol: string(mover.symbol), name: string(mover.name),
        address: nullableString(mover.address), priceChange24h: finite(mover.priceChange24h) };
    }) : [],
    marketSnapshot: { activeMarkets: count(snapshot.activeMarkets), totalVolume: string(snapshot.totalVolume),
      totalOpenInterest: string(snapshot.totalOpenInterest) },
  };
}

async function requestPortfolio(path: string, address?: string, chainId?: number, signal?: AbortSignal): Promise<unknown> {
  const query = new URLSearchParams();
  if (address) query.set('address', address);
  if (chainId !== undefined) query.set('chainId', String(chainId));
  const response = await fetchApi(`${path}${query.size ? `?${query}` : ''}`, { signal });
  const payload: unknown = await response.json().catch(() => null);
  if (!response.ok) {
    const error = object(payload);
    throw new PortfolioApiError(string(error.message) || `HTTP ${response.status}`, response.status, nullableString(error.code));
  }
  if (!payload || typeof payload !== 'object') throw new PortfolioApiError('Invalid portfolio response', response.status, 'PORTFOLIO_RESPONSE_INVALID');
  return object(payload).data ?? payload;
}

export async function getPortfolioAssets(address?: string, chainId?: number, signal?: AbortSignal): Promise<AtlasPortfolioAssetsResponse> {
  return normalizePortfolioAssets(await requestPortfolio('/portfolio/assets', address, chainId, signal));
}

export async function getPortfolioSummary(address?: string, chainId?: number, signal?: AbortSignal): Promise<AtlasPortfolioSummary> {
  return normalizePortfolioSummary(await requestPortfolio('/portfolio/summary', address, chainId, signal));
}
