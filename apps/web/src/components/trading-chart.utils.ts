import type { Time } from 'lightweight-charts';
import { formatUnits } from 'viem';
import type { PriceCandleApi, TradingCandleApi, TradingCandleResolution } from '@/lib/api';

export const TRADING_CHART_RESOLUTIONS: Array<{
  label: string;
  value: TradingCandleResolution;
}> = [
  { label: '1m', value: '1m' },
  { label: '5m', value: '5m' },
  { label: '15m', value: '15m' },
  { label: '1H', value: '1h' },
  { label: '4H', value: '4h' },
  { label: '1D', value: '1d' },
];

export interface TradingChartPoint {
  time: Time;
  open: number;
  high: number;
  low: number;
  close: number;
}

export function mapTradingCandlesToChartData(candles: TradingCandleApi[]): TradingChartPoint[] {
  return candles
    .map((candle) => ({
      time: (candle.timestamp / 1000) as Time,
      open: parseTradingPriceValue(candle.open),
      high: parseTradingPriceValue(candle.high),
      low: parseTradingPriceValue(candle.low),
      close: parseTradingPriceValue(candle.close),
    }))
    .filter((candle) => candle.high > 0)
    .sort((left, right) => Number(left.time) - Number(right.time));
}

/**
 * Reference-price candles (/api/prices/candles) arrive as plain decimal
 * strings — Postgres NUMERIC(38,18) rendered with ::text, e.g. "1911.100…".
 *
 * They deliberately do NOT go through parseTradingNumberish: that helper
 * guesses whether a value is a raw USD-30 integer based on digit count, which
 * is right for perp_trades but would misread a long all-digits reference price
 * as wei and divide it by 1e30. This source is never raw-integer, so it is
 * parsed directly.
 */
export function mapPriceCandlesToChartData(candles: PriceCandleApi[]): TradingChartPoint[] {
  return candles
    .map((candle) => ({
      time: (candle.timestamp / 1000) as Time,
      open: parseDecimalString(candle.open),
      high: parseDecimalString(candle.high),
      low: parseDecimalString(candle.low),
      close: parseDecimalString(candle.close),
    }))
    .filter((candle) => candle.high > 0)
    .sort((left, right) => Number(left.time) - Number(right.time));
}

/** Format a reference-candle volume (plain decimal base-asset amount). */
export function formatPriceCandleVolume(value: string) {
  const amount = parseDecimalString(value);
  if (!Number.isFinite(amount) || amount <= 0) {
    return '0';
  }
  if (amount >= 1_000_000) {
    return `${(amount / 1_000_000).toFixed(2)}M`;
  }
  if (amount >= 1_000) {
    return `${(amount / 1_000).toFixed(2)}K`;
  }
  return amount.toFixed(4);
}

function parseDecimalString(value: string) {
  const parsed = Number(String(value ?? '').trim());
  return Number.isFinite(parsed) ? parsed : 0;
}

export function formatTradingChartPrice(value: number) {
  if (!Number.isFinite(value) || value <= 0) {
    return '—';
  }

  if (value < 0.00001) {
    return value.toExponential(4);
  }

  if (value < 1) {
    return value.toFixed(6);
  }

  return value.toLocaleString(undefined, {
    minimumFractionDigits: 2,
    maximumFractionDigits: 2,
  });
}

/** Candle volume is the summed sizeDelta (raw USD-30) from perp_trades. */
export function formatTradingChartVolume(value: string) {
  const amount = parseTradingNumberish(value, 30);
  if (!Number.isFinite(amount) || amount <= 0) {
    return '0';
  }
  if (amount >= 1_000_000) {
    return `${(amount / 1_000_000).toFixed(2)}M`;
  }
  if (amount >= 1_000) {
    return `${(amount / 1_000).toFixed(2)}K`;
  }
  return amount.toFixed(2);
}

function parseTradingPriceValue(value: string) {
  return parseTradingNumberish(value, 30);
}

function parseTradingNumberish(value: string, decimals: number) {
  const trimmedValue = value.trim();

  if (!trimmedValue) {
    return 0;
  }

  if (looksLikeRawIntegerAmount(trimmedValue)) {
    try {
      return Number(formatUnits(BigInt(trimmedValue), decimals));
    } catch {
      return 0;
    }
  }

  const numericValue = Number(trimmedValue);
  return Number.isFinite(numericValue) ? numericValue : 0;
}

function looksLikeRawIntegerAmount(value: string) {
  const normalizedValue = value.startsWith('-') ? value.slice(1) : value;
  return /^\d+$/.test(normalizedValue) && normalizedValue.length > 18;
}
