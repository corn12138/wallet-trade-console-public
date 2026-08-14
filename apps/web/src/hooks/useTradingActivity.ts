'use client';

import { useCallback } from 'react';
import { useQuery } from '@tanstack/react-query';
import {
  getTradingHistory,
  getTradingOrders,
  type TradingHistoryApi,
  type TradingPendingOrderApi,
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

  const refetchActivity = useCallback(() => {
    refetchOrders();
    refetchHistory();
  }, [refetchOrders, refetchHistory]);

  return {
    orders,
    history,
    isOrdersLoading,
    isHistoryLoading,
    refetchActivity,
  };
}
