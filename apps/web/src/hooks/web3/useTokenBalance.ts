'use client';

/**
 * @file useTokenBalance Hook
 * @description 获取代币余额（原生币或 ERC20）
 *
 * 使用示例：
 * const { balance, formatted, symbol, isLoading } = useTokenBalance();
 * const { balance, formatted, isLoading } = useTokenBalance('0x...');
 */

import { useAccount, useBalance, useReadContract } from 'wagmi';
import { formatUnits } from 'viem';
import { erc20Abi } from '@/lib/web3/contracts';

interface TokenBalanceResult {
  balance: bigint | undefined;
  formatted: string;
  symbol: string | undefined;
  decimals: number;
  isLoading: boolean;
  error: Error | null;
}

/**
 * 获取代币余额
 * @param tokenAddress ERC20 代币地址（不传则获取原生币余额）
 */
export function useTokenBalance(tokenAddress?: `0x${string}`): TokenBalanceResult {
  const { address: userAddress } = useAccount();

  // 原生币余额
  const nativeBalance = useBalance({
    address: userAddress,
    query: {
      enabled: !!userAddress && !tokenAddress,
      refetchInterval: 10000, // 10 秒刷新
    },
  });

  // ERC20 代币余额
  const tokenBalance = useReadContract({
    address: tokenAddress,
    abi: erc20Abi,
    functionName: 'balanceOf',
    args: userAddress ? [userAddress] : undefined,
    query: {
      enabled: !!userAddress && !!tokenAddress,
      refetchInterval: 10000,
    },
  });

  // ERC20 代币精度
  const tokenDecimals = useReadContract({
    address: tokenAddress,
    abi: erc20Abi,
    functionName: 'decimals',
    query: {
      enabled: !!tokenAddress,
      staleTime: Infinity, // decimals 不会变，永久缓存
    },
  });

  // ERC20 代币符号
  const tokenSymbol = useReadContract({
    address: tokenAddress,
    abi: erc20Abi,
    functionName: 'symbol',
    query: {
      enabled: !!tokenAddress,
      staleTime: Infinity,
    },
  });

  // 原生币返回
  if (!tokenAddress) {
    const nativeValue = nativeBalance.data?.value;
    return {
      balance: nativeValue,
      formatted: nativeValue ? formatUnits(nativeValue, 18) : '0',
      symbol: nativeBalance.data?.symbol,
      decimals: 18,
      isLoading: nativeBalance.isLoading,
      error: nativeBalance.error,
    };
  }

  // ERC20 代币返回
  const decimals = (tokenDecimals.data as number) ?? 18;
  const balance = tokenBalance.data as bigint | undefined;

  return {
    balance,
    formatted: balance ? formatUnits(balance, decimals) : '0',
    symbol: tokenSymbol.data as string | undefined,
    decimals,
    isLoading: tokenBalance.isLoading || tokenDecimals.isLoading,
    error: tokenBalance.error || tokenDecimals.error,
  };
}
