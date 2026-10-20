import { describe, expect, it } from 'vitest';
import { formatPortfolioQuantity, formatPortfolioUsd, getPortfolioView } from './portfolio-display';
import { normalizePortfolioAssets, type AtlasPortfolioAsset } from './api/portfolio';

const asset = (patch: Partial<AtlasPortfolioAsset>) => ({ rawBalance: '', balanceUnit: null, ...patch }) as AtlasPortfolioAsset;

describe('exact portfolio presentation', () => {
  it('keeps USD30 notional exposure exact at and beyond JavaScript integer precision', () => {
    expect(formatPortfolioQuantity(asset({ balanceUnit: 'usd30', rawBalance: '1000000000000000000000000000000000' })))
      .toEqual({ label: 'usd30', value: '1000' });
    expect(formatPortfolioQuantity(asset({ balanceUnit: 'usd30', rawBalance: '9007199254740993123456789012345678901234567890001' })))
      .toEqual({ label: 'usd30', value: '9007199254740993123.456789012345678901234567890001' });
  });

  it('preserves token units and raw base units without guessing token decimals', () => {
    const rawBalance = '100000000000000000000000000000000000000001';
    expect(formatPortfolioQuantity(asset({ balanceUnit: 'base-unit', rawBalance }))).toEqual({ label: 'base-unit', value: rawBalance });
    expect(formatPortfolioQuantity(asset({ balanceUnit: 'token', rawBalance: '100.000000000000000001' })))
      .toEqual({ label: 'token', value: '100.000000000000000001' });
    expect(formatPortfolioQuantity(asset({ rawBalance }))).toEqual({ label: 'unknown', value: rawBalance });
  });

  it('distinguishes a missing holder record from a real zero record', () => {
    expect(formatPortfolioQuantity(asset({ assetType: 'token', balanceUnit: 'base-unit', rawBalance: '' })))
      .toEqual({ label: 'unconfirmed', value: '—' });
    expect(formatPortfolioQuantity(asset({ assetType: 'token', balanceUnit: 'base-unit', rawBalance: '0' })))
      .toEqual({ label: 'base-unit', value: '0' });
  });

  it('renders known zero as USD but never unavailable or invalid values', () => {
    expect(formatPortfolioUsd(0, 'en')).toBe('$0.00');
    for (const value of [undefined, null, Infinity, NaN, -100]) expect(formatPortfolioUsd(value, 'en')).toBe('—');
  });

  it('requires readable sources as well as empty valuation before declaring no assets', () => {
    const data = normalizePortfolioAssets({ valuationVersion: 1, items: [],
      valuation: { state: 'empty', knownSubtotalUsd: null, valuedCount: 0, unpricedCount: 0 },
      coverage: { scope: 'indexed-protocol-assets', limits: { createdTokens: 20, holdings: 40 },
        sources: { staking: 'unavailable', perp: 'ready', holdings: 'ready', createdTokens: 'ready' } },
    });
    expect(getPortfolioView(data)).toMatchObject({ sourceState: 'partial', isEmpty: false });
    data.coverage!.sources.staking = 'ready';
    expect(getPortfolioView(data)).toMatchObject({ sourceState: 'ready', isEmpty: true });
    expect(getPortfolioView(normalizePortfolioAssets({ items: [] }))).toMatchObject({ isLegacy: true, isEmpty: false });
  });
});
