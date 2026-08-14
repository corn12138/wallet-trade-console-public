import type { PerpMarketConfig } from '@/lib/web3/markets';
import type { AggregatedPosition } from './web3/usePerpPositions';

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
