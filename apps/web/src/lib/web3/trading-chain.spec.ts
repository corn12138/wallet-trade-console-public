import { describe, expect, it } from 'vitest';
import {
  DEFAULT_TRADING_CHAIN_ID,
  hasPerpTradingContracts,
  resolveTradingChainId,
} from './trading-chain';

describe('trading chain resolution', () => {
  it('defaults unconnected and unsupported chains to Sepolia', () => {
    expect(resolveTradingChainId(undefined)).toBe(DEFAULT_TRADING_CHAIN_ID);
    expect(resolveTradingChainId(null)).toBe(DEFAULT_TRADING_CHAIN_ID);
    expect(resolveTradingChainId(1)).toBe(DEFAULT_TRADING_CHAIN_ID);
  });

  it('keeps chains with deployed perp contracts', () => {
    expect(hasPerpTradingContracts(11155111)).toBe(true);
    expect(resolveTradingChainId(11155111)).toBe(11155111);
    expect(resolveTradingChainId(31337)).toBe(31337);
  });
});
