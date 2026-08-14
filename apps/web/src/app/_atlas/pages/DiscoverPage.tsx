'use client';
import Link from 'next/link';
import { useQuery } from '@tanstack/react-query';
import { useTranslations } from 'next-intl';
import { getDiscoverHome } from '@/lib/api/atlas';
import { useDisplayChainId } from '@/hooks/useDisplayChainId';
import { Icon } from '../Icon';
import { PageHeader } from '../Common';
import { DataStatePanel, SourceMeta, deriveDataPanelState } from '../DataState';
import { ProductStatusHint } from '../ProductStatus';
import { fmtCompact, fmtPct } from '../data';

const DAPP_PALETTE = ['y', 'o', 'c', 'p', 'g', 'b'];
const TOKEN_PALETTE = ['o', 'p', 'y', 'g', 'c', 'r', 'b'];
function paletteFor(i: number, palette: string[]) {
  return palette[i % palette.length];
}
function riskBadge(r: string) {
  if (r === 'high' || r === 'critical') return { color: 'r', label: r };
  if (r === 'medium' || r === 'med') return { color: 'y', label: 'med' };
  return { color: 'g', label: r || 'low' };
}

export function DiscoverPage() {
  const t = useTranslations('discover');
  const { chainId, isFallback: isChainFallback } = useDisplayChainId();
  const { data, isLoading, isError, error, isFetching, dataUpdatedAt, refetch } = useQuery({
    queryKey: ['atlas-design-discover', chainId],
    queryFn: () => getDiscoverHome(chainId),
    refetchInterval: 60_000,
  });

  const rowCount =
    (data?.curatedDapps?.length ?? 0) + (data?.trendingTokens?.length ?? 0) + (data?.earnProducts?.length ?? 0);

  return (
    <div className="col gap-24">
      <PageHeader
        eyebrow={t('headerEyebrow')}
        title={t('headerTitle')}
        kicker={isChainFallback ? t('headerKickerFallback', { chainId }) : t('headerKicker', { chainId })}
      />

      <SourceMeta
        endpoint="GET /api/discover/home"
        chainId={chainId}
        updatedAt={dataUpdatedAt}
        refetchIntervalMs={60_000}
        rowCount={rowCount}
        state={deriveDataPanelState({ isLoading, isError, rowCount })}
        isFetching={isFetching}
        onRefresh={() => refetch()}
      />

      <ProductStatusHint codes={['NO_SCENARIO_DATA', 'DB_UNAVAILABLE', 'INDEXER_BEHIND_EVENTS', 'INDEXER_NO_CURSOR']} />

      <section>
        <div className="row" style={{ marginBottom: 10 }}>
          <span className="pill flat">{t('curatedPill')}</span>
        </div>
        <div className="grid-3">
        {isLoading && (
          <div className="block">
            <div className="skel lg" />
          </div>
        )}
        {!isLoading &&
          (data?.curatedDapps ?? []).map((d, i) => {
            const c = paletteFor(i, DAPP_PALETTE);
            const inverseInk = ['c', 'b', 'p', 'o'].includes(c);
            const risk = riskBadge(d.risk);
            return (
              <div key={d.id} className="block lift">
                <div className="row gap-14">
                  <span
                    style={{
                      width: 52,
                      height: 52,
                      borderRadius: 14,
                      background: `var(--${c})`,
                      color: inverseInk ? '#fff' : 'var(--ink)',
                      border: '3px solid var(--ink)',
                      boxShadow: '0 3px 0 0 var(--ink)',
                      display: 'grid',
                      placeItems: 'center',
                      fontFamily: 'var(--df)',
                      fontWeight: 900,
                      fontSize: 22,
                    }}
                  >
                    {d.name[0]}
                  </span>
                  <div>
                    <div className="h-display" style={{ fontSize: 18 }}>{d.name}</div>
                    <div className="row gap-6" style={{ marginTop: 4, flexWrap: 'wrap' }}>
                      <span className="pill flat" style={{ fontSize: 10, padding: '3px 8px' }}>
                        {d.category}
                      </span>
                      {d.status === 'roadmap' && (
                        <span
                          className="pill"
                          data-testid="curated-roadmap-badge"
                          style={{ fontSize: 10, padding: '3px 8px', background: 'var(--y)', color: 'var(--ink)' }}
                        >
                          {t('roadmapBadge')}
                        </span>
                      )}
                    </div>
                  </div>
                  <span
                    className="pill"
                    style={{
                      marginLeft: 'auto',
                      background: `var(--${risk.color})`,
                      color: risk.color === 'r' ? '#fff' : 'var(--ink)',
                      padding: '3px 8px',
                      fontSize: 10,
                    }}
                  >
                    {risk.label}
                  </span>
                </div>
                {d.summary && (
                  <div className="mono" style={{ fontSize: 12, color: 'var(--ink-2)', marginTop: 12, lineHeight: 1.5 }}>
                    {d.summary}
                  </div>
                )}
                <div className="row gap-6 mt-14">
                  {/* Roadmap cards are NOT launchable: no "Open" control renders,
                      only an honest roadmap note, so Discover never presents a
                      disabled product as a normal app. Live cards: slug from the
                      discover API is an internal route today (e.g. /swap);
                      external https URLs get a safe new-tab link; anything else
                      renders as unavailable, not a dead button. */}
                  {d.status === 'roadmap' ? (
                    <span
                      className="mono"
                      data-testid="curated-roadmap-note"
                      style={{ fontSize: 11, color: 'var(--ink-2)' }}
                    >
                      {t('roadmapNote')}
                    </span>
                  ) : d.slug?.startsWith('/') ? (
                    <Link href={d.slug} className="btn btn-xs btn-y">
                      {t('open')} <Icon name="arrowRight" size={12} />
                    </Link>
                  ) : d.slug?.startsWith('https://') ? (
                    <a href={d.slug} target="_blank" rel="noopener noreferrer" className="btn btn-xs btn-y">
                      {t('open')} <Icon name="ext" size={12} />
                    </a>
                  ) : (
                    <span className="mono" style={{ fontSize: 11, color: 'var(--ink-2)' }}>
                      {t('noLaunchLink')}
                    </span>
                  )}
                </div>
              </div>
            );
          })}
        {!isLoading && !(data?.curatedDapps?.length) && (
          <DataStatePanel
            state={isError ? 'error' : 'empty'}
            endpoint="GET /api/discover/home"
            emptyTitle={t('emptyCuratedTitle')}
            emptyBody={t('emptyCuratedBody')}
            errorDetail={(error as Error | null)?.message}
            icon="discover"
            onRetry={() => refetch()}
          />
        )}
        </div>
      </section>

      <section>
        <div className="sec-head">
          <div>
            <span className="pill live"><span className="dot" /> {t('trendingPill')}</span>
            <h2 style={{ fontSize: 32, marginTop: 6 }}>{t('heatBoard')}</h2>
          </div>
          <Link href="/ranking" className="btn btn-sm">
            {t('fullRanking')} <Icon name="arrowRight" size={12} />
          </Link>
        </div>
        <div className="grid-3">
          {isLoading && (
            <div className="block">
              <div className="skel lg" />
            </div>
          )}
          {!isLoading &&
            (data?.trendingTokens ?? []).slice(0, 6).map((token, i) => {
              const c = paletteFor(i, TOKEN_PALETTE);
              const inverseInk = ['c', 'b', 'p', 'o', 'r'].includes(c);
              return (
                <Link
                  key={token.id}
                  href={`/token/${token.address ?? token.symbol.toLowerCase()}`}
                  className="block lift"
                  style={{ display: 'block' }}
                >
                  <div className="row between">
                    <span className="row gap-10">
                      <span
                        style={{
                          width: 36,
                          height: 36,
                          borderRadius: 999,
                          background: `var(--${c})`,
                          color: inverseInk ? '#fff' : 'var(--ink)',
                          border: '2px solid var(--ink)',
                          display: 'grid',
                          placeItems: 'center',
                          fontFamily: 'var(--df)',
                          fontWeight: 900,
                          fontSize: 14,
                        }}
                      >
                        {token.symbol[0]}
                      </span>
                      <span>
                        <div className="h-display" style={{ fontSize: 18 }}>${token.symbol}</div>
                        <div className="mono" style={{ fontSize: 11, color: 'var(--ink-2)' }}>{token.name}</div>
                      </span>
                    </span>
                    {token.badges?.[0] && (
                      <span className="pill flat" style={{ padding: '3px 8px', fontSize: 10, textTransform: 'uppercase' }}>
                        {token.badges[0]}
                      </span>
                    )}
                  </div>
                  <div className="row between mt-14">
                    <div>
                      <div className="eyebrow">{t('marketCap')}</div>
                      <div className="mono" style={{ fontSize: 18, fontWeight: 700, marginTop: 4 }}>
                        {fmtCompact(token.marketCap || 0)} NATIVE
                      </div>
                    </div>
                    <div style={{ textAlign: 'right' }}>
                      <div className="eyebrow">{t('h24')}</div>
                      <div
                        className="mono"
                        style={{
                          fontSize: 18,
                          fontWeight: 700,
                          marginTop: 4,
                          color: token.priceChange24h > 0 ? 'var(--pos)' : token.priceChange24h < 0 ? 'var(--neg)' : 'var(--ink-2)',
                        }}
                      >
                        {token.priceChange24h ? fmtPct(token.priceChange24h) : '—'}
                      </div>
                    </div>
                  </div>
                </Link>
              );
            })}
          {!isLoading && !(data?.trendingTokens?.length) && (
            <DataStatePanel
              state={isError ? 'error' : 'empty'}
              endpoint="GET /api/discover/home · trending"
              emptyTitle={t('emptyTrendingTitle')}
              emptyBody={t('emptyTrendingBody')}
              errorDetail={(error as Error | null)?.message}
              icon="fire"
              onRetry={() => refetch()}
              emptyAction={
                <Link href="/create-token" className="btn btn-sm btn-y">
                  {t('launchToken')}
                </Link>
              }
            />
          )}
        </div>
      </section>

      {(data?.earnProducts?.length ?? 0) > 0 && (
        <section>
          <div className="sec-head">
            <div>
              <span className="pill">{t('liveEarnPill')}</span>
              <h2 style={{ fontSize: 28, marginTop: 6 }}>{t('topYield')}</h2>
            </div>
            <Link href="/earn" className="btn btn-sm">
              {t('allVaults')} <Icon name="arrowRight" size={12} />
            </Link>
          </div>
          <div className="grid-4">
            {data!.earnProducts.slice(0, 4).map((p, i) => {
              const c = paletteFor(i, DAPP_PALETTE);
              const inverseInk = ['c', 'b', 'p', 'o'].includes(c);
              return (
                <div key={p.id} className={'block bg-' + c} style={{ padding: 18, color: inverseInk ? '#fff' : 'var(--ink)' }}>
                  <div className="eyebrow" style={{ opacity: 0.85 }}>{p.type}</div>
                  <div className="h-display" style={{ fontSize: 22, marginTop: 6 }}>{p.name}</div>
                  <div className="row between mt-14">
                    <div>
                      <div className="eyebrow" style={{ opacity: 0.7 }}>{t('apy')}</div>
                      <div className="mono" style={{ fontSize: 22, fontWeight: 800 }}>{p.apy.toFixed(1)}%</div>
                    </div>
                    <div style={{ textAlign: 'right' }}>
                      <div className="eyebrow" style={{ opacity: 0.7 }}>{t('tvl')}</div>
                      <div className="mono" style={{ fontSize: 14, fontWeight: 700 }}>${fmtCompact(p.tvl || 0)}</div>
                    </div>
                  </div>
                </div>
              );
            })}
          </div>
        </section>
      )}
    </div>
  );
}
