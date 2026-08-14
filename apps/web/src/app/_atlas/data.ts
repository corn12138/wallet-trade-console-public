// Shared number formatters for the Atlas shell. The static sample orderbook
// (ATLAS) that used to live here was deleted with the home terminal preview:
// the home page now renders the real live trading service (HomeLivePreview →
// TradeCandleChart), so no product surface ships sample/random trading data.
export const fmt = (n: number, d = 2) => Number(n).toLocaleString('en-US', { minimumFractionDigits: d, maximumFractionDigits: d });
// Locale-aware compact rendering of a decimal STRING (the API's normalized
// NATIVE amounts). The durable value stays the string; Number() here is a
// leaf display conversion guarded against non-finite input.
export const fmtCompactDec = (dec: string | null | undefined, locale = 'en') => {
  if (!dec) return '0';
  const n = Number(dec);
  if (!Number.isFinite(n)) return '0';
  return new Intl.NumberFormat(locale === 'zh' ? 'zh-CN' : 'en-US', {
    notation: 'compact',
    maximumFractionDigits: 2,
  }).format(n);
};

export const fmtCompact = (n: number) => {
  const abs = Math.abs(Number(n));
  if (abs >= 1e9) return (n / 1e9).toFixed(2) + 'B';
  if (abs >= 1e6) return (n / 1e6).toFixed(2) + 'M';
  if (abs >= 1e3) return (n / 1e3).toFixed(1) + 'K';
  return Number(n).toFixed(2);
};
export const fmtPct = (n: number) => (n >= 0 ? '+' : '') + Number(n).toFixed(2) + '%';
