'use client';

/**
 * @file usePerpPositions Hook
 * @description 聚合所有市场的 long/short 仓位
 *
 * 对每个市场检查 long 和 short 两个方向的仓位
 */

import { useAccount, useChainId } from 'wagmi';
import { getMarkets, type PerpMarketConfig } from '@/lib/web3/markets';
import { usePerpPosition, type PerpPositionData } from './usePerpPosition';

export interface AggregatedPosition extends PerpPositionData {
  isLong: boolean;
  market: PerpMarketConfig;
}

interface UsePerpPositionsParams {
  market?: PerpMarketConfig | null;
}

export function usePerpPositions(params?: UsePerpPositionsParams) {
  const { address } = useAccount();
  const chainId = useChainId();
  const markets = getMarkets(chainId);
  const market = params?.market ?? markets[0];

  // Check long position
  const longResult = usePerpPosition({
    account: address,
    indexToken: market?.indexToken || '0x0000000000000000000000000000000000000000',
    collateralToken: market?.collateralToken || '0x0000000000000000000000000000000000000000',
    isLong: true,
  });

  // Check short position
  const shortResult = usePerpPosition({
    account: address,
    indexToken: market?.indexToken || '0x0000000000000000000000000000000000000000',
    collateralToken: market?.collateralToken || '0x0000000000000000000000000000000000000000',
    isLong: false,
  });

  // Combine into a list, filtering out empty positions
  const positions: AggregatedPosition[] = [];

  if (market && longResult.position?.hasPosition) {
    positions.push({ ...longResult.position, isLong: true, market });
  }
  if (market && shortResult.position?.hasPosition) {
    positions.push({ ...shortResult.position, isLong: false, market });
  }

  return {
    positions,
    isLoading: longResult.isLoading || shortResult.isLoading,
    refetchAll: () => {
      longResult.refetch();
      shortResult.refetch();
    },
  };
}
