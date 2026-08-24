'use client';
import { useMemo, useState } from 'react';
import Link from 'next/link';
import { useTranslations } from 'next-intl';
import { useChainId } from 'wagmi';
import { useQuery } from '@tanstack/react-query';
import { getTradingMarketsByChain, getTradingStats } from '@/lib/api';
import { getMarketsSnapshot } from '@/lib/api/atlas';
import { resolveTradingChainId } from '@/lib/web3/trading-chain';
import { Icon } from '../Icon';
import { BlockBtn, Empty, MetricCard, PageHeader, TabBar } from '../Common';
import { fmtPct } from '../data';
import type { SortKey } from './tradeTypes';
import { compactFromString, percentFromString } from './tradeUtils';

export function MarketsPage() {
  const t = useTranslations('marketsAtlas');
  const walletChainId = useChainId();
  const chainId = resolveTradingChainId(walletChainId);
  const [q, setQ] = useState('');
  const [sort, setSort] = useState<SortKey>('vol');

  const {
    data: markets = [],
    isLoading,
    isError,
    refetch,
  } = useQuery({
    queryKey: ['atlas-design-markets', chainId],
    queryFn: () => getTradingMarketsByChain(chainId),
    refetchInterval: 30_000,
  });

  const { data: stats } = useQuery({
    queryKey: ['atlas-design-trading-stats', chainId],
    queryFn: () => getTradingStats(chainId),
    refetchInterval: 30_000,
  });

  const { data: snapshot } = useQuery({
    queryKey: ['atlas-design-markets-snapshot', chainId],
    queryFn: () => getMarketsSnapshot(chainId),
    refetchInterval: 60_000,
  });

  const enriched = useMemo(() => {
    return markets.map((m) => {
      const longOI = Number(m.longOpenInterest || 0);
      const shortOI = Number(m.shortOpenInterest || 0);
      return {
        ...m,
        vol: Number(m.volume24h || 0),
        oi: longOI + shortOI,
        fund: percentFromString(m.fundingRate),
      };
    });
  }, [markets]);

  const filtered = useMemo(() => {
    let list = enriched.filter((m) => m.symbol.toLowerCase().includes(q.toLowerCase()));
    list = [...list].sort((a, b) =>
      sort === 'vol'
        ? b.vol - a.vol
        : sort === 'oi'
        ? b.oi - a.oi
        : sort === 'fund'
        ? b.fund - a.fund
        : a.symbol.localeCompare(b.symbol),
    );
    return list;
  }, [enriched, q, sort]);

  const avgFunding = enriched.length > 0 ? enriched.reduce((s, m) => s + m.fund, 0) / enriched.length : 0;

  return (
    <div className="col gap-24">
      <PageHeader
        eyebrow={t('headerEyebrow')}
        title={t('headerTitle')}
        kicker={t('headerKicker')}
        actions={
          <>
            <BlockBtn sm icon="refresh" tip={t('refreshTip')} onClick={() => refetch()}>
              {t('refresh')}
            </BlockBtn>
            <Link
              href="/trade"
              className="btn btn-sm btn-y"
              style={{ display: 'inline-flex' }}
            >
              <Icon name="trade" size={14} /> {t('openTerminal')}
            </Link>
          </>
        }
      />

      <section className="grid-4">
        <MetricCard
          label={t('metricLive')}
          value={String(snapshot?.summary.activeMarkets ?? enriched.length)}
          sub={t('metricLiveSub', { chainId })}
          tone="y"
        />
        <MetricCard
          label={t('metricVolume')}
          value={
            stats?.totalVolume ?? snapshot?.summary.totalVolume
              ? `$${compactFromString(stats?.totalVolume ?? snapshot?.summary.totalVolume)}`
              : '—'
          }
          sub={t('metricVolumeSub')}
          tone="c"
        />
        <MetricCard
          label={t('metricOI')}
          value={
            stats?.totalOpenInterest ?? snapshot?.summary.totalOpenInterest
              ? `$${compactFromString(stats?.totalOpenInterest ?? snapshot?.summary.totalOpenInterest)}`
              : '—'
          }
          sub={t('metricOISub')}
          tone="o"
        />
        <MetricCard label={t('metricFunding')} value={fmtPct(avgFunding)} sub={t('metricFundingSub', { count: enriched.length || 0 })} tone="p" />
      </section>

      <section className="block tight">
        <div className="row between" style={{ padding: '6px 6px 14px' }}>
          <div className="input-row" style={{ minWidth: 280, padding: '8px 12px' }}>
            <Icon name="search" size={16} />
            <input placeholder={t('searchPlaceholder')} value={q} onChange={(e) => setQ(e.target.value)} />
          </div>
          <div className="row gap-6">
            <span className="eyebrow">{t('sortBy')}</span>
            <TabBar
              tabs={[
                { key: 'vol' as SortKey, label: t('sortVolume') },
                { key: 'oi' as SortKey, label: t('sortOI') },
                { key: 'fund' as SortKey, label: t('sortFunding') },
                { key: 'sym' as SortKey, label: t('sortAz') },
              ]}
              value={sort}
              onChange={setSort}
            />
          </div>
        </div>

        <div className="tbl" style={{ boxShadow: 'none', border: '2px solid var(--ink)', borderRadius: 14 }}>
          <div className="tbl-head" style={{ gridTemplateColumns: '1.4fr 1fr 1fr 1fr 1fr auto' }}>
            <span>{t('colMarket')}</span>
            <span>{t('colFunding')}</span>
            <span>{t('colVolume')}</span>
            <span>{t('colLongOI')}</span>
            <span>{t('colShortOI')}</span>
            <span />
          </div>
          {isLoading && (
            <div className="tbl-row" style={{ gridTemplateColumns: '1fr' }}>
              <div className="skel lg" />
            </div>
          )}
          {!isLoading &&
            filtered.map((m) => (
              <div
                key={m.symbol}
                className="tbl-row"
                style={{ gridTemplateColumns: '1.4fr 1fr 1fr 1fr 1fr auto' }}
              >
                <div className="sym">
                  <span className="b">{m.symbol[0]}</span>
                  {m.symbol}
                  <span className="pill flat" style={{ padding: '3px 8px', fontSize: 10 }}>
                    {t('perp')}
                  </span>
                </div>
                <div className="mono" style={{ color: m.fund >= 0 ? 'var(--pos)' : 'var(--neg)' }}>
                  {fmtPct(m.fund)}
                </div>
                <div className="mono">${compactFromString(m.volume24h)}</div>
                <div className="mono">${compactFromString(m.longOpenInterest)}</div>
                <div className="mono">${compactFromString(m.shortOpenInterest)}</div>
                <Link href={`/trade?symbol=${encodeURIComponent(m.symbol)}`} className="btn btn-xs btn-y">
                  {t('trade')}
                </Link>
              </div>
            ))}
          {!isLoading && !filtered.length && (
            <Empty body={isError ? t('emptyError') : t('emptySearch', { query: q })} />
          )}
        </div>
      </section>

      {snapshot && snapshot.topMovers.length > 0 && (
        <section>
          <div className="sec-head">
            <h2 style={{ fontSize: 32 }}>{t('topMovers')}</h2>
          </div>
          <div className="grid-4">
            {snapshot.topMovers.slice(0, 8).map((m) => (
              <div key={m.symbol} className="block tight" style={{ padding: 16 }}>
                <div className="eyebrow">{m.symbol}</div>
                <div
                  className="h-display mono"
                  style={{
                    fontSize: 22,
                    marginTop: 4,
                    color: m.change24h >= 0 ? 'var(--pos)' : 'var(--neg)',
                  }}
                >
                  {fmtPct(m.change24h)}
                </div>
                {m.address && (
                  <div
                    className="mono"
                    style={{ fontSize: 11, color: 'var(--ink-2)', marginTop: 6, wordBreak: 'break-all' }}
                  >
                    {m.address.slice(0, 6)}…{m.address.slice(-4)}
                  </div>
                )}
              </div>
            ))}
          </div>
        </section>
      )}
    </div>
  );
}
