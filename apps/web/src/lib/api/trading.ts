import { buildApiUrl } from './base-url';
import { fetchApi } from './auth-fetch';

export interface TradingMarketApi {
  symbol: string;
  chainId: number;
  indexToken: `0x${string}`;
  collateralToken: `0x${string}`;
  longOpenInterest: string;
  shortOpenInterest: string;
  fundingRate: string;
  volume24h: string;
}

export interface TradingStatsApi {
  totalVolume: string;
  totalOpenInterest: string;
}

export interface TradingOrderbookApi {
  bids: Array<[string, string]>;
  asks: Array<[string, string]>;
}

export interface TradingMarketTickerApi {
  symbol: string;
  chainId: number;
  price: string;
  bestBid: string | null;
  bestAsk: string | null;
  spread: string | null;
  volume24h: string;
  fundingRate: string;
  longOpenInterest: string;
  shortOpenInterest: string;
  updatedAt: string;
}

export interface TradingPendingOrderApi {
  id: string;
  token: string;
  isLong: boolean;
  orderType: string;
  sizeDelta: string;
  triggerPrice: string | null;
  status: string;
  createdAt: string;
}

export interface TradingHistoryApi {
  id: string;
  token: string;
  isLong: boolean;
  tradeType: string;
  sizeDelta: string;
  price: string;
  fee: string;
  pnl: string | null;
  txHash: string;
  createdAt: string;
}

export type TradingCandleResolution = '1m' | '5m' | '15m' | '1h' | '4h' | '1d';

export interface TradingCandleApi {
  timestamp: number;
  open: string;
  high: string;
  low: string;
  close: string;
  volume: string;
  trades: number;
}

export async function getTradingMarkets(chainId?: number) {
  return getTradingMarketsByChain(chainId);
}

export async function getTradingMarketsByChain(chainId?: number) {
  const response = await fetch(buildApiUrl(buildTradingQueryPath('/trading/markets', { chainId })));

  if (!response.ok) {
    throw new Error('Failed to fetch trading markets');
  }

  return unwrapApiData<TradingMarketApi[]>(await response.json());
}

export async function getTradingStats(chainId?: number) {
  const response = await fetch(buildApiUrl(buildTradingQueryPath('/trading/stats', { chainId })));

  if (!response.ok) {
    throw new Error('Failed to fetch trading stats');
  }

  return unwrapApiData<TradingStatsApi>(await response.json());
}

export async function getTradingOrderbook(symbol: string, chainId?: number) {
  const response = await fetch(buildApiUrl(buildTradingQueryPath('/trading/orderbook', { symbol, chainId })));

  if (!response.ok) {
    throw new Error('Failed to fetch orderbook');
  }

  return unwrapApiData<TradingOrderbookApi>(await response.json());
}

export async function getTradingMarketTrades(symbol: string, chainId?: number, limit = 20) {
  const response = await fetch(
    buildApiUrl(buildTradingQueryPath('/trading/market-trades', { symbol, chainId, limit })),
  );

  if (!response.ok) {
    throw new Error('Failed to fetch market trades');
  }

  return unwrapApiData<TradingHistoryApi[]>(await response.json());
}

export async function getTradingOrders(
  account: `0x${string}`,
  symbol?: string,
  chainId?: number,
) {
  const response = await fetchApi(
    buildTradingQueryPath(`/trading/orders/${account}`, { symbol, chainId }),
  );

  if (!response.ok) {
    throw new Error('Failed to fetch trading orders');
  }

  return unwrapApiData<TradingPendingOrderApi[]>(await response.json());
}

export async function getTradingHistory(
  account: `0x${string}`,
  symbol?: string,
  chainId?: number,
) {
  const response = await fetchApi(
    buildTradingQueryPath(`/trading/history/${account}`, { symbol, chainId }),
  );

  if (!response.ok) {
    throw new Error('Failed to fetch trading history');
  }

  return unwrapApiData<TradingHistoryApi[]>(await response.json());
}

export async function getTradingCandles(
  symbol: string,
  resolution: TradingCandleResolution,
  limit: number,
  chainId?: number,
) {
  const response = await fetch(
    buildApiUrl(buildTradingQueryPath('/trading/candles', { symbol, resolution, limit, chainId })),
  );

  if (!response.ok) {
    throw new Error('Failed to fetch trading candles');
  }

  return unwrapApiData<TradingCandleApi[]>(await response.json());
}

function unwrapApiData<T>(payload: unknown): T {
  if (isApiEnvelope<T>(payload)) {
    return payload.data;
  }

  return payload as T;
}

function isApiEnvelope<T>(payload: unknown): payload is { data: T } {
  return Boolean(
    payload &&
    typeof payload === 'object' &&
    'data' in payload &&
    ('code' in payload || 'message' in payload),
  );
}

function buildTradingQueryPath(
  pathname: string,
  params: Record<string, string | number | undefined>,
) {
  const searchParams = new URLSearchParams();

  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== '') {
      searchParams.set(key, String(value));
    }
  }

  const suffix = searchParams.toString();
  return suffix ? `${pathname}?${suffix}` : pathname;
}
