'use client';
import { useEffect, useMemo, useState } from 'react';
import { useAccount, useChainId } from 'wagmi';
import { useQuery } from '@tanstack/react-query';
import { getPerpAddresses } from '@/lib/web3/contracts';
import {
  getTradingMarketTrades,
  getTradingMarketsByChain,
  getTradingOrderbook,
  getTradingStats,
  type TradingMarketApi,
} from '@/lib/api';
import { useMarketStream } from './useMarketStream';
import { resolveTradingChainId } from '@/lib/web3/trading-chain';
import { usePerpPrice } from './web3/usePerpPrice';
import { usePerpPositions } from './web3/usePerpPositions';
import { useTokenBalance } from './web3/useTokenBalance';
import { useTradingActivity } from './useTradingActivity';
import { useTradingTransactions } from './useTradingTransactions';
import type { TradingMarketView } from './trading.types';
import { useAuth } from '@/lib/web3/auth-provider';

function toPerpMarketConfig(market: TradingMarketApi): TradingMarketView {
  return {
    symbol: market.symbol,
    indexToken: market.indexToken,
    collateralToken: market.collateralToken,
    indexDecimals: 18,
    collateralDecimals: 18,
    pricePrecision: 30,
    chainId: market.chainId,
    fundingRate: market.fundingRate,
    longOpenInterest: market.longOpenInterest,
    shortOpenInterest: market.shortOpenInterest,
    volume24h: market.volume24h,
  };
}

export function useTrading() {
  const { address } = useAccount();
  const { isAuthenticated, authenticatedAddress } = useAuth();
  const walletChainId = useChainId();
  const chainId = resolveTradingChainId(walletChainId);
  const addrs = getPerpAddresses(chainId);
  const [selectedMarketSymbol, setSelectedMarketSymbol] = useState<string | null>(null);

  const { data: remoteMarkets = [], isLoading: isMarketsLoading } = useQuery({
    queryKey: ['trading-markets', chainId],
    queryFn: () => getTradingMarketsByChain(chainId),
    staleTime: 30_000,
    refetchInterval: 30_000,
  });

  const { data: tradingStats } = useQuery({
    queryKey: ['trading-stats', chainId],
    queryFn: () => getTradingStats(chainId),
    staleTime: 30_000,
    refetchInterval: 30_000,
  });

  const markets = useMemo(
    () => remoteMarkets.map(toPerpMarketConfig).filter((market) => market.chainId === chainId),
    [chainId, remoteMarkets],
  );

  useEffect(() => {
    if (markets.length === 0) {
      setSelectedMarketSymbol(null);
      return;
    }

    const hasSelectedMarket = selectedMarketSymbol
      ? markets.some((market) => market.symbol === selectedMarketSymbol)
      : false;

    if (!hasSelectedMarket) {
      setSelectedMarketSymbol(markets[0].symbol);
    }
  }, [markets, selectedMarketSymbol]);

  const selectedMarket = useMemo(
    () => markets.find((market) => market.symbol === selectedMarketSymbol) || markets[0] || null,
    [markets, selectedMarketSymbol],
  );

  const marketStream = useMarketStream({
    symbol: selectedMarket?.symbol,
    chainId: selectedMarket?.chainId,
  });

  const {
    data: orderbook,
    isLoading: isOrderbookLoading,
    isError: isOrderbookError,
    dataUpdatedAt: orderbookUpdatedAt,
    refetch: refetchOrderbook,
  } = useQuery({
    queryKey: ['trading-orderbook', selectedMarket?.chainId, selectedMarket?.symbol],
    enabled: Boolean(selectedMarket?.symbol),
    queryFn: () => getTradingOrderbook(selectedMarket!.symbol, selectedMarket!.chainId),
    staleTime: 10_000,
    refetchInterval: marketStream.isConnected ? false : 10_000,
  });

  const { data: marketRecentTrades = [], isLoading: isMarketRecentTradesLoading } = useQuery({
    queryKey: ['trading-market-trades', selectedMarket?.chainId, selectedMarket?.symbol],
    enabled: Boolean(selectedMarket?.symbol),
    queryFn: () => getTradingMarketTrades(selectedMarket!.symbol, selectedMarket!.chainId, 12),
    staleTime: 10_000,
    refetchInterval: marketStream.isConnected ? false : 10_000,
  });

  const liveSelectedMarket = useMemo(() => {
    if (!selectedMarket) {
      return null;
    }

    if (!marketStream.hasLiveTicker || !marketStream.ticker) {
      return selectedMarket;
    }

    return {
      ...selectedMarket,
      fundingRate: marketStream.ticker.fundingRate,
      longOpenInterest: marketStream.ticker.longOpenInterest,
      shortOpenInterest: marketStream.ticker.shortOpenInterest,
      volume24h: marketStream.ticker.volume24h,
    };
  }, [marketStream.hasLiveTicker, marketStream.ticker, selectedMarket]);

  const displayOrderbook = marketStream.hasLiveOrderbook && marketStream.orderbook
    ? marketStream.orderbook
    : orderbook;
  const recentTrades = marketStream.hasLiveTrades ? marketStream.trades : marketRecentTrades;

  // ═══════════ READ: Oracle Price ═══════════
  const {
    price: currentPrice,
    formatted: formattedPrice,
    isLoading: priceLoading,
  } = usePerpPrice(liveSelectedMarket?.indexToken);

  // ═══════════ READ: USDC Balance ═══════════
  const {
    balance: usdcBalance,
    formatted: formattedBalance,
    isLoading: balanceLoading,
  } = useTokenBalance(addrs.usdc || undefined);

  // ═══════════ READ: Positions ═══════════
  const {
    positions,
    isLoading: positionsLoading,
    refetchAll,
  } = usePerpPositions({ market: liveSelectedMarket });

  const {
    orders,
    history,
    isOrdersLoading,
    isHistoryLoading,
    refetchActivity,
  } = useTradingActivity({
    account: address,
    symbol: liveSelectedMarket?.symbol,
    chainId: liveSelectedMarket?.chainId,
    enabled: Boolean(
      isAuthenticated &&
      address &&
      authenticatedAddress?.toLowerCase() === address.toLowerCase()
    ),
  });
  const tradingTransactions = useTradingTransactions({
    address,
    addrs,
    chainId,
    currentPrice,
    selectedMarket: liveSelectedMarket,
    refetchAll,
    refetchActivity,
  });

  return {
    // Market data
    markets,
    selectedMarket: liveSelectedMarket,
    selectedMarketSymbol,
    setSelectedMarketSymbol,
    isMarketsLoading,
    currentPrice,
    formattedPrice,
    priceLoading,
    orderbook: displayOrderbook,
    isOrderbookLoading: isOrderbookLoading && !marketStream.hasLiveOrderbook,
    isOrderbookError: isOrderbookError && !marketStream.hasLiveOrderbook,
    orderbookUpdatedAt,
    refetchOrderbook,
    hasLiveOrderbook: marketStream.hasLiveOrderbook,
    tradingStats,
    recentTrades,
    isRecentTradesLoading: isMarketRecentTradesLoading && !marketStream.hasLiveTrades,
    marketStreamStatus: marketStream.status,
    marketStreamLastMessageAt: marketStream.lastMessageAt,
    isMarketStreamConnected: marketStream.isConnected,

    // User data
    usdcBalance,
    formattedBalance,
    balanceLoading,
    positions,
    positionsLoading,
    orders,
    history,
    isOrdersLoading,
    isHistoryLoading,

    // Actions
    openPosition: tradingTransactions.openPosition,
    closePosition: tradingTransactions.closePosition,

    // Tx states — open
    isApproving: tradingTransactions.isApproving,
    isApproveSuccess: tradingTransactions.isApproveSuccess,
    isOpenPending: tradingTransactions.isOpenPending,
    isOpenConfirming: tradingTransactions.isOpenConfirming,
    isOpenSuccess: tradingTransactions.isOpenSuccess,
    openError: tradingTransactions.openError,
    resetOpen: tradingTransactions.resetOpen,

    // Tx states — close
    isClosePending: tradingTransactions.isClosePending,
    isCloseConfirming: tradingTransactions.isCloseConfirming,
    isCloseSuccess: tradingTransactions.isCloseSuccess,
    closeError: tradingTransactions.closeError,
    resetClose: tradingTransactions.resetClose,
  };
}
