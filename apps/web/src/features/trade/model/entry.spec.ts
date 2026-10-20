import { describe, expect, it } from 'vitest';
import { tradeEntry } from './entry';

describe('trade entry facade', () => {
  it('links only to markets this app can trade', () => {
    expect(tradeEntry({ chainId: 11155111, marketId: 'ETH-USD' })).toEqual({ href: '/trade?symbol=ETH-USD&chainId=11155111', chainId: 11155111, marketId: 'ETH-USD' });
    // Both chains ship ETH-USD; the link must not collapse them into one symbol.
    expect(tradeEntry({ chainId: 31337, marketId: 'ETH-USD' })?.href).toBe('/trade?symbol=ETH-USD&chainId=31337');
    for (const target of [
      { chainId: 11155111, marketId: 'DOGE-USD' }, { chainId: 1, marketId: 'ETH-USD' }, { chainId: 0, marketId: 'ETH-USD' },
      { chainId: Number.NaN, marketId: 'ETH-USD' }, { chainId: 11155111, marketId: 'ETH-USD&side=long' }, { chainId: 11155111, marketId: '' },
    ]) expect(tradeEntry(target)).toBeNull();
  });
});
