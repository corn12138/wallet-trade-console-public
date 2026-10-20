type ActivityIcon = 'trade' | 'warn' | 'swap' | 'security' | 'earn' | 'rocket' | 'nft' | 'activity';

const EVENT_BUCKETS: Record<string, { type: string; tone: string; icon: ActivityIcon; labelKey: string }> = {
  IncreasePosition: { type: 'open', tone: 'pos', icon: 'trade', labelKey: 'eventOpened' },
  DecreasePosition: { type: 'close', tone: 'neg', icon: 'trade', labelKey: 'eventClosed' },
  Liquidation: { type: 'close', tone: 'neg', icon: 'warn', labelKey: 'eventLiquidation' },
  Swap: { type: 'swap', tone: 'info', icon: 'swap', labelKey: 'eventSwap' },
  Approval: { type: 'approve', tone: 'info', icon: 'security', labelKey: 'eventApproval' },
  Transfer: { type: 'swap', tone: 'info', icon: 'swap', labelKey: 'eventTransfer' },
  Deposit: { type: 'deposit', tone: 'info', icon: 'earn', labelKey: 'eventVaultDeposit' },
  Withdraw: { type: 'deposit', tone: 'warn', icon: 'earn', labelKey: 'eventVaultWithdraw' },
  TokenLaunched: { type: 'open', tone: 'pos', icon: 'rocket', labelKey: 'eventTokenLaunched' },
  NFTMinted: { type: 'open', tone: 'pos', icon: 'nft', labelKey: 'eventNftMinted' },
};

export function bucketFor(eventName: string) {
  return (
    EVENT_BUCKETS[eventName] ?? {
      type: 'other',
      tone: 'info',
      icon: 'activity' as const,
      labelKey: '',
    }
  );
}

export function formatRelative(iso?: string) {
  if (!iso) return '—';
  const dt = new Date(iso).getTime();
  if (!Number.isFinite(dt)) return '—';
  const delta = Math.max(0, Date.now() - dt);
  const s = Math.floor(delta / 1000);
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h`;
  const d = Math.floor(h / 24);
  return `${d}d`;
}

export function summariseEvent(eventName: string, args: Record<string, unknown>): string {
  const fragments: string[] = [];
  const obj = args || {};
  for (const key of Object.keys(obj).slice(0, 4)) {
    const value = obj[key];
    if (value === null || value === undefined) continue;
    const stringValue = typeof value === 'string' ? value : String(value);
    const display =
      stringValue.length > 24 ? `${stringValue.slice(0, 10)}…${stringValue.slice(-6)}` : stringValue;
    fragments.push(`${key}: ${display}`);
  }
  return fragments.join(' · ') || eventName;
}
