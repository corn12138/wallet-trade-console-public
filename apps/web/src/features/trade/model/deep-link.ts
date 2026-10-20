import { tradeEntry } from './entry';

export type TradeLink = Readonly<{ symbol: string; chainId: number | null; chainInvalid: boolean }>;
export type TradeLinkState =
  | { kind: 'none' }
  | { kind: 'symbolOnly'; symbol: string }
  | { kind: 'ready'; symbol: string; chainId: number }
  | { kind: 'connectRequired'; symbol: string; chainId: number; displayable: boolean }
  | { kind: 'switchRequired'; symbol: string; chainId: number; walletChainId: number }
  | { kind: 'unsupported'; symbol: string; chainId: number | null };

const CHAIN = /^[1-9][0-9]{0,9}$/;

export function parseTradeLink(search: string): TradeLink | null {
  const params = new URLSearchParams(search);
  const symbol = params.get('symbol');
  if (!symbol) return null;
  const raw = params.get('chainId');
  if (raw === null) return { symbol, chainId: null, chainInvalid: false };
  return CHAIN.test(raw) ? { symbol, chainId: Number(raw), chainInvalid: false } : { symbol, chainId: null, chainInvalid: true };
}

/** How the trading page may honor a link. A link naming a chain is resolved
 * only by a connected wallet on that chain (`walletChainId`); the chain the
 * terminal merely displays (`displayChainId`, a default while disconnected
 * or on an unsupported wallet chain) never resolves it. Disconnected, the
 * target may be shown read-only when the display chain matches; on another
 * wallet chain the page asks for the ordinary explicit switch and selects
 * nothing. A link without a chain keeps the symbol-only behavior. */
export function tradeLinkState(link: TradeLink | null, displayChainId: number, walletChainId: number | null): TradeLinkState {
  if (!link) return { kind: 'none' };
  if (link.chainInvalid) return { kind: 'unsupported', symbol: link.symbol, chainId: null };
  if (link.chainId === null) return { kind: 'symbolOnly', symbol: link.symbol };
  if (!tradeEntry({ chainId: link.chainId, marketId: link.symbol })) return { kind: 'unsupported', symbol: link.symbol, chainId: link.chainId };
  if (walletChainId === null) return { kind: 'connectRequired', symbol: link.symbol, chainId: link.chainId, displayable: displayChainId === link.chainId };
  return walletChainId === link.chainId ? { kind: 'ready', symbol: link.symbol, chainId: link.chainId }
    : { kind: 'switchRequired', symbol: link.symbol, chainId: link.chainId, walletChainId };
}
