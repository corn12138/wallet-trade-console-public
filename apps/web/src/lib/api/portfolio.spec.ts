import { beforeEach, describe, expect, it, vi } from 'vitest';
import { getPortfolioAssets, getPortfolioSummary, normalizePortfolioAssets, normalizePortfolioSummary, PortfolioApiError } from './portfolio';

const request = vi.fn();
vi.mock('./auth-fetch', () => ({ fetchApi: (...args: unknown[]) => request(...args) }));

const coverage = {
  scope: 'indexed-protocol-assets', limits: { createdTokens: 20, holdings: 40 },
  sources: { staking: 'ready', perp: 'ready', createdTokens: 'ready', holdings: 'ready' },
};
const row = {
  id: 'stake-1', assetType: 'staking', symbol: 'Pool A', name: 'Pool A', chainId: 11155111,
  rawBalance: '123456789123456789.000000000000000001', balanceUnit: 'token', valueUsd: null,
  valuationStatus: 'unavailable', valuationReason: 'missing-usd-price', status: 'active', source: 'staking',
};
const unavailable = {
  valuationVersion: 1, coverage,
  valuation: { state: 'unavailable', knownSubtotalUsd: null, valuedCount: 0, unpricedCount: 1 },
  items: [row], filterSummary: { visibleCount: 1 },
};

beforeEach(() => request.mockReset());

describe('portfolio response compatibility', () => {
  it('preserves exact raw quantities while v1 unavailable never becomes zero USD', () => {
    const response = normalizePortfolioAssets(unavailable);
    expect(response.valuationVersion).toBe(1);
    expect(response.items[0]).toMatchObject({ rawBalance: row.rawBalance, balanceUnit: 'token', valueUsd: null });
    expect(response.valuation.knownSubtotalUsd).toBeNull();
  });

  it.each([undefined, 2, '1'])('does not trust legacy/unknown marker %s even with positive USD values', (valuationVersion) => {
    const response = normalizePortfolioAssets({ ...unavailable, valuationVersion, items: [{ ...row, valueUsd: 900000, valuationStatus: 'known' }] });
    expect(response.valuationVersion).toBeNull();
    expect(response.items[0]).toMatchObject({ rawBalance: row.rawBalance, balanceUnit: null, valueUsd: null, valuationStatus: 'unavailable' });
    expect(response.valuation.knownSubtotalUsd).toBeNull();
  });

  it('keeps genuine known zero distinct from unknown and supports partial valuation', () => {
    const response = normalizePortfolioAssets({ ...unavailable,
      items: [{ ...row, id: 'zero', valueUsd: 0, valuationStatus: 'known', valuationReason: null }, row],
      valuation: { state: 'partial', knownSubtotalUsd: 0, valuedCount: 1, unpricedCount: 1 },
    });
    expect(response.valuationVersion).toBe(1);
    expect(response.items[0].valueUsd).toBe(0);
    expect(response.items[1].valueUsd).toBeNull();
    expect(response.valuation).toMatchObject({ state: 'partial', knownSubtotalUsd: 0 });
  });

  it.each([null, -1, Infinity, NaN, '100'])('rejects malformed known value %s without trusting its summary', (valueUsd) => {
    const response = normalizePortfolioAssets({ ...unavailable, items: [{ ...row, valuationStatus: 'known', valueUsd }],
      valuation: { state: 'complete', knownSubtotalUsd: 100, valuedCount: 1, unpricedCount: 0 },
    });
    expect(response.valuation.knownSubtotalUsd).toBeNull();
    expect(response.items[0].valueUsd).toBeNull();
  });

  it.each([
    { valuation: { state: 'empty', knownSubtotalUsd: 100, valuedCount: 0, unpricedCount: 0 } },
    { valuation: { state: 'complete', knownSubtotalUsd: null, valuedCount: 1, unpricedCount: 0 } },
    { valuation: { state: 'partial', knownSubtotalUsd: 1, valuedCount: -1, unpricedCount: 2 } },
    { coverage: { ...coverage, sources: { ...coverage.sources, staking: 'invented' } } },
    { coverage: { ...coverage, limits: { createdTokens: 200, holdings: 40 } } },
    { items: [{ ...row, balanceUnit: 'ether' }] },
  ])('fails closed for inconsistent v1 metadata %#', (patch) => {
    const response = normalizePortfolioAssets({ ...unavailable, ...patch });
    expect(response.valuationVersion).toBeNull();
    expect(response.valuation.state).toBe('unavailable');
  });

  it('does not default numeric raw balances to 18 decimals or stringify a rounded number', () => {
    const response = normalizePortfolioAssets({ ...unavailable, items: [{ ...row, rawBalance: 1e30 }] });
    expect(response.items[0].rawBalance).toBe('');
  });

  it('rejects a subtotal that contradicts the visible known rows', () => {
    const response = normalizePortfolioAssets({ ...unavailable,
      items: [{ ...row, valueUsd: 10, valuationStatus: 'known', valuationReason: null }],
      valuation: { state: 'complete', knownSubtotalUsd: 100, valuedCount: 1, unpricedCount: 0 },
    });
    expect(response.valuationVersion).toBeNull();
    expect(response.items[0].valueUsd).toBeNull();
  });

  it('validates summary separately from assets and rejects inconsistent visible counts', () => {
    const summary = { valuationVersion: 1, coverage, portfolioValueUsd: 99,
      valuation: { state: 'complete', knownSubtotalUsd: 99, valuedCount: 1, unpricedCount: 0 },
      assetFilters: { visibleCount: 1 } };
    expect(normalizePortfolioSummary(summary).valuation.knownSubtotalUsd).toBe(99);
    expect(normalizePortfolioSummary({ ...summary, portfolioValueUsd: 100,
      valuation: { ...summary.valuation, knownSubtotalUsd: 0 } }).portfolioValueUsd).toBeNull();
    expect(normalizePortfolioSummary({ ...summary, valuationVersion: undefined }).portfolioValueUsd).toBeNull();
    expect(normalizePortfolioSummary({ ...summary, assetFilters: { visibleCount: 2 } }).valuationVersion).toBeNull();
  });

  it('preserves source failures without claiming an empty inventory', () => {
    const response = normalizePortfolioAssets({ ...unavailable, items: [],
      valuation: { state: 'empty', knownSubtotalUsd: null, valuedCount: 0, unpricedCount: 0 },
      coverage: { ...coverage, sources: { ...coverage.sources, staking: 'unavailable' } },
    });
    expect(response.coverage?.sources.staking).toBe('unavailable');
    expect(response.valuation.state).toBe('empty');
  });

  it('rejects malformed assets shape instead of inventing a successful empty list', () => {
    expect(() => normalizePortfolioAssets({})).toThrow(PortfolioApiError);
  });
});

describe('portfolio HTTP boundary', () => {
  it('passes AbortSignal and keeps cross-chain reads free of a chain filter', async () => {
    request.mockResolvedValue(new Response(JSON.stringify(unavailable)));
    const signal = new AbortController().signal;
    await getPortfolioAssets('0xabc', undefined, signal);
    expect(request).toHaveBeenCalledWith('/portfolio/assets?address=0xabc', { signal });
    request.mockResolvedValue(new Response(JSON.stringify({ assetFilters: { visibleCount: 0 } })));
    await getPortfolioSummary('0xabc', 11155111, signal);
    expect(request).toHaveBeenLastCalledWith('/portfolio/summary?address=0xabc&chainId=11155111', { signal });
  });

  it.each([401, 403, 500, 503])('preserves public status/code for failure %i', async (status) => {
    request.mockResolvedValue(new Response(JSON.stringify({ code: 'PORTFOLIO_VISIBILITY_UNAVAILABLE', message: 'Unavailable' }), { status }));
    await expect(getPortfolioAssets()).rejects.toMatchObject({ status, code: 'PORTFOLIO_VISIBILITY_UNAVAILABLE' });
  });
});
