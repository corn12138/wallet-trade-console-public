import { getOptionalContractAddress } from './contracts';

export const DEFAULT_TRADING_CHAIN_ID = 11155111;

export type TradingChainId = 31337 | typeof DEFAULT_TRADING_CHAIN_ID;

const TRADING_CHAIN_IDS: readonly TradingChainId[] = [DEFAULT_TRADING_CHAIN_ID, 31337];
const REQUIRED_PERP_CONTRACTS = ['PerpMarket', 'PositionManager', 'MockUSDC'] as const;

function isTradingChainId(chainId: number | null | undefined): chainId is TradingChainId {
  return TRADING_CHAIN_IDS.includes(chainId as TradingChainId);
}

export function hasPerpTradingContracts(chainId: number | null | undefined): chainId is TradingChainId {
  if (!isTradingChainId(chainId)) {
    return false;
  }

  return REQUIRED_PERP_CONTRACTS.every((name) => Boolean(getOptionalContractAddress(chainId, name)));
}

export function resolveTradingChainId(chainId: number | null | undefined): TradingChainId {
  return hasPerpTradingContracts(chainId) ? chainId : DEFAULT_TRADING_CHAIN_ID;
}
