'use client';

import { useQuery } from '@tanstack/react-query';
import {
  getPriceCandles,
  type PriceSeriesApi,
  type PriceSource,
  type TradingCandleResolution,
} from '@/lib/api';

interface UsePriceCandlesOptions {
  symbol?: string;
  resolution: TradingCandleResolution;
  limit?: number;
  source?: PriceSource;
  /**
   * Gate the request. The chart only reaches for reference prices once the
   * on-chain series is known to be empty, so this stays false while the
   * authoritative source is still loading.
   */
  enabled?: boolean;
}

/**
 * Reference price candles (GET /api/prices/candles).
 *
 * Refresh cadence matches useTradingCandles so both series on the /trade
 * screen move together. The oracle source is polled no faster than the market
 * one would be, but its rows only change on a real aggregator update — a
 * faster poll would just re-read the same round.
 */
export function usePriceCandles({
  symbol,
  resolution,
  limit = 200,
  source = 'market',
  enabled = true,
}: UsePriceCandlesOptions) {
  const query = useQuery({
    queryKey: ['price-candles', source, symbol, resolution, limit],
    enabled: Boolean(symbol) && enabled,
    queryFn: async (): Promise<PriceSeriesApi> =>
      getPriceCandles(symbol!, resolution, limit, source),
    staleTime: 15_000,
    refetchInterval: resolution === '1m' ? 5_000 : 30_000,
  });

  return {
    ...query,
    series: query.data,
    candles: query.data?.candles ?? [],
    // Named `seriesStatus`, not `status`, because react-query already owns
    // `status` ('pending' | 'error' | 'success'). This one is the server's
    // answer about the DATA — it distinguishes "no rows" from "could not
    // read", which the chart renders as two different messages.
    seriesStatus: query.data?.status,
    provider: query.data?.provider,
  };
}
