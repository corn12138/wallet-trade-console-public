'use client';

import { useRef } from 'react';
import { useReadContract } from 'wagmi';
import { erc20Abi } from 'viem';
import { useQueryClient } from '@tanstack/react-query';
import type { TradingChainId } from '@/lib/web3/trading-chain';
import { useTxIntent } from './web3/txIntent/useTxIntent';
import type { TxErrorCode } from './web3/txIntent/types';
import type { AggregatedPosition } from './web3/usePerpPositions';
import { buildTradingSteps, type TradeIntentRequest } from './trading-intent';
import { canonicalize } from './web3/txIntent/intent';
import type { OpenTradingPositionParams, TradingMarketView } from './trading.types';

export interface UseTradingTransactionsParams {
  address?: `0x${string}`;
  addrs: { positionManager?: `0x${string}`; usdc?: `0x${string}` };
  chainId: TradingChainId;
  currentPrice?: bigint;
  selectedMarket: TradingMarketView | null;
  positions: AggregatedPosition[];
  openParams: OpenTradingPositionParams;
  viewerKey?: string | null;
  refetchAll: () => void;
  refetchActivity: () => void;
}

/** A single operation owns the wallet prompt; market reads remain in useTrading. */
export function useTradingTransactions(options: UseTradingTransactionsParams) {
  const { address, addrs, chainId, currentPrice, selectedMarket, positions, openParams, viewerKey } = options;
  const queryClient = useQueryClient();
  const selection = useRef<TradeIntentRequest | null>(null);
  const referencePrice = useRef<bigint | undefined>(undefined);
  const allowanceRead = useReadContract({
    chainId,
    address: selectedMarket?.collateralToken,
    abi: erc20Abi,
    functionName: 'allowance',
    args: address && addrs.positionManager ? [address, addrs.positionManager] : undefined,
    query: { enabled: Boolean(address && selectedMarket && addrs.positionManager), refetchInterval: 5_000 },
  });
  const allowance = allowanceRead.isError ? undefined : allowanceRead.data;

  const build = (now: number) => {
    const requested = selection.current;
    if (!requested) return null;
    let request: TradeIntentRequest;
    if (requested.kind === 'open') {
      // Read the live ticket again at send; retaining only click-time params
      // would make the drift check incapable of noticing an edited order.
      request = { kind: 'open', params: openParams };
    } else {
      const frozen = requested.params.position;
      const live = positions.find((position) =>
        position.isLong === frozen.isLong &&
        position.market.indexToken.toLowerCase() === frozen.market.indexToken.toLowerCase() &&
        position.market.collateralToken.toLowerCase() === frozen.market.collateralToken.toLowerCase());
      // Closing an old size after another transaction changed the position
      // requires a fresh review, even if the contract would accept it.
      if (!live || positionSnapshot(live) !== positionSnapshot(frozen) || !live.hasPosition) return null;
      request = { kind: 'close', params: {
        position: live,
        slippagePercent: openParams.slippagePercent,
        deadlineMinutes: openParams.deadlineMinutes,
      } };
    }
    return buildTradingSteps({ owner: address, chainId, positionManager: addrs.positionManager,
      market: selectedMarket, currentPrice, referencePrice: referencePrice.current,
      allowance, request, now });
  };

  const intent = useTxIntent({
    build,
    targetChainId: chainId,
    viewerKey,
    liveAllowance: selection.current?.kind === 'open' ? allowance : undefined,
    onFinalized: () => {
      void queryClient.invalidateQueries({
        predicate: (query) => query.queryKey[0] === 'readContract' || query.queryKey[0] === 'balance',
      });
      options.refetchAll();
      options.refetchActivity();
    },
  });

  const locked = intent.busy || Boolean(intent.hash && intent.state === 'FAILED');
  const review = (request: TradeIntentRequest): TxErrorCode | null => {
    if (locked) return 'UNKNOWN';
    if (!currentPrice || currentPrice <= 0n) return 'PRICE_UNAVAILABLE';
    if ((openParams.deadlineMinutes ?? 20) < 2) return 'DEADLINE_EXPIRED';
    selection.current = request.kind === 'close' ? { kind: 'close', params: { ...request.params,
      position: { ...request.params.position, market: { ...request.params.position.market } },
    } } : request;
    referencePrice.current = currentPrice;
    return intent.review();
  };

  return {
    intent,
    locked,
    reviewOpen: () => review({ kind: 'open', params: openParams }),
    reviewClose: (position: AggregatedPosition) => review({ kind: 'close', params: {
      position, slippagePercent: openParams.slippagePercent, deadlineMinutes: openParams.deadlineMinutes,
    } }),
    // A retry only rebuilds a review. No retry path can send another transaction.
    retry: (): TxErrorCode | null => {
      const requested = selection.current;
      if (!requested) return 'UNKNOWN';
      if (requested.kind === 'open') return review(requested);
      const old = requested.params.position;
      const current = positions.find((position) => position.isLong === old.isLong &&
        position.market.indexToken.toLowerCase() === old.market.indexToken.toLowerCase() &&
        position.market.collateralToken.toLowerCase() === old.market.collateralToken.toLowerCase());
      return current ? review({ kind: 'close', params: { ...requested.params, position: current } }) : 'UNKNOWN';
    },
  };
}

function positionSnapshot(position: AggregatedPosition): string {
  // Market ticker metadata can refresh without changing the position. Only
  // chain position fields identify whether the reviewed position still exists.
  return canonicalize({ ...position, market: undefined });
}
