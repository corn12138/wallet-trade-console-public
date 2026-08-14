import { fmtCompact } from '../data';

export function priceFlash(prev: bigint | undefined, next: bigint | undefined): 'up' | 'down' | null {
  if (!prev || !next || prev === next) return null;
  return next > prev ? 'up' : 'down';
}

export function formatBalanceDisplay(formatted: string) {
  if (!formatted || formatted === '—') return '0.00';
  const numeric = Number(formatted);
  if (!Number.isFinite(numeric)) return formatted;
  return numeric.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 });
}

export function compactFromString(value: string | undefined | null): string {
  if (value === undefined || value === null || value === '') return '0';
  const numeric = Number(value);
  if (!Number.isFinite(numeric)) return value;
  return fmtCompact(numeric);
}

export function percentFromString(value: string | undefined | null): number {
  const n = Number(value ?? 0);
  return Number.isFinite(n) ? n : 0;
}
