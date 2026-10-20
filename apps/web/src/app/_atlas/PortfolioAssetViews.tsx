'use client';

import Link from 'next/link';
import { useLocale, useTranslations } from 'next-intl';
import type { AtlasPortfolioAsset, AtlasPortfolioAssetsResponse, PortfolioMetadata } from '@/lib/api/portfolio';
import { formatPortfolioQuantity, formatPortfolioUsd, getPortfolioView } from '@/lib/portfolio-display';
import type { PortfolioQueryView } from '@/hooks/usePortfolioData';

const wrap = { minWidth: 0, overflowWrap: 'anywhere' as const };
const quantityKeys = {
  token: 'quantityToken',
  'base-unit': 'quantityBase',
  usd30: 'quantityNotional',
  unknown: 'quantityUnknown',
  unconfirmed: 'quantityUnconfirmed',
} as const;
const sourceKeys = {
  staking: 'sourceStaking',
  perp: 'sourcePerp',
  createdTokens: 'sourceCreatedTokens',
  holdings: 'sourceHoldings',
} as const;

export function PortfolioValue({ data, isLoading = false }: {
  data?: PortfolioMetadata;
  isLoading?: boolean;
}) {
  const t = useTranslations('portfolioAtlas');
  const locale = useLocale();
  const view = getPortfolioView(data);
  return (
    <div style={wrap} data-testid="portfolio-valuation" aria-busy={isLoading}>
      <div className="h-display" style={{ fontSize: 28, lineHeight: 1.15 }}>
        {isLoading ? '—' : formatPortfolioUsd(view.knownSubtotalUsd, locale)}
      </div>
      <div className="mono mt-8" style={{ fontSize: 11, lineHeight: 1.5 }}>
        {isLoading ? t('loadingAssets') : view.knownSubtotalUsd === null ? t('valueUnavailable') : t('valuedCount', { count: view.valuedCount })}
      </div>
      {!isLoading && data && view.unpricedCount > 0 && (
        <div className="mono mt-6" style={{ fontSize: 11 }}>{t('unpricedCount', { count: view.unpricedCount })}</div>
      )}
    </div>
  );
}

export function PortfolioScope({ chainId }: { chainId?: number }) {
  const t = useTranslations('portfolioAtlas');
  return (
    <p className="mono" style={{ ...wrap, fontSize: 12, color: 'var(--ink-2)', lineHeight: 1.6 }} data-testid="portfolio-scope">
      <b>{chainId === undefined ? t('scopeAllChains') : t('scopeChain', { chainId })}</b>
      {' · '}{t('scopeNotice')}
    </p>
  );
}

export function PortfolioReadNotice({ query }: { query: Pick<PortfolioQueryView<PortfolioMetadata>, 'data' | 'isError' | 'error' | 'isStale' | 'dataUpdatedAt' | 'refetch'> }) {
  const t = useTranslations('portfolioAtlas');
  const locale = useLocale();
  const view = getPortfolioView(query.data);
  const reason = query.error?.status === 401 ? 'signinExpired' : query.error?.status === 403 ? 'accessDenied' : 'loadFailed';
  const message = query.isStale ? t('staleNotice')
    : query.isError ? t(reason)
      : !query.data ? null
        : view.isLegacy ? t('legacyNotice')
          : view.sourceState === 'partial' ? t('sourcePartial')
            : view.sourceState === 'unavailable' ? t('sourceUnavailable')
              : view.sourceState === 'unknown' ? t('sourceUnknown') : null;
  if (!message) return null;
  return (
    <div className="block tight" role="status" style={{ ...wrap, borderColor: 'var(--o)', padding: 14 }}>
      <p style={{ fontSize: 13, lineHeight: 1.6 }}>{message}</p>
      {query.isStale && query.dataUpdatedAt > 0 && (
        <p className="mono mt-6" style={{ fontSize: 11 }}>{t('updated')}: {new Date(query.dataUpdatedAt).toLocaleString(locale)}</p>
      )}
      {(query.isError || view.sourceState === 'partial' || view.sourceState === 'unavailable') && (
        <button type="button" className="btn btn-xs mt-10" onClick={() => void query.refetch()}>{t('retryAssets')}</button>
      )}
    </div>
  );
}

function PortfolioAssetRow({ asset }: { asset: AtlasPortfolioAsset }) {
  const t = useTranslations('portfolioAtlas');
  const locale = useLocale();
  const quantity = formatPortfolioQuantity(asset);
  // Creation records have their own ID namespace; that relationship is not evidence of a held balance.
  const createdByOwner = asset.assetType === 'token' && asset.id.startsWith('token:');
  const typeKey = asset.assetType === 'earn' ? 'assetEarn' : asset.assetType === 'perp' ? 'assetPerp' : asset.assetType === 'token' ? 'assetToken' : 'assetOther';
  const reasonKey = asset.valuationReason === 'missing-usd-price' ? 'valuationMissingPrice'
    : asset.valuationReason === 'position-equity-unavailable' ? 'valuationEquityUnavailable'
      : asset.valuationReason === 'holder-valuation-unavailable' ? 'valuationHolderUnavailable' : 'valueUnavailable';
  return (
    <article className="block tight" style={{ ...wrap, padding: 14, background: 'var(--bg-2)', boxShadow: 'none' }} data-testid="portfolio-asset">
      <div className="row between" style={{ flexWrap: 'wrap', gap: 10, ...wrap }}>
        <div style={{ flex: '1 1 150px', ...wrap }}>
          <div className="h-display" style={{ fontSize: 16 }}>{asset.assetType === 'earn' ? asset.name : asset.symbol}</div>
          <div className="mono mt-6" style={{ fontSize: 11 }}>{t(typeKey)} · {t('scopeChain', { chainId: asset.chainId })}</div>
          {createdByOwner && <div className="mono mt-6" style={{ fontSize: 11 }}>{t('createdRelation')}</div>}
        </div>
        <div style={{ ...wrap, maxWidth: '100%' }}>
          <div className="eyebrow">{t(quantityKeys[quantity.label])}</div>
          <div className="mono mt-6" style={{ fontSize: 14 }} data-testid="portfolio-quantity">{quantity.value}</div>
        </div>
      </div>
      <div className="row between mt-14" style={{ flexWrap: 'wrap', gap: 10, ...wrap }}>
        <span className="mono" style={{ fontSize: 11, ...wrap }}>
          {asset.valuationStatus === 'known' ? t('assetValue') : t(reasonKey)}
        </span>
        <b className="mono" data-testid="portfolio-asset-value">{formatPortfolioUsd(asset.valueUsd, locale)}</b>
      </div>
      {asset.address && <div className="mono mt-10" style={{ ...wrap, fontSize: 10, color: 'var(--ink-2)' }}>{asset.address}</div>}
    </article>
  );
}

export function PortfolioInventory({ query, chainId }: { query: PortfolioQueryView<AtlasPortfolioAssetsResponse>; chainId?: number }) {
  const t = useTranslations('portfolioAtlas');
  const view = getPortfolioView(query.data);
  const items = query.data?.items ?? [];
  return (
    <div className="col gap-14" style={wrap}>
      <PortfolioScope chainId={chainId} />
      <PortfolioReadNotice query={query} />
      {query.isLoading && <div className="skel lg" aria-label={t('loadingAssets')} aria-busy="true" style={{ height: 90 }} />}
      {!query.isLoading && items.map((asset) => <PortfolioAssetRow key={asset.id} asset={asset} />)}
      {/* An empty valuation is only a successful empty inventory when every source was read. */}
      {!query.isLoading && !query.isError && view.isEmpty && view.sourceState === 'ready' && (
        <div className="block tight" style={{ padding: 20 }}>
          <p>{t('emptyRecorded')}</p>
          {Boolean(query.data?.filterSummary.filteredCount) && <p className="mono mt-8" style={{ fontSize: 11 }}>{t('emptyFiltered')}</p>}
          <Link href="/trade" className="btn btn-xs mt-14">{t('openTrade')}</Link>
        </div>
      )}
    </div>
  );
}

export function PortfolioCoverage({ data }: { data?: PortfolioMetadata }) {
  const t = useTranslations('portfolioAtlas');
  if (!data?.coverage) return null;
  return (
    <div className="block" style={wrap}>
      <div className="eyebrow">{t('coverageTitle')}</div>
      {Object.entries(sourceKeys).map(([source, key]) => (
        <div className="meta-row mt-10" style={{ flexWrap: 'wrap', gap: 6 }} key={source}>
          <span>{t(key)}</span>
          <b>{t(data.coverage!.sources[source as keyof typeof sourceKeys] === 'ready' ? 'sourceReady' : 'sourceNotReady')}</b>
        </div>
      ))}
      <p className="mono mt-14" style={{ fontSize: 11, lineHeight: 1.6 }}>{t('recordLimits', data.coverage.limits)}</p>
    </div>
  );
}
