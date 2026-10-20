import { getMarkets } from '@/lib/web3/markets';
import { hasPerpTradingContracts } from '@/lib/web3/trading-chain';

export type TradeEntryTarget = Readonly<{ chainId: number; marketId: string }>;
export type TradeEntry = Readonly<{ href: string; chainId: number; marketId: string }>;

/** The ordinary trading page for a market this app can trade, or null. Other
 * features may link to it; they cannot pass an amount, side, calldata or
 * approval, and opening it places nothing. The trading page keeps its own
 * wallet, chain and market checks. */
export function tradeEntry(target: TradeEntryTarget): TradeEntry | null {
  const { chainId, marketId } = target;
  if (!Number.isSafeInteger(chainId) || !hasPerpTradingContracts(chainId)) return null;
  if (!getMarkets(chainId).some((market) => market.symbol === marketId)) return null;
  // The chain travels with the link; the trading page applies it only on that chain.
  return { href: `/trade?${new URLSearchParams({ symbol: marketId, chainId: String(chainId) })}`, chainId, marketId };
}
