'use client';

import { useQuery } from '@tanstack/react-query';
import {
  getTradingCandles,
  type TradingCandleApi,
  type TradingCandleResolution,
} from '@/lib/api';

interface UseTradingCandlesOptions {
  symbol?: string;
  chainId?: number;
  resolution: TradingCandleResolution;
  limit?: number;
}

export function useTradingCandles({
  symbol,
  chainId,
  resolution,
  limit = 200,
}: UseTradingCandlesOptions) {
  const query = useQuery({
    queryKey: ['trading-candles', chainId, symbol, resolution, limit],
    enabled: Boolean(symbol),
    queryFn: async (): Promise<TradingCandleApi[]> => getTradingCandles(symbol!, resolution, limit, chainId),
    staleTime: 15_000,
    refetchInterval: resolution === '1m' ? 5_000 : 30_000,
  });

  return {
    candles: query.data ?? [],
    ...query,
  };
}
