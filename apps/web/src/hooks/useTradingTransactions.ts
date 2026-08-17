'use client';

import { useCallback, useEffect, useState } from 'react';
import { useWaitForTransactionReceipt, useWriteContract } from 'wagmi';
import { parseUnits } from 'viem';
import { CONTRACT_ABIS } from '@/lib/web3/contracts';
import type { TradingChainId } from '@/lib/web3/trading-chain';
import { useTokenApproval } from './web3/useTokenApproval';
import { usePersistTransactionLifecycle } from './web3/usePersistTransactionLifecycle';
import { buildCloseExecutionParams, buildOpenExecutionParams } from './trading-execution.utils';
import type {
  CloseTradingPositionParams,
  OpenTradingPositionParams,
  TradingMarketView,
} from './trading.types';

interface UseTradingTransactionsParams {
  address?: `0x${string}`;
  addrs: {
    positionManager?: `0x${string}`;
    usdc?: `0x${string}`;
  };
  chainId: TradingChainId;
  currentPrice?: bigint;
  selectedMarket: TradingMarketView | null;
  refetchAll: () => void;
  refetchActivity: () => void;
}

export function useTradingTransactions({
  address,
  addrs,
  chainId,
  currentPrice,
  selectedMarket,
  refetchAll,
  refetchActivity,
}: UseTradingTransactionsParams) {
  const emptyAddress = '0x0000000000000000000000000000000000000000';
  const [openTrackingMetadata, setOpenTrackingMetadata] = useState<Record<string, unknown> | null>(null);
  const [closeTrackingMetadata, setCloseTrackingMetadata] = useState<Record<string, unknown> | null>(null);
  const {
    isApproved,
    approve: approveUsdc,
    isPending: isApproving,
    isSuccess: isApproveSuccess,
  } = useTokenApproval(
    addrs.usdc || emptyAddress,
    addrs.positionManager || emptyAddress,
    { chainId },
  );

  const {
    writeContract: writeOpen,
    data: openHash,
    isPending: isOpenPending,
    error: openError,
    reset: resetOpen,
  } = useWriteContract();
  const {
    isLoading: isOpenConfirming,
    isSuccess: isOpenSuccess,
    data: openReceipt,
  } = useWaitForTransactionReceipt({ chainId, hash: openHash });

  const {
    writeContract: writeClose,
    data: closeHash,
    isPending: isClosePending,
    error: closeError,
    reset: resetClose,
  } = useWriteContract();
  const {
    isLoading: isCloseConfirming,
    isSuccess: isCloseSuccess,
    data: closeReceipt,
  } = useWaitForTransactionReceipt({ chainId, hash: closeHash });

  usePersistTransactionLifecycle({
    chainId,
    hash: openHash,
    fromAddress: address,
    toAddress: addrs.positionManager,
    contractAddress: addrs.positionManager,
    value: '0',
    txType: 'open-position',
    metadata: openTrackingMetadata,
    receipt: openReceipt,
    enabled: Boolean(address && addrs.positionManager && openHash),
  });

  usePersistTransactionLifecycle({
    chainId,
    hash: closeHash,
    fromAddress: address,
    toAddress: addrs.positionManager,
    contractAddress: addrs.positionManager,
    value: '0',
    txType: 'close-position',
    metadata: closeTrackingMetadata,
    receipt: closeReceipt,
    enabled: Boolean(address && addrs.positionManager && closeHash),
  });

  // After a position open/close confirms, the indexer needs a beat to pick
  // up the on-chain event and the API needs another to materialize it for
  // the listing endpoints. The previous implementation fired a single
  // refetch 2s after success — short enough to miss a slow indexer or a
  // congested chain, with no retry. Instead we walk a short ladder so the
  // UI converges as soon as the data lands, regardless of which side is
  // slow today. Keying the effect off the receipt hash (rather than the
  // boolean `isSuccess`) ensures back-to-back trades each get a fresh
  // ladder; the cleanup cancels still-pending timers when the user
  // navigates away or kicks off the next trade.
  const openConfirmedHash = openReceipt?.transactionHash;
  const closeConfirmedHash = closeReceipt?.transactionHash;
  useEffect(() => {
    if (!openConfirmedHash && !closeConfirmedHash) return;

    const REFETCH_LADDER_MS = [1_000, 2_500, 5_000, 10_000];
    let cancelled = false;
    const timers: ReturnType<typeof setTimeout>[] = [];

    for (const delay of REFETCH_LADDER_MS) {
      const timer = setTimeout(() => {
        if (cancelled) return;
        refetchAll();
        refetchActivity();
      }, delay);
      timers.push(timer);
    }

    return () => {
      cancelled = true;
      for (const t of timers) clearTimeout(t);
    };
  }, [openConfirmedHash, closeConfirmedHash, refetchAll, refetchActivity]);

  const openPosition = useCallback((params: OpenTradingPositionParams) => {
    if (!address || !selectedMarket || !addrs.positionManager) {
      return;
    }

    const collateralWei = parseUnits(params.collateralAmount, 18);
    const sizeUsd = parseUnits(
      (parseFloat(params.collateralAmount) * params.leverage).toString(),
      30,
    );
    const executionParams = buildOpenExecutionParams({
      currentPrice,
      explicitAcceptablePrice: params.acceptablePrice,
      slippagePercent: params.slippagePercent ?? 0.5,
      deadlineMinutes: params.deadlineMinutes ?? 20,
      isLong: params.isLong,
    });

    if (!isApproved(collateralWei)) {
      approveUsdc(collateralWei);
      return;
    }

    setOpenTrackingMetadata({
      market: selectedMarket.symbol,
      indexToken: selectedMarket.indexToken,
      collateralToken: selectedMarket.collateralToken,
      collateralAmount: params.collateralAmount,
      leverage: params.leverage,
      isLong: params.isLong,
    });

    writeOpen({
      chainId,
      address: addrs.positionManager,
      abi: CONTRACT_ABIS.PositionManager,
      functionName: 'openPosition',
      args: [
        selectedMarket.indexToken,
        selectedMarket.collateralToken,
        collateralWei,
        sizeUsd,
        params.isLong,
        executionParams.acceptablePrice,
        executionParams.deadline,
      ],
    });
  }, [address, selectedMarket, addrs, chainId, currentPrice, isApproved, approveUsdc, writeOpen]);

  const closePosition = useCallback((params: CloseTradingPositionParams) => {
    if (!address || !addrs.positionManager) {
      return;
    }

    const executionParams = buildCloseExecutionParams({
      currentPrice,
      slippagePercent: params.slippagePercent ?? 0.5,
      deadlineMinutes: params.deadlineMinutes ?? 20,
      isLong: params.position.isLong,
    });

    setCloseTrackingMetadata({
      market: params.position.market.symbol,
      indexToken: params.position.market.indexToken,
      collateralToken: params.position.market.collateralToken,
      size: params.position.size.toString(),
      isLong: params.position.isLong,
    });

    writeClose({
      chainId,
      address: addrs.positionManager,
      abi: CONTRACT_ABIS.PositionManager,
      functionName: 'closePosition',
      args: [
        params.position.market.indexToken,
        params.position.market.collateralToken,
        0n,
        params.position.size,
        params.position.isLong,
        executionParams.acceptablePrice,
        executionParams.deadline,
      ],
    });
  }, [address, addrs, chainId, currentPrice, writeClose]);

  return {
    openPosition,
    closePosition,
    // Exposed so the page can tell WHICH transaction the next submit sends.
    // `openPosition` silently diverts to an approve when the allowance is
    // short, and a pre-sign review that could not see that would describe the
    // position open while the wallet was handed an ERC20 approval.
    isApproved,
    isApproving,
    isApproveSuccess,
    isOpenPending,
    isOpenConfirming,
    isOpenSuccess,
    openError,
    resetOpen,
    isClosePending,
    isCloseConfirming,
    isCloseSuccess,
    closeError,
    resetClose,
  };
}
