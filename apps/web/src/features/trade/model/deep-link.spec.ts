import { describe, expect, it } from 'vitest';
import { parseTradeLink, tradeLinkState } from './deep-link';

const SEPOLIA = 11155111, LOCAL = 31337;
const state = (search: string, display: number, wallet: number | null) => tradeLinkState(parseTradeLink(search), display, wallet);

describe('trade deep link', () => {
  it('is resolved only by a connected wallet on its chain', () => {
    expect(state(`?symbol=ETH-USD&chainId=${SEPOLIA}`, SEPOLIA, SEPOLIA)).toEqual({ kind: 'ready', symbol: 'ETH-USD', chainId: SEPOLIA });
    expect(state(`?symbol=ETH-USD&chainId=${LOCAL}`, LOCAL, LOCAL)).toEqual({ kind: 'ready', symbol: 'ETH-USD', chainId: LOCAL });
  });

  it('is never resolved by the display chain alone', () => {
    expect(state(`?symbol=ETH-USD&chainId=${SEPOLIA}`, SEPOLIA, null)).toEqual({ kind: 'connectRequired', symbol: 'ETH-USD', chainId: SEPOLIA, displayable: true });
    expect(state(`?symbol=ETH-USD&chainId=${LOCAL}`, SEPOLIA, null)).toEqual({ kind: 'connectRequired', symbol: 'ETH-USD', chainId: LOCAL, displayable: false });
    // An unsupported wallet chain displays the default chain; that is not the target.
    expect(state(`?symbol=ETH-USD&chainId=${SEPOLIA}`, SEPOLIA, 1)).toEqual({ kind: 'switchRequired', symbol: 'ETH-USD', chainId: SEPOLIA, walletChainId: 1 });
  });

  it.each([[SEPOLIA, LOCAL], [LOCAL, SEPOLIA]])('asks for a switch instead of substituting wallet chain %s for target %s', (wallet, target) => {
    expect(state(`?symbol=ETH-USD&chainId=${target}`, wallet, wallet)).toEqual({ kind: 'switchRequired', symbol: 'ETH-USD', chainId: target, walletChainId: wallet });
  });

  it.each([
    ['?symbol=ETH-USD&chainId=1', 1], ['?symbol=DOGE-USD&chainId=11155111', SEPOLIA], ['?symbol=ETH-USD&chainId=0x1', null],
    ['?symbol=ETH-USD&chainId=011155111', null], ['?symbol=ETH-USD&chainId=', null], ['?symbol=ETH-USD&chainId=-1', null],
  ])('refuses an unsupported or malformed target %s', (search, chainId) => {
    for (const wallet of [null, SEPOLIA]) {
      expect(state(search, SEPOLIA, wallet)).toEqual({ kind: 'unsupported', symbol: search.includes('DOGE') ? 'DOGE-USD' : 'ETH-USD', chainId });
    }
  });

  it('keeps symbol-only links and absent links as before', () => {
    expect(state('?symbol=ETH-USD', LOCAL, null)).toEqual({ kind: 'symbolOnly', symbol: 'ETH-USD' });
    expect(state('', LOCAL, LOCAL)).toEqual({ kind: 'none' });
  });
});
