/**
 * @file Reference price API client (GET /api/prices).
 *
 * These are REAL prices from outside this product's own order flow:
 *
 *   market — dense OHLCV from a public spot venue, ingested and stored by
 *            services/api-go/cmd/pricefeed. Carries real traded volume.
 *   oracle — Chainlink aggregator rounds read on-chain. Trust-minimized, but
 *            sparse (heartbeat/deviation only) and carries NO volume.
 *
 * They are deliberately a separate endpoint from /api/trading/candles, which
 * projects this product's own perp trades. Callers must keep them visually
 * distinct — a reference price is not a trade that happened here.
 */

import { buildApiUrl } from './base-url';

export type PriceSource = 'market' | 'oracle';

/** Matches internal/pricefeed.Service status vocabulary. */
export type PriceSeriesStatus = 'ok' | 'empty' | 'unavailable';

export interface PriceCandleApi {
  timestamp: number;
  open: string;
  high: string;
  low: string;
  close: string;
  volume: string;
  quoteVolume: string;
  /** false = bucket still forming; its close is the latest price, not a final one. */
  closed: boolean;
}

export interface PriceSeriesApi {
  symbol: string;
  resolution: string;
  source: PriceSource;
  /** Venue id for the market source (e.g. "gateio"); absent for oracle. */
  provider?: string;
  status: PriceSeriesStatus;
  candles: PriceCandleApi[];
  generatedAt: string;
}

export interface PriceSpotApi {
  symbol: string;
  source: PriceSource;
  price: string;
  observedAt: string;
  status: PriceSeriesStatus;
  /** Oracle-only provenance. */
  chainId?: number;
  feedAddress?: string;
  roundId?: string;
}

function buildPricesPath(pathname: string, params: Record<string, string | number | undefined>) {
  const searchParams = new URLSearchParams();

  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== '') {
      searchParams.set(key, String(value));
    }
  }

  const suffix = searchParams.toString();
  return suffix ? `${pathname}?${suffix}` : pathname;
}

/**
 * Fetch a stored reference price series.
 *
 * The endpoint answers 200 with `status: "empty"` when it has no rows and
 * `"unavailable"` when it could not read them. Both are returned to the caller
 * as-is rather than collapsed into an exception, because the chart renders a
 * different message for "nothing here" than for "we could not look".
 */
export async function getPriceCandles(
  symbol: string,
  resolution: string,
  limit: number,
  source: PriceSource = 'market',
): Promise<PriceSeriesApi> {
  const response = await fetch(
    buildApiUrl(buildPricesPath('/prices/candles', { symbol, resolution, limit, source })),
  );

  if (!response.ok) {
    throw new Error(`Failed to fetch ${source} price candles (HTTP ${response.status})`);
  }

  return (await response.json()) as PriceSeriesApi;
}

/** Fetch the latest known price per symbol from one source. */
export async function getPriceSpot(
  symbols: string[],
  source: PriceSource = 'market',
): Promise<PriceSpotApi[]> {
  const response = await fetch(
    buildApiUrl(buildPricesPath('/prices/spot', { symbols: symbols.join(','), source })),
  );

  if (!response.ok) {
    throw new Error(`Failed to fetch ${source} spot prices (HTTP ${response.status})`);
  }

  return (await response.json()) as PriceSpotApi[];
}
