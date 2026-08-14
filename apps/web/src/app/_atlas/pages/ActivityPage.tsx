'use client';
import { useMemo, useState } from 'react';
import { useAccount, useChainId } from 'wagmi';
import { useQuery } from '@tanstack/react-query';
import { useTranslations } from 'next-intl';
import { getUserEvents } from '@/lib/api/events';
import { useAuth } from '@/lib/web3';
import { buildTransactionExplorerUrl } from '@/lib/web3/explorer';
import { useApp } from '../AppContext';
import { Icon, LogoCube } from '../Icon';
import { BlockBtn, PageHeader, TabBar } from '../Common';
import { DataStatePanel, SourceMeta, deriveDataPanelState } from '../DataState';

const EVENT_BUCKETS: Record<string, { type: string; tone: string; icon: string; labelKey: string }> = {
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

function bucketFor(eventName: string) {
  return (
    EVENT_BUCKETS[eventName] ?? {
      type: 'other',
      tone: 'info',
      icon: 'activity',
      labelKey: '',
    }
  );
}

function formatRelative(iso?: string) {
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

function summariseEvent(eventName: string, args: Record<string, unknown>): string {
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

export function ActivityPage() {
  const app = useApp();
  const t = useTranslations('activity');
  const tc = useTranslations('commonAtlas');
  const { address } = useAccount();
  const chainId = useChainId();
  const { isAuthenticated } = useAuth();
  const [filter, setFilter] = useState<string>('all');

  const enabled = Boolean(address);

  const {
    data: events = [],
    isLoading,
    isError,
    error,
    isFetching,
    dataUpdatedAt,
    refetch,
  } = useQuery({
    queryKey: ['atlas-design-user-events', address, chainId],
    queryFn: () => getUserEvents(address as string, chainId, 100),
    enabled,
    refetchInterval: 20_000,
  });

  const enriched = useMemo(
    () =>
      events.map((e) => {
        const b = bucketFor(e.eventName);
        return { ...e, ...b };
      }),
    [events],
  );

  const items = useMemo(
    () => (filter === 'all' ? enriched : enriched.filter((a) => a.type === filter)),
    [filter, enriched],
  );

  if (!address) {
    return (
      <div className="col gap-24">
        <PageHeader
          eyebrow={t('headerEyebrow')}
          title={t('connectTitle')}
          kicker={t('connectKicker')}
        />
        <div className="block" style={{ padding: 60, textAlign: 'center' }}>
          <LogoCube size={56} />
          <div className="h-display" style={{ fontSize: 24, marginTop: 16 }}>{tc('watchModeActive')}</div>
          <button className="btn btn-y mt-22" onClick={app.openConnect}>{tc('connectWallet')}</button>
        </div>
      </div>
    );
  }

  const panelState = deriveDataPanelState({
    walletRequired: true,
    hasWallet: Boolean(address),
    isLoading,
    isError,
    rowCount: enriched.length,
  });

  return (
    <div className="col gap-24">
      <PageHeader
        eyebrow={t('headerEyebrow')}
        title={t('headerTitle')}
        kicker={isAuthenticated ? t('headerKicker') : t('headerKickerReadOnly')}
      />

      <section className="row between" style={{ flexWrap: 'wrap', gap: 10 }}>
        <TabBar
          tabs={[
            { key: 'all', label: `${t('tabAll')} · ${enriched.length}` },
            { key: 'open', label: t('tabOpens') },
            { key: 'close', label: t('tabCloses') },
            { key: 'swap', label: t('tabSwaps') },
            { key: 'approve', label: t('tabApprovals') },
            { key: 'deposit', label: t('tabVaults') },
          ]}
          value={filter}
          onChange={setFilter}
        />
        <BlockBtn sm icon="refresh" onClick={() => refetch()}>{t('refresh')}</BlockBtn>
      </section>

      <SourceMeta
        endpoint="GET /api/web3-events/user"
        chainId={chainId}
        wallet={address}
        updatedAt={dataUpdatedAt}
        refetchIntervalMs={20_000}
        rowCount={enriched.length}
        state={panelState}
        isFetching={isFetching}
        onRefresh={() => refetch()}
      />

      <section className="block tight">
        <div className="col gap-6">
          {isLoading && (
            <div className="row gap-14" style={{ padding: 14 }}>
              <div className="skel" style={{ width: 42, height: 42 }} />
              <div style={{ flex: 1 }}>
                <div className="skel lg" style={{ marginBottom: 6 }} />
                <div className="skel" />
              </div>
            </div>
          )}
          {!isLoading &&
            items.map((a) => {
              const swatch = a.tone === 'pos' ? 'g' : a.tone === 'neg' ? 'o' : a.tone === 'warn' ? 'y' : 'c';
              const inverseInk = swatch === 'c' || swatch === 'o';
              const shortHash = a.txHash ? `${a.txHash.slice(0, 6)}…${a.txHash.slice(-4)}` : '—';
              const label = a.labelKey ? t(a.labelKey) : a.eventName;
              return (
                <div
                  key={a.id}
                  className="row gap-14"
                  style={{
                    padding: 14,
                    background: 'var(--bg-2)',
                    border: '2px solid var(--bg-3)',
                    borderRadius: 14,
                  }}
                >
                  <span
                    style={{
                      width: 42,
                      height: 42,
                      borderRadius: 12,
                      background: `var(--${swatch})`,
                      color: inverseInk ? '#fff' : 'var(--ink)',
                      border: '2px solid var(--ink)',
                      display: 'grid',
                      placeItems: 'center',
                    }}
                  >
                    <Icon name={a.icon} size={20} />
                  </span>
                  <div style={{ flex: 1, minWidth: 0 }}>
                    <div className="h-display" style={{ fontSize: 14 }}>{label}</div>
                    <div className="mono" style={{ fontSize: 11, color: 'var(--ink-2)', marginTop: 4 }}>
                      {summariseEvent(a.eventName, a.args)} · {shortHash}
                    </div>
                  </div>
                  <div style={{ textAlign: 'right' }}>
                    <div className="mono" style={{ fontSize: 12, color: 'var(--ink-2)' }}>
                      {t('agoSuffix', { time: formatRelative(a.timestamp) })}
                    </div>
                    {(() => {
                      const explorerUrl = a.txHash
                        ? buildTransactionExplorerUrl(a.txHash, a.chainId ?? chainId)
                        : null;
                      return explorerUrl ? (
                        <a
                          className="btn btn-xs mt-6"
                          href={explorerUrl}
                          target="_blank"
                          rel="noopener noreferrer"
                          data-tip={t('explorerTip')}
                          aria-label={t('explorerTip')}
                        >
                          <Icon name="ext" size={12} />
                        </a>
                      ) : null;
                    })()}
                  </div>
                </div>
              );
            })}
          {!isLoading && !items.length && enriched.length > 0 && (
            <DataStatePanel
              state="empty"
              endpoint="GET /api/web3-events/user"
              emptyTitle={t('emptyFilteredTitle')}
              emptyBody={t('emptyFilteredBody')}
              icon="activity"
            />
          )}
          {!isLoading && enriched.length === 0 && (
            <DataStatePanel
              state={panelState === 'data' ? 'empty' : panelState}
              endpoint="GET /api/web3-events/user"
              emptyTitle={t('emptyTitle')}
              emptyBody={t('emptyBody')}
              errorDetail={(error as Error | null)?.message}
              icon="activity"
              onRetry={() => refetch()}
              onConnect={app.openConnect}
            />
          )}
        </div>
      </section>
    </div>
  );
}
