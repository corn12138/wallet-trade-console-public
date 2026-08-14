import type { TradingHistoryApi, TradingOrderbookApi, TradingPendingOrderApi } from '@/lib/api';
import type { AggregatedPosition } from '@/hooks/web3/usePerpPositions';
import type { TradingMarketView } from '@/hooks/trading.types';

export type TabKey = 'positions' | 'orders' | 'history';
export type SortKey = 'vol' | 'oi' | 'fund' | 'sym';
export type TradeSide = 'long' | 'short';
export type PriceFlash = 'up' | 'down' | null;

export type TradePosition = AggregatedPosition;
export type TradeOrder = TradingPendingOrderApi;
export type TradeHistoryItem = TradingHistoryApi;
export type TradeOrderbook = TradingOrderbookApi | null | undefined;
export type TradeMarket = TradingMarketView;
