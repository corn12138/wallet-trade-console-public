'use client';

/**
 * @file useTokenApproval Hook
 * @description ERC20 代币授权 Hook
 *
 * 使用示例：
 * const { approve, isApproved, isPending, isConfirming } = useTokenApproval(tokenAddress, spenderAddress);
 * await approve(parseUnits('1000', 18));
 */

import { useEffect, useState } from 'react';
import {
  useAccount,
  useChainId,
  useReadContract,
  useWaitForTransactionReceipt,
  useWriteContract,
} from 'wagmi';
import { erc20Abi } from '@/lib/web3/contracts';
import { type Address } from 'viem';
import { usePersistTransactionLifecycle } from './usePersistTransactionLifecycle';

interface TokenApprovalResult {
  allowance: bigint;
  isApproved: (amount: bigint) => boolean;
  approve: (amount?: bigint) => void;
  approveMax: () => void;
  isPending: boolean;
  isConfirming: boolean;
  isSuccess: boolean;
  hash: `0x${string}` | undefined;
  error: Error | null;
}

// 最大授权额度 (uint256 max)
const MAX_UINT256 = BigInt('115792089237316195423570985008687907853269984665640564039457584007913129639935');
type AppChainId = 1 | 31337 | 11155111;

/**
 * ERC20 代币授权
 * @param tokenAddress 代币地址
 * @param spenderAddress 被授权地址（如 Router 合约）
 */
export function useTokenApproval(
  tokenAddress: Address,
  spenderAddress: Address,
  options: { chainId?: AppChainId } = {},
): TokenApprovalResult {
  const { address: userAddress } = useAccount();
  const walletChainId = useChainId();
  const chainId = options.chainId ?? walletChainId;
  const [requestedAllowance, setRequestedAllowance] = useState<string | null>(null);

  // 查询当前授权额度
  const { data: allowance = BigInt(0), refetch: refetchAllowance } = useReadContract({
    chainId,
    address: tokenAddress,
    abi: erc20Abi,
    functionName: 'allowance',
    args: userAddress ? [userAddress, spenderAddress] : undefined,
    query: {
      enabled: !!userAddress,
      refetchInterval: 5000,
    },
  });

  // 写入授权
  const {
    writeContract,
    data: hash,
    isPending,
    error: writeError,
  } = useWriteContract();

  // 等待交易确认
  const {
    isLoading: isConfirming,
    isSuccess,
    data: receipt,
    error: receiptError,
  } = useWaitForTransactionReceipt({
    chainId,
    hash,
  });

  usePersistTransactionLifecycle({
    chainId,
    hash,
    fromAddress: userAddress,
    toAddress: tokenAddress,
    contractAddress: tokenAddress,
    txType: 'approve',
    metadata: {
      spenderAddress,
      requestedAllowance,
    },
    receipt,
    enabled: Boolean(userAddress && hash),
  });

  useEffect(() => {
    if (!isSuccess) {
      return;
    }

    void refetchAllowance();
  }, [isSuccess, refetchAllowance]);

  // 检查是否已授权足够额度
  const isApproved = (amount: bigint): boolean => {
    return (allowance as bigint) >= amount;
  };

  // 授权指定额度
  const approve = (amount: bigint = MAX_UINT256) => {
    if (!userAddress) return;
    setRequestedAllowance(amount.toString());

    writeContract({
      chainId,
      address: tokenAddress,
      abi: erc20Abi,
      functionName: 'approve',
      args: [spenderAddress, amount],
    });
  };

  // 授权最大额度（无限授权）
  const approveMax = () => approve(MAX_UINT256);

  return {
    allowance: allowance as bigint,
    isApproved,
    approve,
    approveMax,
    isPending,
    isConfirming,
    isSuccess,
    hash,
    error: writeError || receiptError,
  };
}
