import type { AtlasPortfolioAsset, PortfolioMetadata } from './api/portfolio';

export function formatPortfolioUsd(value: number | null | undefined, locale: string): string {
  if (typeof value !== 'number' || !Number.isFinite(value) || value < 0) return '—';
  return new Intl.NumberFormat(locale === 'zh' ? 'zh-CN' : 'en-US', {
    style: 'currency', currency: 'USD', minimumFractionDigits: 2, maximumFractionDigits: 2,
  }).format(value);
}

export interface PortfolioQuantity {
  value: string;
  label: 'token' | 'base-unit' | 'usd30' | 'unknown' | 'unconfirmed';
}

export function formatPortfolioQuantity(asset: AtlasPortfolioAsset): PortfolioQuantity {
  const raw = asset.rawBalance;
  if (!raw) return { value: '—', label: asset.balanceUnit && asset.assetType === 'token' ? 'unconfirmed' : 'unknown' };
  if (!asset.balanceUnit) return { value: raw, label: 'unknown' };
  if (asset.balanceUnit !== 'usd30') return { value: raw, label: asset.balanceUnit };
  if (!/^\d+$/.test(raw)) return { value: raw, label: 'unknown' };
  // USD30 is notional exposure. Shift decimal digits exactly; converting a
  // large on-chain integer through Number would destroy the user's quantity.
  const digits = raw.replace(/^0+(?=\d)/, '').padStart(31, '0');
  const fraction = digits.slice(-30).replace(/0+$/, '');
  return { value: `${digits.slice(0, -30)}${fraction ? `.${fraction}` : ''}`, label: 'usd30' };
}

export function getPortfolioView(data?: PortfolioMetadata) {
  const coverage = data?.coverage;
  const states = coverage ? Object.values(coverage.sources) : [];
  const sourceState: 'ready' | 'partial' | 'unavailable' | 'unknown' = !coverage ? 'unknown'
    : states.every((state) => state === 'ready') ? 'ready'
      : states.every((state) => state === 'unavailable') ? 'unavailable' : 'partial';
  const valid = data?.valuationVersion === 1;
  return {
    valuationState: valid ? data.valuation.state : 'unavailable' as const,
    knownSubtotalUsd: valid ? data.valuation.knownSubtotalUsd : null,
    valuedCount: valid ? data.valuation.valuedCount : 0,
    unpricedCount: data?.valuation.unpricedCount ?? 0,
    sourceState,
    isLegacy: Boolean(data && !valid),
    isEmpty: valid && data.valuation.state === 'empty' && sourceState === 'ready',
  };
}
