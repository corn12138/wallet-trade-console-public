import type { PerpMarketConfig } from '@/lib/web3/markets';
import type { TradingPositionApi } from '@/lib/api';
import type { AggregatedPosition } from './web3/usePerpPositions';

/**
 * One row of the portfolio positions table. `chain` rows are the on-chain
 * read for the selected market — authoritative and closeable (the close call
 * needs the market config and the live oracle price). `indexed` rows come
 * from the DB projection and cover every other market — display plus a
 * switch-to-market action, never a direct close (closing them here would
 * price the acceptable-price guard off the wrong market's oracle).
 */
export type PortfolioPositionRow =
  | { kind: 'chain'; position: AggregatedPosition }
  | { kind: 'indexed'; position: TradingPositionApi; marketSymbol: string | null };

export interface TradingMarketView extends PerpMarketConfig {
  chainId: number;
  fundingRate: string;
  longOpenInterest: string;
  shortOpenInterest: string;
  volume24h: string;
}

export interface OpenTradingPositionParams {
  collateralAmount: string;
  leverage: number;
  isLong: boolean;
  acceptablePrice?: string;
  slippagePercent?: number;
  deadlineMinutes?: number;
}

export interface CloseTradingPositionParams {
  position: AggregatedPosition;
  slippagePercent?: number;
  deadlineMinutes?: number;
}
