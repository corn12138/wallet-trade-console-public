'use client';
import { useState } from 'react';
import { useAccount, useChainId } from 'wagmi';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useTranslations } from 'next-intl';
import {
  createWatchOnlyWallet,
  getWalletManager,
  type AtlasWalletRecord,
} from '@/lib/api/atlas';
import { buildAddressExplorerUrl } from '@/lib/web3/explorer';
import { useApp } from '../AppContext';
import { Icon, LogoCube } from '../Icon';
import { BlockBtn, MetricCard, PageHeader } from '../Common';
import { formatDiagnosticValue } from '../diagnostics';
import { DataSkeleton, DataStatePanel, SourceMeta, deriveDataPanelState } from '../DataState';
import { paletteFor, shortAddr } from './assetUtils';

function AddWalletModal({
  ownerAddress,
  chainId,
  close,
  onCreated,
}: {
  ownerAddress: string;
  chainId: number;
  close: () => void;
  onCreated: () => void;
}) {
  const app = useApp();
  const t = useTranslations('walletsPage');
  const [addr, setAddr] = useState('');
  const [name, setName] = useState('');

  const m = useMutation({
    mutationFn: () =>
      createWatchOnlyWallet({
        address: addr,
        chainId,
        ownerAddress,
        displayName: name || undefined,
      }),
    onSuccess: (rec) => {
      app.toast(t('toastWatching', { name: rec.profile?.displayName || rec.address.slice(0, 6) }), 'ok');
      onCreated();
      close();
    },
    onError: (e: any) => app.toast(e?.message || t('toastAddFailed'), 'err'),
  });

  return (
    <div className="modal-bg" onClick={(e) => e.target === e.currentTarget && close()}>
      <div className="modal-box">
        <div className="modal-head">
          <h3>{t('modalTitle')}</h3>
          <button className="modal-x" onClick={close}>
            <Icon name="close" size={16} />
          </button>
        </div>
        <div className="field">
          <div className="l"><span>{t('modalAddress')}</span></div>
          <input
            value={addr}
            onChange={(e) => setAddr(e.target.value)}
            placeholder="0x…"
            style={{ fontFamily: 'var(--mf)', fontSize: 14 }}
            autoFocus
          />
        </div>
        <div className="field mt-14">
          <div className="l"><span>{t('modalLabel')}</span></div>
          <input value={name} onChange={(e) => setName(e.target.value)} placeholder={t('modalLabelPlaceholder')} />
        </div>
        <button
          className="btn btn-y mt-14"
          style={{ width: '100%' }}
          disabled={!addr || m.isPending}
          onClick={() => m.mutate()}
        >
          {m.isPending ? (
            <>
              <span className="spinner" /> {t('modalAdding')}
            </>
          ) : (
            t('modalSubmit')
          )}
        </button>
      </div>
    </div>
  );
}

export function WalletsPage() {
  const app = useApp();
  const t = useTranslations('walletsPage');
  const tc = useTranslations('commonAtlas');
  const tDiag = useTranslations('diagnostics');
  const { address } = useAccount();
  const chainId = useChainId();
  const qc = useQueryClient();

  const [addOpen, setAddOpen] = useState(false);

  const { data, isLoading, isError, error, isFetching, dataUpdatedAt, refetch } = useQuery({
    queryKey: ['atlas-design-wallets', address, chainId],
    queryFn: () => getWalletManager(address, chainId),
    enabled: Boolean(address),
    refetchInterval: 60_000,
  });

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

  const wallets: AtlasWalletRecord[] = data?.wallets ?? [];
  const summary = data?.summary;

  return (
    <div className="col gap-24">
      <PageHeader
        eyebrow={t('headerEyebrow')}
        title={t('headerTitle')}
        kicker={t('headerKicker')}
        actions={
          <BlockBtn sm icon="plus" tone="y" onClick={() => setAddOpen(true)}>
            {t('addWallet')}
          </BlockBtn>
        }
      />

      <section className="grid-4">
        <MetricCard label={t('metricTotal')} value={String(summary?.totalWallets ?? wallets.length)} sub={t('metricTotalSub')} tone="y" />
        <MetricCard label={t('metricLinked')} value={String(summary?.connectedWallets ?? 0)} sub={t('metricLinkedSub')} tone="g" />
        <MetricCard label={t('metricWatch')} value={String(summary?.watchOnlyWallets ?? 0)} sub={t('metricWatchSub')} tone="c" />
        <MetricCard label={t('metricGroups')} value={String(summary?.groupedWallets ?? 0)} sub={t('metricGroupsSub', { count: data?.groups?.length ?? 0 })} tone="p" />
      </section>

      <SourceMeta
        endpoint="GET /api/wallets/manager"
        chainId={chainId}
        wallet={address}
        updatedAt={dataUpdatedAt}
        refetchIntervalMs={60_000}
        rowCount={wallets.length}
        state={deriveDataPanelState({ isLoading, isError, rowCount: wallets.length })}
        isFetching={isFetching}
        onRefresh={() => refetch()}
      />

      <section className="grid-3">
        {isLoading && <DataSkeleton shape="rows" count={3} />}
        {!isLoading &&
          wallets.map((w, i) => {
            const color = paletteFor(i);
            const inverseInk = ['c', 'b', 'p', 'o', 'r'].includes(color);
            const linked = w.authState === 'authenticated' || w.walletType === 'linked';
            return (
              <div key={w.id} className="block">
                <div className="row between">
                  <span className={'pill ' + (linked ? 'live' : '')}>
                    {linked ? <span className="dot" /> : null}
                    {linked ? t('linked') : t('watching')}
                  </span>
                  <span
                    style={{
                      width: 36,
                      height: 36,
                      borderRadius: 999,
                      background: `var(--${color})`,
                      color: inverseInk ? '#fff' : 'var(--ink)',
                      border: '2px solid var(--ink)',
                      display: 'grid',
                      placeItems: 'center',
                      fontFamily: 'var(--df)',
                      fontWeight: 900,
                      fontSize: 14,
                    }}
                  >
                    {(w.profile?.displayName || w.address)[2]?.toUpperCase() ?? '?'}
                  </span>
                </div>
                <div className="h-display mt-14" style={{ fontSize: 20 }}>
                  {w.profile?.displayName || t('walletFallbackName', { address: shortAddr(w.address) })}
                </div>
                <div className="mono" style={{ fontSize: 12, color: 'var(--ink-2)', marginTop: 4 }}>
                  {t('walletMeta', { address: shortAddr(w.address), chainId: w.chainId })}
                </div>
                <div className="row gap-6 mt-14">
                  <div style={{ flex: 1 }}>
                    <div className="eyebrow">{t('typeLabel')}</div>
                    <div className="h-display" style={{ fontSize: 16, marginTop: 4, textTransform: 'capitalize' }}>
                      {formatDiagnosticValue(tDiag, w.walletType)}
                    </div>
                  </div>
                  <div style={{ flex: 1 }}>
                    <div className="eyebrow">{t('lastLogin')}</div>
                    <div className="mono" style={{ fontSize: 12, marginTop: 4, color: 'var(--ink-2)' }}>
                      {w.lastLoginAt ? new Date(w.lastLoginAt).toLocaleDateString() : '—'}
                    </div>
                  </div>
                </div>
                <div className="row gap-6 mt-14">
                  <button
                    className="btn btn-xs"
                    onClick={() => {
                      navigator.clipboard?.writeText(w.address);
                      app.toast(t('toastCopied'));
                    }}
                  >
                    <Icon name="copy" size={12} /> {t('copy')}
                  </button>
                  {(() => {
                    const explorerUrl = buildAddressExplorerUrl(w.address, w.chainId);
                    return explorerUrl ? (
                      <a
                        className="btn btn-xs"
                        href={explorerUrl}
                        target="_blank"
                        rel="noopener noreferrer"
                      >
                        <Icon name="ext" size={12} /> {t('explorer')}
                      </a>
                    ) : null;
                  })()}
                </div>
              </div>
            );
          })}
        {!isLoading && !wallets.length && (
          <DataStatePanel
            state={isError ? 'error' : 'empty'}
            endpoint="GET /api/wallets/manager"
            emptyTitle={t('emptyTitle')}
            emptyBody={t('emptyBody')}
            errorDetail={(error as Error | null)?.message}
            icon="wallet"
            onRetry={() => refetch()}
            emptyAction={
              <button className="btn btn-sm btn-y" onClick={() => setAddOpen(true)}>
                {t('addWallet')}
              </button>
            }
          />
        )}
      </section>

      {addOpen && (
        <AddWalletModal
          ownerAddress={address}
          chainId={chainId}
          close={() => setAddOpen(false)}
          onCreated={() => {
            qc.invalidateQueries({ queryKey: ['atlas-design-wallets'] });
            refetch();
          }}
        />
      )}
    </div>
  );
}
