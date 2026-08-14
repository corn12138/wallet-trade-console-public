export function paletteFor(idx: number) {
  const palette = ['c', 'b', 'o', 'p', 'y', 'g', 'r'];
  return palette[idx % palette.length];
}

export function chainSwatch(id: number) {
  if (id === 11155111) return { color: 'c', icon: 'S' };
  if (id === 1) return { color: 'b', icon: 'M' };
  if (id === 31337) return { color: 'y', icon: 'L' };
  if (id === 84532) return { color: 'b', icon: 'B' };
  if (id === 421614) return { color: 'p', icon: 'A' };
  return { color: 'p', icon: '?' };
}

export function shortAddr(addr: string) {
  if (!addr || addr.length <= 12) return addr;
  return addr.slice(0, 6) + '…' + addr.slice(-4);
}

export function formatAllowance(raw: string): string {
  if (!raw) return '—';
  if (
    raw.length >= 70 ||
    raw === '115792089237316195423570985008687907853269984665640564039457584007913129639935'
  ) {
    return 'Unlimited';
  }
  return raw;
}

export function riskColor(level: string): string {
  if (level === 'critical' || level === 'high') return 'r';
  if (level === 'medium' || level === 'med') return 'y';
  if (level === 'low') return 'g';
  return 'c';
}
