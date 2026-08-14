import { getPerpAddresses } from './contracts';

export interface PerpMarketConfig {
  symbol: string;
  indexToken: `0x${string}`;
  collateralToken: `0x${string}`;
  indexDecimals: number;
  collateralDecimals: number;
  pricePrecision: number; // Oracle returns prices in this many decimals
}

/**
 * Get available perpetual markets for a chain.
 * Currently only ETH-USD on localhost.
 */
export function getMarkets(chainId: number): PerpMarketConfig[] {
  const addrs = getPerpAddresses(chainId);

  if (!addrs.usdc) return [];

  const markets: PerpMarketConfig[] = [];

  if (addrs.weth) {
    markets.push({
      symbol: 'ETH-USD',
      indexToken: addrs.weth,
      collateralToken: addrs.usdc,
      indexDecimals: 18,
      collateralDecimals: 18,
      pricePrecision: 30,
    });
  }

  if (addrs.wbtc) {
    markets.push({
      symbol: 'BTC-USD',
      indexToken: addrs.wbtc,
      collateralToken: addrs.usdc,
      indexDecimals: 8,
      collateralDecimals: 18,
      pricePrecision: 30,
    });
  }

  return markets;
}
