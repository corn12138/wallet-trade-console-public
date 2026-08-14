'use client';

/**
 * @file usePerpPrice Hook
 * @description 从 PerpOracle 读取代币价格 (30 decimals)
 */

import { useReadContract, useChainId } from 'wagmi';
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
export function usePerpPrice(tokenAddress: `0x${string}` | undefined): PerpPriceResult {
  const chainId = useChainId();
  const addrs = getPerpAddresses(chainId);

  const { data, isLoading, error } = useReadContract({
    address: addrs.oracle || undefined,
    abi: CONTRACT_ABIS.PerpOracle,
    functionName: 'getPrice',
    args: tokenAddress ? [tokenAddress] : undefined,
    query: {
      enabled: !!tokenAddress && !!addrs.oracle,
      refetchInterval: 5000,
    },
  });

  const price = data as bigint | undefined;
  const formatted = price ? formatUnits(price, 30) : '0';

  return {
    price,
    formatted,
    isLoading,
    error,
  };
}
