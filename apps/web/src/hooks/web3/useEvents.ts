'use client';

/**
 * @file useEvents
 * @description 链上事件查询 Hooks
 */

import { useQuery } from '@tanstack/react-query';
import { useAccount, useChainId } from 'wagmi';
import {
  getEvents,
  getStats,
  getUserEvents,
  getEventsByTxHash,
  type GetEventsParams,
  type Web3Event,
  type StatsResponse,
  type PaginatedResponse,
} from '@/lib/api/events';

/**
 * 获取事件列表
 */
export function useEvents(params: GetEventsParams = {}) {
  const chainId = useChainId();

  return useQuery<PaginatedResponse<Web3Event>>({
    queryKey: ['events', { ...params, chainId: params.chainId || chainId }],
    queryFn: () => getEvents({ ...params, chainId: params.chainId || chainId }),
    staleTime: 30 * 1000, // 30 秒
  });
}

/**
 * 获取事件统计
 */
export function useEventStats() {
  const chainId = useChainId();

  return useQuery<StatsResponse>({
    queryKey: ['eventStats', chainId],
    queryFn: () => getStats(chainId),
    staleTime: 60 * 1000, // 1 分钟
    refetchInterval: 60 * 1000, // 每分钟自动刷新
  });
}

/**
 * 获取当前用户的事件
 */
export function useMyEvents(limit?: number) {
  const { address } = useAccount();
  const chainId = useChainId();

  return useQuery<Web3Event[]>({
    queryKey: ['myEvents', address, chainId, limit],
    queryFn: () => getUserEvents(address!, chainId, limit),
    enabled: !!address,
    staleTime: 30 * 1000,
  });
}

/**
 * 按交易哈希获取事件
 */
export function useEventsByTxHash(txHash: string) {
  const chainId = useChainId();

  return useQuery<Web3Event[]>({
    queryKey: ['eventsByTx', txHash, chainId],
    queryFn: () => getEventsByTxHash(txHash, chainId),
    enabled: !!txHash,
    staleTime: 5 * 60 * 1000, // 5 分钟（交易不会改变）
  });
}
