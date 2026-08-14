import { buildApiUrl } from './base-url';
import type {
  TradingHistoryApi,
  TradingMarketTickerApi,
  TradingOrderbookApi,
} from './trading';

export interface MarketSnapshotApi {
  symbol: string;
  chainId?: number;
  ticker: TradingMarketTickerApi | null;
  orderbook: TradingOrderbookApi | null;
  trades: TradingHistoryApi[];
  updatedAt: string;
}

export async function getMarketSnapshot(
  symbol: string,
  chainId?: number,
  signal?: AbortSignal,
): Promise<MarketSnapshotApi> {
  const params = new URLSearchParams();
  if (chainId !== undefined) {
    params.set('chainId', String(chainId));
  }
  const suffix = params.toString();
  const url = buildApiUrl(
    `/markets/snapshot/${encodeURIComponent(symbol.toUpperCase())}${suffix ? `?${suffix}` : ''}`,
  );

  const response = await fetch(url, { signal });
  if (!response.ok) {
    throw new Error(`Failed to fetch market snapshot (${response.status})`);
  }
  return response.json() as Promise<MarketSnapshotApi>;
}
