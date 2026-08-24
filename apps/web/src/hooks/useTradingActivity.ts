'use client';

import { useCallback } from 'react';
import { useQuery } from '@tanstack/react-query';
import {
  getTradingHistory,
  getTradingOrders,
  getTradingPositions,
  type TradingHistoryApi,
  type TradingPendingOrderApi,
  type TradingPositionApi,
} from '@/lib/api';

interface UseTradingActivityOptions {
  account?: `0x${string}`;
  symbol?: string;
  chainId?: number;
  enabled?: boolean;
}

export function useTradingActivity({
  account,
  symbol,
  chainId,
  enabled = true,
}: UseTradingActivityOptions) {
  const { data: orders = [], isLoading: isOrdersLoading, refetch: refetchOrders } = useQuery({
    queryKey: ['trading-orders', account, symbol, chainId],
    enabled: Boolean(account) && enabled,
    queryFn: async (): Promise<TradingPendingOrderApi[]> => getTradingOrders(account!, symbol, chainId),
    staleTime: 10_000,
    refetchInterval: 10_000,
  });

  const { data: history = [], isLoading: isHistoryLoading, refetch: refetchHistory } = useQuery({
    queryKey: ['trading-history', account, symbol, chainId],
    enabled: Boolean(account) && enabled,
    queryFn: async (): Promise<TradingHistoryApi[]> => getTradingHistory(account!, symbol, chainId),
    staleTime: 10_000,
    refetchInterval: 10_000,
  });

  // The indexer's cross-market open-positions projection. Deliberately not
  // keyed on `symbol`: this is the portfolio view, so it must keep covering
  // every market while the terminal has one selected.
  const {
    data: indexedPositions = [],
    isLoading: isIndexedPositionsLoading,
    refetch: refetchIndexedPositions,
  } = useQuery({
    queryKey: ['trading-positions', account, chainId],
    enabled: Boolean(account) && enabled,
    queryFn: async (): Promise<TradingPositionApi[]> => getTradingPositions(account!, chainId),
    staleTime: 10_000,
    refetchInterval: 10_000,
  });

  const refetchActivity = useCallback(() => {
    refetchOrders();
    refetchHistory();
    refetchIndexedPositions();
  }, [refetchOrders, refetchHistory, refetchIndexedPositions]);

  return {
    orders,
    history,
    indexedPositions,
    isOrdersLoading,
    isHistoryLoading,
    isIndexedPositionsLoading,
    refetchActivity,
  };
}
