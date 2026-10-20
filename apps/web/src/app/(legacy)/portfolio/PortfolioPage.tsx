'use client';

import Link from 'next/link';
import { useAccount, useChainId } from 'wagmi';
import { useTranslations } from 'next-intl';
import { usePortfolioData } from '@/hooks/usePortfolioData';
import { useApp } from '@/app/_atlas/AppContext';
import { LogoCube } from '@/app/_atlas/Icon';
import { MetricCard, PageHeader } from '@/app/_atlas/Common';
import { SourceMeta, deriveDataPanelState } from '@/app/_atlas/DataState';
import { PortfolioCoverage, PortfolioInventory, PortfolioReadNotice, PortfolioValue } from '@/app/_atlas/PortfolioAssetViews';

export default function PortfolioPage() {
  const t = useTranslations('portfolioAtlas');
  const tc = useTranslations('commonAtlas');
  const app = useApp();
  const { address } = useAccount();
  const chainId = useChainId();
  const { isAuthorized, accessState, assetsQuery, summaryQuery, refetchAll } = usePortfolioData({
    targetAddress: address,
    chainId,
    includeSummary: true,
  });

  if (!isAuthorized) {
    const needsSiwe = accessState !== 'disconnected';
    return (
      <div className="col gap-24">
        <PageHeader eyebrow={t('headerEyebrow')} title={t('headerTitle')} kicker={t('headerKicker')} />
        <div className="block" style={{ padding: '40px 20px', textAlign: 'center' }}>
          <LogoCube size={56} />
          <div className="h-display mt-14" style={{ fontSize: 24 }}>{needsSiwe ? t('siweTitle') : t('connectTitle')}</div>
          <p style={{ color: 'var(--ink-2)', maxWidth: 460, margin: '10px auto 0', lineHeight: 1.55 }}>
            {needsSiwe ? t('siweBody') : t('connectBody')}
          </p>
          <button className="btn btn-y mt-22" onClick={app.openConnect} data-testid="portfolio-connect">{tc('connectWallet')}</button>
        </div>
      </div>
    );
  }

  const assetFilters = assetsQuery.data?.filterSummary;
  const assets = assetsQuery.data?.items ?? [];
  const movers = summaryQuery.data?.topMovers ?? [];
  const assetsState = deriveDataPanelState({ isLoading: assetsQuery.isLoading, isError: assetsQuery.isError, rowCount: assets.length });
  const count = (value?: number) => value === undefined ? '—' : String(value);

  return (
    <div className="col gap-24">
      <PageHeader eyebrow={t('headerEyebrow')} title={t('headerTitle')} kicker={t('headerKicker')} />
      <section className="grid-4" style={{ gridTemplateColumns: 'repeat(auto-fit, minmax(min(100%, 180px), 1fr))' }}>
        <div className="block bg-y" style={{ minWidth: 0 }}>
          <div className="eyebrow" style={{ marginBottom: 8 }}>{t('valueSubtotal')}</div>
          <PortfolioValue data={summaryQuery.data} isLoading={summaryQuery.isLoading} />
        </div>
        <MetricCard label={t('metricVisible')} value={count(assetFilters?.visibleCount)} tone="g" />
        <MetricCard label={t('metricHidden')} value={count(assetFilters?.hiddenMatchedCount)} tone="c" />
        <MetricCard label={t('metricSpam')} value={count(assetFilters?.spamFilteredCount)} tone="p" />
      </section>
      <PortfolioReadNotice query={summaryQuery} />
      <SourceMeta
        endpoint="GET /api/portfolio/assets"
        chainId={chainId}
        wallet={address}
        updatedAt={assetsQuery.dataUpdatedAt}
        refetchIntervalMs={60_000}
        rowCount={assets.length}
        state={assetsState}
        isFetching={summaryQuery.isFetching || assetsQuery.isFetching}
        onRefresh={() => void refetchAll()}
      />
      <section className="page-split" style={{ '--split-l': '1.2fr', '--split-r': '0.8fr' } as React.CSSProperties}>
        <div className="block" style={{ minWidth: 0 }}>
          <div className="row between" style={{ marginBottom: 14, flexWrap: 'wrap', gap: 10 }}>
            <div style={{ minWidth: 0 }}>
              <div className="eyebrow">{t('inventory')}</div>
              <div className="mono mt-6" style={{ fontSize: 12, overflowWrap: 'anywhere', color: 'var(--ink-2)' }}>{address}</div>
            </div>
            <Link href="/settings" className="btn btn-xs">{t('openSettings')}</Link>
          </div>
          <PortfolioInventory query={assetsQuery} chainId={chainId} />
        </div>
        <div className="col gap-14" style={{ minWidth: 0 }}>
          <PortfolioCoverage data={assetsQuery.data} />
          <div className="block" style={{ minWidth: 0 }}>
            <div className="eyebrow" style={{ marginBottom: 10 }}>{t('filters')}</div>
            {[
              [t('hiddenConfigured'), count(assetFilters?.hiddenConfiguredCount)],
              [t('metricHidden'), count(assetFilters?.hiddenMatchedCount)],
              [t('filteredTotal'), count(assetFilters?.filteredCount)],
            ].map(([label, value]) => (
              <div key={label} className="meta-row mt-6"><span>{label}</span><b className="mono">{value}</b></div>
            ))}
          </div>
          <div className="block" style={{ minWidth: 0 }}>
            <div className="eyebrow" style={{ marginBottom: 10 }}>{t('topMovers')}</div>
            <div className="col gap-6">
              {movers.map((token) => (
                <div key={token.id} className="row between" style={{ padding: 10, background: 'var(--bg-2)', borderRadius: 10, flexWrap: 'wrap', gap: 8 }}>
                  <div style={{ minWidth: 0, overflowWrap: 'anywhere' }}>
                    <div style={{ fontFamily: 'var(--df)', fontWeight: 800, fontSize: 13 }}>{token.symbol}</div>
                    <div className="mono mt-6" style={{ fontSize: 10, color: 'var(--ink-2)' }}>{token.name}</div>
                  </div>
                  <div className="mono" style={{ fontWeight: 700, color: token.priceChange24h >= 0 ? 'var(--pos)' : 'var(--neg)' }}>
                    {token.priceChange24h >= 0 ? '+' : ''}{token.priceChange24h.toFixed(2)}%
                  </div>
                </div>
              ))}
              {!movers.length && <p className="mono" style={{ fontSize: 12, color: 'var(--ink-2)' }}>{t('emptyMoversTitle')}</p>}
            </div>
          </div>
        </div>
      </section>
    </div>
  );
}
