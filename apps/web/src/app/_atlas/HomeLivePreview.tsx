'use client';
import { useMemo, useState } from 'react';
import Link from 'next/link';
import { useQuery } from '@tanstack/react-query';
import { useTranslations } from 'next-intl';
import { getTradingMarketsByChain, getTradingStats, type TradingCandleResolution } from '@/lib/api';
import { formatTradingChartVolume } from '@/components/trading-chart.utils';
import { useDisplayChainId } from '@/hooks/useDisplayChainId';
import { Icon } from './Icon';
import { TradeCandleChart } from './pages/TradeCandleChart';
import { DataStatePanel } from './DataState';

/**
 * Home terminal preview — READ-ONLY and fully real. It renders the same live
 * candle chart component as /trade (TradeCandleChart → GET /api/trading/candles)
 * for the top market by 24h volume, plus a stat strip sourced from
 * GET /api/trading/markets and GET /api/trading/stats. There is no Math.random,
 * no static ATLAS orderbook, and no hardcoded 67,421.55 / $2.41B sample values:
 * if the trading service has no markets, an honest empty state renders instead.
 */
export function HomeLivePreview() {
  const t = useTranslations('marketing');
  const { chainId } = useDisplayChainId();
  const [resolution, setResolution] = useState<TradingCandleResolution>('15m');

  const marketsQ = useQuery({
    queryKey: ['trading-markets', chainId],
    queryFn: () => getTradingMarketsByChain(chainId),
    staleTime: 30_000,
    refetchInterval: 30_000,
  });

  const statsQ = useQuery({
    queryKey: ['trading-stats', chainId],
    queryFn: () => getTradingStats(chainId),
    staleTime: 30_000,
    refetchInterval: 30_000,
  });

  const markets = marketsQ.data ?? [];
  const topMarket = useMemo(() => {
    if (markets.length === 0) return null;
    return [...markets].sort((a, b) => Number(b.volume24h) - Number(a.volume24h))[0];
  }, [markets]);

  const networkLabel = chainId === 11155111 ? 'Sepolia' : `chain ${chainId}`;

  if (!marketsQ.isLoading && markets.length === 0) {
    return (
      <div className="terminal" data-testid="home-live-preview">
        <div className="term-bar">
          <div className="lights"><b /><b /><b /></div>
          <div className="ttl">{t('livePreviewPill')}</div>
          <div className="right">
            <span className="status-dot" /> {networkLabel}
          </div>
        </div>
        <div style={{ padding: 20 }}>
          <DataStatePanel
            state={marketsQ.isError ? 'error' : 'empty'}
            endpoint="GET /api/trading/markets"
            emptyTitle={t('liveMarketsEmptyTitle')}
            emptyBody={t('liveMarketsEmptyBody')}
            errorDetail={(marketsQ.error as Error | null)?.message}
            icon="markets"
            onRetry={() => marketsQ.refetch()}
          />
        </div>
      </div>
    );
  }

  return (
    <div className="terminal" data-testid="home-live-preview">
      <div className="term-bar">
        <div className="lights"><b /><b /><b /></div>
        <div className="ttl">{topMarket ? topMarket.symbol : t('liveMarketsLoading')}</div>
        <div className="right">
          <span className="status-dot ok" /> {networkLabel}
        </div>
      </div>

      {/* Real stat strip from the trading service — no sample numbers. */}
      <div
        className="row"
        style={{ padding: '12px 16px', gap: 18, flexWrap: 'wrap', borderBottom: '2px solid var(--ink)' }}
        data-testid="home-live-stats"
      >
        <PreviewStat label={t('liveMarketsLabel')} value={String(markets.length)} />
        <PreviewStat
          label={t('live24hVolume')}
          value={statsQ.data ? `$${formatTradingChartVolume(statsQ.data.totalVolume)}` : '—'}
        />
        <PreviewStat
          label={t('liveOpenInterest')}
          value={statsQ.data ? `$${formatTradingChartVolume(statsQ.data.totalOpenInterest)}` : '—'}
        />
        <PreviewStat label={t('liveNetwork')} value={networkLabel} />
        {topMarket && (
          <PreviewStat label={t('liveFunding')} value={topMarket.fundingRate} mono />
        )}
      </div>

      <div style={{ padding: 16, minHeight: 360, display: 'flex' }}>
        <TradeCandleChart
          symbol={topMarket?.symbol}
          chainId={topMarket?.chainId ?? chainId}
          resolution={resolution}
          onResolutionChange={setResolution}
        />
      </div>

      <div
        className="row between"
        style={{ padding: '10px 16px', borderTop: '2px solid var(--ink)', flexWrap: 'wrap', gap: 8 }}
      >
        <span className="mono" style={{ fontSize: 11, color: 'var(--ink-2)' }}>{t('livePreviewNote')}</span>
        <Link href="/trade" className="btn btn-xs btn-y">
          <Icon name="trade" size={12} /> {t('openTerminal')}
        </Link>
      </div>
    </div>
  );
}

function PreviewStat({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div>
      <div className="eyebrow" style={{ opacity: 0.7 }}>{label}</div>
      <div className={mono ? 'mono' : 'h-display'} style={{ fontSize: mono ? 13 : 18, marginTop: 2 }}>
        {value}
      </div>
    </div>
  );
}
