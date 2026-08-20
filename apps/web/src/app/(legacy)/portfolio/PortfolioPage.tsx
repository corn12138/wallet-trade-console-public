'use client';

import Link from 'next/link';
import { useAccount, useChainId } from 'wagmi';
import { useQuery } from '@tanstack/react-query';
import { useTranslations, useLocale } from 'next-intl';
import { getPortfolioAssets, getPortfolioSummary } from '@/lib/api/atlas';
import { useAuth } from '@/lib/web3';
import { useApp } from '@/app/_atlas/AppContext';
import { LogoCube } from '@/app/_atlas/Icon';
import { MetricCard, PageHeader } from '@/app/_atlas/Common';
import { DataStatePanel, SourceMeta, deriveDataPanelState } from '@/app/_atlas/DataState';

/**
 * /portfolio — Atlas-shell native rebuild of the old dark "surface" page.
 * Same real data flow (SIWE-guarded portfolio summary + assets endpoints,
 * backend-side hidden/spam filtering), but rendered with the Atlas design
 * system and fully localized. States are typed: connect → SIWE → loading →
 * error → empty → data.
 */
export default function PortfolioPage() {
  const t = useTranslations('portfolioAtlas');
  const tc = useTranslations('commonAtlas');
  const locale = useLocale();
  const app = useApp();
  const { isConnected, address } = useAccount();
  const chainId = useChainId();
  const { isAuthenticated } = useAuth();

  const enabled = Boolean(address && isAuthenticated);

  const summaryQuery = useQuery({
    queryKey: ['portfolio-summary', address, chainId],
    queryFn: () => getPortfolioSummary(address, chainId),
    enabled,
    refetchInterval: 60_000,
  });

  const assetsQuery = useQuery({
    queryKey: ['portfolio-assets', address, chainId],
    queryFn: () => getPortfolioAssets(address, chainId),
    enabled,
    refetchInterval: 60_000,
  });

  const formatUsd = (value?: number | null) => {
    if (!value || Number.isNaN(value)) return '$0.00';
    return new Intl.NumberFormat(locale === 'zh' ? 'zh-CN' : 'en-US', {
      style: 'currency',
      currency: 'USD',
      minimumFractionDigits: 2,
      maximumFractionDigits: 2,
    }).format(value);
  };

  if (!isConnected || !isAuthenticated) {
    const needsSiwe = isConnected && !isAuthenticated;
    return (
      <div className="col gap-24">
        <PageHeader
          eyebrow={t('headerEyebrow')}
          title={t('headerTitle')}
          kicker={t('headerKicker')}
        />
        <div className="block" style={{ padding: 60, textAlign: 'center' }}>
          <LogoCube size={56} />
          <div className="h-display" style={{ fontSize: 24, marginTop: 16 }}>
            {needsSiwe ? t('siweTitle') : t('connectTitle')}
          </div>
          <p style={{ marginTop: 10, color: 'var(--ink-2)', maxWidth: 460, margin: '10px auto 0', lineHeight: 1.55 }}>
            {needsSiwe ? t('siweBody') : t('connectBody')}
          </p>
          <button className="btn btn-y mt-22" onClick={app.openConnect} data-testid="portfolio-connect">
            {tc('connectWallet')}
          </button>
        </div>
      </div>
    );
  }

  const assetFilters = assetsQuery.data?.filterSummary ?? summaryQuery.data?.assetFilters;
  const assets = assetsQuery.data?.items ?? [];
  const movers = summaryQuery.data?.topMovers ?? [];

  const assetsState = deriveDataPanelState({
    isLoading: assetsQuery.isLoading,
    isError: assetsQuery.isError,
    rowCount: assets.length,
  });

  return (
    <div className="col gap-24">
      <PageHeader
        eyebrow={t('headerEyebrow')}
        title={t('headerTitle')}
        kicker={t('headerKicker')}
      />

      <section className="grid-4">
        <MetricCard label={t('metricValue')} value={formatUsd(summaryQuery.data?.portfolioValueUsd)} tone="y" />
        <MetricCard label={t('metricVisible')} value={String(assetFilters?.visibleCount ?? 0)} tone="g" />
        <MetricCard label={t('metricHidden')} value={String(assetFilters?.hiddenMatchedCount ?? 0)} tone="c" />
        <MetricCard label={t('metricSpam')} value={String(assetFilters?.spamFilteredCount ?? 0)} tone="p" />
      </section>

      <SourceMeta
        endpoint="GET /api/portfolio/summary · assets"
        chainId={chainId}
        wallet={address}
        updatedAt={Math.max(summaryQuery.dataUpdatedAt, assetsQuery.dataUpdatedAt)}
        refetchIntervalMs={60_000}
        rowCount={assets.length}
        state={assetsState}
        isFetching={summaryQuery.isFetching || assetsQuery.isFetching}
        onRefresh={() => {
          summaryQuery.refetch();
          assetsQuery.refetch();
        }}
      />

      <section className="page-split" style={{ '--split-l': '1.2fr', '--split-r': '0.8fr' } as React.CSSProperties}>
        <div className="block">
          <div className="row between" style={{ marginBottom: 14, flexWrap: 'wrap', gap: 10 }}>
            <div>
              <div className="eyebrow">{t('inventory')}</div>
              <div className="mono" style={{ fontSize: 12, marginTop: 4, wordBreak: 'break-all', color: 'var(--ink-2)' }}>
                {address}
              </div>
            </div>
            <div className="row gap-6">
              <span className="pill flat" style={{ fontSize: 10 }}>
                {isAuthenticated ? t('authReady') : t('authPending')}
              </span>
              <Link href="/settings" className="btn btn-xs">
                {t('openSettings')}
              </Link>
            </div>
          </div>

          <div className="col gap-6">
            {assetsQuery.isLoading &&
              Array.from({ length: 4 }).map((_, index) => (
                <div key={index} className="skel lg" style={{ height: 64 }} />
              ))}
            {!assetsQuery.isLoading &&
              assets.map((asset) => (
                <div
                  key={asset.id}
                  className="row gap-14"
                  style={{
                    padding: 14,
                    background: 'var(--bg-2)',
                    border: '2px solid var(--bg-3)',
                    borderRadius: 14,
                    alignItems: 'flex-start',
                  }}
                >
                  <div style={{ flex: 1, minWidth: 0 }}>
                    <div className="row gap-6" style={{ flexWrap: 'wrap' }}>
                      <span className="h-display" style={{ fontSize: 16 }}>{asset.symbol}</span>
                      <span className="pill flat" style={{ padding: '2px 8px', fontSize: 9 }}>{asset.assetType}</span>
                      <span className="pill flat" style={{ padding: '2px 8px', fontSize: 9 }}>{asset.status}</span>
                    </div>
                    <div style={{ marginTop: 4, fontSize: 13, color: 'var(--ink-2)' }}>{asset.name}</div>
                    <div className="mono" style={{ marginTop: 6, fontSize: 11, color: 'var(--ink-2)' }}>
                      {asset.source} · {t('rawBalance')}: {asset.rawBalance}
                    </div>
                    <div className="mono" style={{ marginTop: 2, fontSize: 11, color: 'var(--ink-2)' }}>
                      {t('updated')}: {new Date(asset.updatedAt).toLocaleString(locale === 'zh' ? 'zh-CN' : 'en-US')}
                    </div>
                  </div>
                  <div style={{ textAlign: 'right' }}>
                    <div className="h-display" style={{ fontSize: 20 }}>
                      {asset.valueUsd > 0 ? formatUsd(asset.valueUsd) : t('trackedOnly')}
                    </div>
                    <div className="mono" style={{ marginTop: 6, fontSize: 10, color: 'var(--ink-2)' }}>
                      {asset.address ? `${asset.address.slice(0, 6)}…${asset.address.slice(-4)}` : '—'}
                    </div>
                  </div>
                </div>
              ))}
            {!assetsQuery.isLoading && assets.length === 0 && (
              <DataStatePanel
                state={assetsState === 'data' ? 'empty' : assetsState}
                endpoint="GET /api/portfolio/assets"
                emptyTitle={t('emptyAssetsTitle')}
                emptyBody={t('emptyAssetsBody')}
                errorDetail={(assetsQuery.error as Error | null)?.message}
                icon="wallet"
                onRetry={() => assetsQuery.refetch()}
                emptyAction={
                  <Link href="/trade" className="btn btn-sm btn-y">
                    {t('headerTitle')}
                  </Link>
                }
              />
            )}
          </div>
        </div>

        <div className="col">
          <div className="block">
            <div className="eyebrow" style={{ marginBottom: 10 }}>{t('filters')}</div>
            {(
              [
                [t('spamLevel'), assetFilters?.spamFilterLevel || 'standard'],
                [t('hiddenConfigured'), String(assetFilters?.hiddenConfiguredCount ?? 0)],
                [t('metricHidden'), String(assetFilters?.hiddenMatchedCount ?? 0)],
                [t('filteredTotal'), String(assetFilters?.filteredCount ?? 0)],
              ] as [string, string][]
            ).map(([label, value]) => (
              <div key={label} className="meta-row" style={{ marginTop: 6 }}>
                <span>{label}</span>
                <b className="mono">{value}</b>
              </div>
            ))}
            <div className="meta-row" style={{ marginTop: 6 }}>
              <span>{t('updated')}</span>
              <b className="mono">
                {summaryQuery.isLoading || assetsQuery.isLoading
                  ? t('syncing')
                  : summaryQuery.data?.dataCompleteness || 'partial-live'}
              </b>
            </div>
          </div>

          <div className="block">
            <div className="eyebrow" style={{ marginBottom: 10 }}>{t('topMovers')}</div>
            <div className="col gap-6">
              {movers.length > 0 ? (
                movers.map((token) => (
                  <div
                    key={token.id}
                    className="row between"
                    style={{ padding: 10, background: 'var(--bg-2)', borderRadius: 10 }}
                  >
                    <div>
                      <div style={{ fontFamily: 'var(--df)', fontWeight: 800, fontSize: 13 }}>{token.symbol}</div>
                      <div className="mono" style={{ fontSize: 10, color: 'var(--ink-2)', marginTop: 2 }}>{token.name}</div>
                    </div>
                    <div
                      className="mono"
                      style={{
                        fontWeight: 700,
                        color: token.priceChange24h >= 0 ? 'var(--pos)' : 'var(--neg)',
                      }}
                    >
                      {token.priceChange24h >= 0 ? '+' : ''}{token.priceChange24h.toFixed(2)}%
                    </div>
                  </div>
                ))
              ) : (
                <div className="mono" style={{ fontSize: 12, color: 'var(--ink-2)', lineHeight: 1.5 }}>
                  <b style={{ fontFamily: 'var(--df)', display: 'block', marginBottom: 4 }}>{t('emptyMoversTitle')}</b>
                  {t('emptyMoversBody')}
                </div>
              )}
            </div>
          </div>
        </div>
      </section>
    </div>
  );
}
