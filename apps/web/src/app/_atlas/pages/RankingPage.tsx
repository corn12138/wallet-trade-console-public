'use client';
import { useEffect, useMemo, useState } from 'react';
import Link from 'next/link';
import { useLocale, useTranslations } from 'next-intl';
import { Icon } from '../Icon';
import { PageHeader, TabBar } from '../Common';
import { DataSkeleton, DataStatePanel } from '../DataState';
import { ProductStatusHint } from '../ProductStatus';
import { fmtCompact, fmtCompactDec, fmtPct } from '../data';
import {
  getRankingTokens,
  type AtlasRankingToken,
} from '@/lib/api/atlas';

// Deterministic color pick from symbol so tokens render with stable
// palette tones without needing a column on the DB. The five values
// match the CSS variables RankingPage already references (--c, --b,
// --p, --o, --y).
const PALETTE = ['c', 'b', 'p', 'o', 'y', 'r', 'g'] as const;

function colorForSymbol(sym: string): string {
  if (!sym) return 'y';
  let hash = 0;
  for (let i = 0; i < sym.length; i++) {
    hash = (hash * 31 + sym.charCodeAt(i)) >>> 0;
  }
  return PALETTE[hash % PALETTE.length];
}

// Map DB status (LAUNCHED / PENDING / …) onto the LIVE / PRESALE /
// UPCOMING / ENDED display tags the existing UI styles around.
function toDisplayStatus(dbStatus: string): 'LIVE' | 'PRESALE' | 'UPCOMING' | 'ENDED' {
  switch (dbStatus.toUpperCase()) {
    case 'LAUNCHED':
      return 'LIVE';
    case 'PENDING':
      return 'PRESALE';
    case 'UPCOMING':
      return 'UPCOMING';
    default:
      return 'ENDED';
  }
}

type TabKey = 'gainers' | 'losers' | 'volume' | 'new';

export function RankingPage() {
  const t = useTranslations('rankingAtlas');
  const locale = useLocale();
  const [tab, setTab] = useState<TabKey>('gainers');
  const [liveTokens, setLiveTokens] = useState<AtlasRankingToken[]>([]);
  const [newTokens, setNewTokens] = useState<AtlasRankingToken[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [reloadKey, setReloadKey] = useState(0);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    Promise.all([
      // Live cohort: one fetch covers gainers/losers/volume — tabs
      // sort the same list client-side so switching is instant.
      getRankingTokens({ status: 'LAUNCHED', sortBy: 'volume', limit: 50 }),
      getRankingTokens({ status: 'PENDING', limit: 50 }),
    ])
      .then(([live, upcoming]) => {
        if (cancelled) return;
        setLiveTokens(live);
        setNewTokens(upcoming);
        setError(null);
      })
      .catch((e: Error) => {
        if (!cancelled) setError(e.message);
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [reloadKey]);

  const list = useMemo<AtlasRankingToken[]>(() => {
    if (tab === 'new') return newTokens;
    const live = [...liveTokens];
    if (tab === 'gainers')
      return live.sort((a, b) => (b.priceChange24h ?? 0) - (a.priceChange24h ?? 0));
    if (tab === 'losers')
      return live.sort((a, b) => (a.priceChange24h ?? 0) - (b.priceChange24h ?? 0));
    return live.sort((a, b) => Number(b.volume24h ?? 0) - Number(a.volume24h ?? 0));
  }, [tab, liveTokens, newTokens]);

  return (
    <div className="col gap-24">
      <PageHeader
        eyebrow={t('headerEyebrow')}
        title={t('headerTitle')}
        kicker={t('headerKicker')}
      />

      <ProductStatusHint codes={['NO_SCENARIO_DATA', 'DB_UNAVAILABLE', 'INDEXER_BEHIND_EVENTS', 'INDEXER_NO_CURSOR']} />

      <TabBar
        tabs={[
          { key: 'gainers' as const, label: t('tabGainers') },
          { key: 'losers' as const, label: t('tabLosers') },
          { key: 'volume' as const, label: t('tabVolume') },
          { key: 'new' as const, label: t('tabUpcoming') },
        ]}
        value={tab}
        onChange={setTab}
      />

      {loading && <DataSkeleton shape="rows" count={5} />}
      {error && !loading && (
        <DataStatePanel
          state="error"
          endpoint="GET /api/token"
          emptyTitle={t('errorTitle')}
          emptyBody=""
          errorDetail={error}
          icon="trendUp"
          onRetry={() => setReloadKey((k) => k + 1)}
        />
      )}
      {!loading && !error && list.length === 0 && (
        <DataStatePanel
          state="empty"
          endpoint="GET /api/token?status=LAUNCHED · PENDING"
          emptyTitle={t('emptyTitle')}
          emptyBody={t('emptyBody')}
          icon="trendUp"
          emptyAction={
            <Link href="/create-token" className="btn btn-sm btn-y">
              {t('launchCta')}
            </Link>
          }
        />
      )}

      <section className="block tight">
        <div className="tbl" style={{ boxShadow: 'none', border: '2px solid var(--ink)', borderRadius: 14 }}>
          <div className="tbl-head" style={{ gridTemplateColumns: 'auto 1.6fr 1fr 1fr 1fr 1fr auto' }}>
            <span>{t('colRank')}</span>
            <span>{t('colToken')}</span>
            <span>{t('colStatus')}</span>
            <span>{t('colMarketCap')}</span>
            <span>{t('col24h')}</span>
            <span>{t('colVolume')}</span>
            <span />
          </div>
          {list.map((token, i) => {
            const color = colorForSymbol(token.symbol);
            const status = toDisplayStatus(token.status);
            const statusLabel =
              status === 'LIVE'
                ? t('statusLive')
                : status === 'PRESALE'
                  ? t('statusPresale')
                  : status === 'UPCOMING'
                    ? t('statusUpcoming')
                    : t('statusEnded');
            const chg = token.priceChange24h ?? 0;
            const mcap = token.marketCap;
            const vol = token.volume24h;
            // Token detail lives at /token/[address]; link by the real contract
            // address. When a token has no address yet (not deployed/indexed),
            // render a non-clickable row instead of linking to a symbol the
            // detail route cannot resolve.
            const rowStyle = {
              display: 'grid',
              gridTemplateColumns: 'auto 1.6fr 1fr 1fr 1fr 1fr auto',
              color: 'var(--ink)',
              textDecoration: 'none',
            } as const;
            const rowInner = (
              <>
                <div className="mono" style={{ fontSize: 16, fontWeight: 700 }}>{i + 1}</div>
                <div className="sym">
                  <span
                    className="b"
                    style={{
                      background: `var(--${color})`,
                      color: ['c', 'b', 'p', 'o', 'r'].includes(color) ? '#fff' : 'var(--ink)',
                    }}
                  >
                    {token.symbol[0]}
                  </span>
                  <div>
                    <div>{token.symbol}</div>
                    <div style={{ fontFamily: 'var(--mf)', fontSize: 11, color: 'var(--ink-2)', fontWeight: 400 }}>{token.name}</div>
                  </div>
                </div>
                <div>
                  <span className="pill flat" style={{ padding: '3px 8px', fontSize: 10 }}>{statusLabel}</span>
                </div>
                <div className="mono">{mcap ? `${fmtCompactDec(mcap, locale)} NATIVE` : '—'}</div>
                <div
                  className="mono"
                  style={{ color: chg > 0 ? 'var(--pos)' : chg < 0 ? 'var(--neg)' : 'var(--ink-2)' }}
                >
                  {chg ? fmtPct(chg) : '—'}
                </div>
                <div className="mono">{vol ? `${fmtCompactDec(vol, locale)} NATIVE` : '—'}</div>
                {token.address ? (
                  <Icon name="chevRight" size={16} />
                ) : (
                  <span
                    className="pill flat"
                    style={{ padding: '2px 7px', fontSize: 9 }}
                    data-tip={t('pendingTip')}
                  >
                    {t('pending')}
                  </span>
                )}
              </>
            );
            return token.address ? (
              <Link key={token.id} href={`/token/${token.address}`} className="tbl-row" style={rowStyle}>
                {rowInner}
              </Link>
            ) : (
              <div
                key={token.id}
                className="tbl-row"
                style={{ ...rowStyle, opacity: 0.55, cursor: 'default' }}
                data-tip={t('pendingTip')}
              >
                {rowInner}
              </div>
            );
          })}
        </div>
      </section>
    </div>
  );
}
