'use client';

/**
 * @file usePerpPosition Hook
 * @description 从 PerpMarket 读取单个仓位数据
 *
 * 流程: getPositionKey(account, indexToken, collateralToken, isLong) → positions[key]
 */

import { useReadContract, useChainId } from 'wagmi';
import { getPerpAddresses, CONTRACT_ABIS } from '@/lib/web3/contracts';

export interface PerpPositionData {
  size: bigint;           // 仓位大小 (USD, 30 decimals)
  collateral: bigint;     // 抵押品 (USD, 30 decimals)
  averagePrice: bigint;   // 平均入场价 (30 decimals)
  entryFundingRate: bigint;
  reserveAmount: bigint;
  realisedPnl: bigint;
  lastUpdatedAt: bigint;
  hasPosition: boolean;   // size > 0
}

interface UsePerpPositionParams {
  account: `0x${string}` | undefined;
  indexToken: `0x${string}`;
  collateralToken: `0x${string}`;
  isLong: boolean;
}

export function usePerpPosition(params: UsePerpPositionParams) {
  const chainId = useChainId();
  const addrs = getPerpAddresses(chainId);
  const enabled = !!params.account && !!addrs.market;

  // Step 1: Compute position key on-chain
  const { data: positionKey } = useReadContract({
    address: addrs.market || undefined,
    abi: CONTRACT_ABIS.PerpMarket,
    functionName: 'getPositionKey',
    args: params.account
      ? [params.account, params.indexToken, params.collateralToken, params.isLong]
      : undefined,
    query: { enabled },
  });

  // Step 2: Read position by key
  const { data: positionData, isLoading, refetch } = useReadContract({
    address: addrs.market || undefined,
    abi: CONTRACT_ABIS.PerpMarket,
    functionName: 'positions',
    args: positionKey ? [positionKey as `0x${string}`] : undefined,
    query: {
      enabled: !!positionKey,
      refetchInterval: 10000,
    },
  });

  // Step 3: Parse tuple into typed object
  const position: PerpPositionData | null = positionData
    ? {
        size: (positionData as any)[0] as bigint,
        collateral: (positionData as any)[1] as bigint,
        averagePrice: (positionData as any)[2] as bigint,
        entryFundingRate: (positionData as any)[3] as bigint,
        reserveAmount: (positionData as any)[4] as bigint,
        realisedPnl: (positionData as any)[5] as bigint,
        lastUpdatedAt: (positionData as any)[6] as bigint,
        hasPosition: ((positionData as any)[0] as bigint) > 0n,
      }
    : null;

  return { position, isLoading, refetch };
}
