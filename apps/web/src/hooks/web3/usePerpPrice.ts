'use client';

/**
 * @file usePerpPrice Hook
 * @description 从 PerpOracle 读取代币价格 (30 decimals)
 */

import { useReadContract } from 'wagmi';
import type { TradingChainId } from '@/lib/web3/trading-chain';
import { formatUnits } from 'viem';
import { getPerpAddresses, CONTRACT_ABIS } from '@/lib/web3/contracts';

interface PerpPriceResult {
  price: bigint | undefined;
  formatted: string;
  isLoading: boolean;
  error: Error | null;
}

/**
 * 读取 PerpOracle 中代币的当前价格
 * @param tokenAddress 代币地址 (indexToken 或 collateralToken)
 */
// Public market reads use the selected market chain even before a wallet connects.
export function usePerpPrice(tokenAddress: `0x${string}` | undefined, chainId: TradingChainId): PerpPriceResult {
  const addrs = getPerpAddresses(chainId);

  const { data, isLoading, error } = useReadContract({
    chainId,
    address: addrs.oracle || undefined,
    abi: CONTRACT_ABIS.PerpOracle,
    functionName: 'getPrice',
    args: tokenAddress ? [tokenAddress] : undefined,
    query: {
      enabled: !!tokenAddress && !!addrs.oracle,
      refetchInterval: 5000,
    },
  });

  // Failed or zero oracle reads are unavailable, including stale data after an error.
  const price = !error && typeof data === 'bigint' && data > 0n ? data : undefined;
  const formatted = price !== undefined ? formatUnits(price, 30) : '—';

  return {
    price,
    formatted,
    isLoading,
    error,
  };
}
