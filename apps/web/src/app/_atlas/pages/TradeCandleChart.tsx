'use client';

import {
  CandlestickSeries,
  ColorType,
  createChart,
  type IChartApi,
  type ISeriesApi,
  type MouseEventParams,
  type Time,
} from 'lightweight-charts';
import { useEffect, useMemo, useRef, useState } from 'react';
import { useTranslations, useLocale } from 'next-intl';
import type { PriceSource, TradingCandleApi, TradingCandleResolution } from '@/lib/api';
import { useTradingCandles } from '@/hooks/useTradingCandles';
import { usePriceCandles } from '@/hooks/usePriceCandles';
import {
  TRADING_CHART_RESOLUTIONS,
  formatPriceCandleVolume,
  formatTradingChartPrice,
  formatTradingChartVolume,
  mapPriceCandlesToChartData,
  mapTradingCandlesToChartData,
} from '@/components/trading-chart.utils';
import { useNow } from '../DataState';
import { Icon } from '../Icon';

type TradeCandleChartProps = {
  symbol?: string;
  chainId?: number;
  resolution: TradingCandleResolution;
  onResolutionChange: (resolution: TradingCandleResolution) => void;
};

/**
 * Which real series the chart is showing. These are three genuinely different
 * things and the chart never blends them:
 *
 *   onchain — this product's own perp trades (GET /api/trading/candles).
 *             Authoritative, but empty until trades actually execute here.
 *   market  — real OHLCV from a public spot venue (GET /api/prices/candles).
 *             Dense and volume-bearing, but it is reference data from another
 *             venue, NOT activity on this product.
 *   oracle  — Chainlink aggregator rounds, read on-chain. Trust-minimized but
 *             sparse, and it carries no traded volume at all.
 */
type ChartSource = 'onchain' | PriceSource;

type HoverCandle = {
  time: number; // seconds
  open: number;
  high: number;
  low: number;
  close: number;
  /** Pre-formatted for the active source, or null when the source has none. */
  volume: string | null;
  /** Trade count exists only for the on-chain source. */
  trades: number | null;
  x: number;
  y: number;
};

const SOURCE_ORDER: ChartSource[] = ['onchain', 'market', 'oracle'];

/**
 * Live candlestick chart for the /trade terminal.
 *
 * Every series it draws is real and stored server-side — there is no synthetic
 * or interpolated data anywhere in this component. Because this product runs on
 * a testnet where its own trade flow is near-zero, the on-chain series is
 * frequently empty; rather than showing a permanently blank chart, the chart
 * falls back ONCE to the reference market series and says so explicitly, both
 * in a banner and in the status bar. The user can switch sources by hand at any
 * time, and a manual choice is never overridden.
 */
export function TradeCandleChart({
  symbol,
  chainId,
  resolution,
  onResolutionChange,
}: TradeCandleChartProps) {
  const t = useTranslations('trade.chart');
  const locale = useLocale();
  const containerRef = useRef<HTMLDivElement>(null);
  const chartRef = useRef<IChartApi | null>(null);
  const seriesRef = useRef<ISeriesApi<'Candlestick'> | null>(null);
  const candleByTimeRef = useRef<Map<number, { volume: string | null; trades: number | null }>>(
    new Map(),
  );
  const [hover, setHover] = useState<HoverCandle | null>(null);

  const [source, setSource] = useState<ChartSource>('onchain');
  // Once the user picks a source we stop auto-switching: silently moving the
  // chart out from under a deliberate choice would be worse than an empty pane.
  const [sourcePinned, setSourcePinned] = useState(false);
  const [autoFellBack, setAutoFellBack] = useState(false);

  const onchain = useTradingCandles({ symbol, chainId, resolution, limit: 200 });
  const reference = usePriceCandles({
    symbol,
    resolution,
    limit: 200,
    source: source === 'oracle' ? 'oracle' : 'market',
    // Only fetch reference data when it is actually needed: while it is the
    // active source, or to decide whether an automatic fallback is possible.
    enabled: source !== 'onchain' || (!sourcePinned && !onchain.isLoading),
  });

  // Auto-fallback: the on-chain endpoint answered normally with zero rows, and
  // real reference candles exist. Runs at most once, and never after a manual pick.
  useEffect(() => {
    if (sourcePinned || source !== 'onchain') return;
    if (onchain.isLoading || onchain.error) return;
    if (onchain.candles.length > 0) return;
    if (reference.seriesStatus !== 'ok' || reference.candles.length === 0) return;
    setSource('market');
    setAutoFellBack(true);
  }, [
    sourcePinned,
    source,
    onchain.isLoading,
    onchain.error,
    onchain.candles.length,
    reference.seriesStatus,
    reference.candles.length,
  ]);

  const selectSource = (next: ChartSource) => {
    setSourcePinned(true);
    setAutoFellBack(false);
    setSource(next);
  };

  const isOnchain = source === 'onchain';
  const active = isOnchain ? onchain : reference;
  const { error, isLoading, isFetching, dataUpdatedAt, refetch } = active;

  const refetchIntervalMs = resolution === '1m' ? 5_000 : 30_000;

  const chartData = useMemo(
    () =>
      isOnchain
        ? mapTradingCandlesToChartData(onchain.candles)
        : mapPriceCandlesToChartData(reference.candles),
    [isOnchain, onchain.candles, reference.candles],
  );

  // Per-bucket extras the candlestick series does not carry (volume, and a
  // trade count where the source has one). Derived with useMemo — NOT read
  // from a ref during render — so the legend sees the current dataset on the
  // same pass that renders it.
  const extrasByTime = useMemo(() => {
    const map = new Map<number, { volume: string | null; trades: number | null }>();
    if (isOnchain) {
      for (const candle of onchain.candles as TradingCandleApi[]) {
        map.set(Math.floor(candle.timestamp / 1000), {
          volume: formatTradingChartVolume(candle.volume),
          trades: candle.trades,
        });
      }
    } else {
      for (const candle of reference.candles) {
        map.set(Math.floor(candle.timestamp / 1000), {
          // An oracle reports a price, not traded size. Showing "0" would read
          // as "no trades"; null renders as "—", which is the honest answer.
          volume: source === 'oracle' ? null : formatPriceCandleVolume(candle.volume),
          trades: null,
        });
      }
    }
    return map;
  }, [isOnchain, source, onchain.candles, reference.candles]);

  // Mirror it into a ref so the crosshair callback — subscribed once on mount —
  // always reads the latest map without needing to re-subscribe.
  useEffect(() => {
    candleByTimeRef.current = extrasByTime;
  }, [extrasByTime]);

  useEffect(() => {
    if (!containerRef.current) return;

    const chart = createChart(containerRef.current, {
      layout: {
        background: { type: ColorType.Solid, color: 'transparent' },
        textColor: 'rgba(26, 18, 7, 0.72)',
        attributionLogo: false,
      },
      grid: {
        vertLines: { color: 'rgba(26, 18, 7, 0.08)' },
        horzLines: { color: 'rgba(26, 18, 7, 0.08)' },
      },
      rightPriceScale: { borderColor: 'rgba(26, 18, 7, 0.25)' },
      timeScale: { borderColor: 'rgba(26, 18, 7, 0.25)', timeVisible: true },
      width: containerRef.current.clientWidth,
      height: containerRef.current.clientHeight || 320,
    });
    chartRef.current = chart;

    const series = chart.addSeries(CandlestickSeries, {
      upColor: '#5BD66B',
      downColor: '#FF7043',
      borderVisible: false,
      wickUpColor: '#5BD66B',
      wickDownColor: '#FF7043',
    });
    seriesRef.current = series;

    const onCrosshairMove = (param: MouseEventParams<Time>) => {
      if (!param.point || param.time === undefined) {
        setHover(null);
        return;
      }
      const bar = param.seriesData.get(series) as
        | { open: number; high: number; low: number; close: number }
        | undefined;
      if (!bar) {
        setHover(null);
        return;
      }
      const timeSec = typeof param.time === 'number' ? param.time : Number(param.time);
      const extra = candleByTimeRef.current.get(timeSec);
      setHover({
        time: timeSec,
        open: bar.open,
        high: bar.high,
        low: bar.low,
        close: bar.close,
        volume: extra?.volume ?? null,
        trades: extra?.trades ?? null,
        x: param.point.x,
        y: param.point.y,
      });
    };
    chart.subscribeCrosshairMove(onCrosshairMove);

    const handleResize = () => {
      if (!containerRef.current) return;
      chart.applyOptions({
        width: containerRef.current.clientWidth,
        height: containerRef.current.clientHeight || 320,
      });
    };
    window.addEventListener('resize', handleResize);

    return () => {
      window.removeEventListener('resize', handleResize);
      chart.unsubscribeCrosshairMove(onCrosshairMove);
      chart.remove();
      chartRef.current = null;
      seriesRef.current = null;
    };
  }, []);

  useEffect(() => {
    if (!seriesRef.current) return;
    seriesRef.current.setData(chartData);
    if (chartData.length > 0) {
      chartRef.current?.timeScale().fitContent();
    }
  }, [chartData]);

  // Drop stale hover state whenever the dataset changes shape, so the tooltip
  // can never describe a candle that is no longer on screen.
  useEffect(() => {
    setHover(null);
  }, [resolution, symbol, chainId, source]);

  const now = useNow(1000);
  const nextRefreshSec = dataUpdatedAt
    ? Math.max(0, Math.ceil((dataUpdatedAt + refetchIntervalMs - now) / 1000))
    : Math.ceil(refetchIntervalMs / 1000);

  // "unavailable" means the server could not read its store — a different
  // failure from an honest empty result, so it gets its own state.
  const referenceUnavailable = !isOnchain && reference.seriesStatus === 'unavailable';

  const state: 'no-market' | 'loading' | 'error' | 'unavailable' | 'empty' | 'data' = !symbol
    ? 'no-market'
    : isLoading && chartData.length === 0
      ? 'loading'
      : error
        ? 'error'
        : referenceUnavailable
          ? 'unavailable'
          : chartData.length === 0
            ? 'empty'
            : 'data';

  const timeFormatter = useMemo(
    () =>
      new Intl.DateTimeFormat(locale, {
        month: 'short',
        day: '2-digit',
        hour: '2-digit',
        minute: '2-digit',
      }),
    [locale],
  );
  const clockFormatter = useMemo(
    () => new Intl.DateTimeFormat(locale, { hour: '2-digit', minute: '2-digit', second: '2-digit' }),
    [locale],
  );

  const lastChart = chartData.length > 0 ? chartData[chartData.length - 1] : null;
  const lastExtra = lastChart ? extrasByTime.get(Number(lastChart.time)) : undefined;

  const endpoint = isOnchain ? 'GET /api/trading/candles' : 'GET /api/prices/candles';
  const sourceLabel = t(`source.${source}.label`);
  const providerChip =
    source === 'market' && reference.provider ? reference.provider : undefined;

  // Tooltip placement: keep inside the frame, flip when near the right edge.
  const tipStyle = useMemo(() => {
    if (!hover || !containerRef.current) return undefined;
    const bounds = containerRef.current.getBoundingClientRect();
    const flipX = hover.x > bounds.width - 200;
    const left = flipX ? Math.max(8, hover.x - 190) : Math.min(bounds.width - 180, hover.x + 16);
    const top = Math.max(8, Math.min((bounds.height || 320) - 150, hover.y + 12));
    return { left, top } as React.CSSProperties;
  }, [hover]);

  return (
    <div className="col" style={{ flex: 1, minHeight: 0, gap: 0 }}>
      {/* Source selector — the three series are different kinds of truth, so
          switching between them is an explicit, always-visible control. */}
      <div className="chart-source-bar src-meta" data-testid="trade-chart-source-bar">
        <span className="src-chip" style={{ opacity: 0.75 }}>{t('sourceLabel')}</span>
        {SOURCE_ORDER.map((option) => (
          <button
            key={option}
            type="button"
            className={'tag ' + (source === option ? 'on' : '')}
            style={{ cursor: 'pointer' }}
            onClick={() => selectSource(option)}
            title={t(`source.${option}.hint`)}
            aria-pressed={source === option}
            data-testid={`trade-source-${option}`}
          >
            {t(`source.${option}.label`)}
          </button>
        ))}
      </div>

      {/* Why the chart is not showing this product's own trades. Only appears
          when the fallback actually happened, and states the reason plainly. */}
      {autoFellBack && source === 'market' && (
        <div className="chart-source-note src-meta" data-testid="trade-chart-fallback-note">
          <Icon name="signal" size={11} />
          <span>{t('fallbackNote')}</span>
        </div>
      )}

      <div className="chartw" style={{ position: 'relative' }}>
        <div className="chart-overlay">
          {TRADING_CHART_RESOLUTIONS.map((option) => (
            <button
              key={option.value}
              type="button"
              className={'tag ' + (resolution === option.value ? 'on' : '')}
              style={{ cursor: 'pointer' }}
              onClick={() => onResolutionChange(option.value)}
              data-testid={`trade-resolution-${option.value}`}
            >
              {option.label}
            </button>
          ))}
        </div>

        {/* Last-candle legend (when data exists and nothing is hovered) */}
        {state === 'data' && lastChart && !hover && (
          <div className="chart-legend" data-testid="trade-chart-legend">
            <span className="src-chip">{sourceLabel}</span>
            <span className="src-chip">
              {t('close')} <b>{formatTradingChartPrice(lastChart.close)}</b>
            </span>
            {lastExtra?.volume && (
              <span className="src-chip">
                {t('volume')} <b>{lastExtra.volume}</b>
              </span>
            )}
            {lastExtra?.trades !== null && lastExtra?.trades !== undefined && (
              <span className="src-chip">
                {t('trades')} <b>{lastExtra.trades}</b>
              </span>
            )}
            <span className="src-chip" style={{ opacity: 0.75 }}>{t('hoverHint')}</span>
          </div>
        )}

        {/* Crosshair tooltip */}
        {hover && (
          <div className="chart-tip" style={tipStyle} data-testid="trade-chart-tooltip">
            <div className="tt-time">{timeFormatter.format(hover.time * 1000)}</div>
            <div className="tt-row"><span>{t('open')}</span><b>{formatTradingChartPrice(hover.open)}</b></div>
            <div className="tt-row"><span>{t('high')}</span><b>{formatTradingChartPrice(hover.high)}</b></div>
            <div className="tt-row"><span>{t('low')}</span><b>{formatTradingChartPrice(hover.low)}</b></div>
            <div className="tt-row"><span>{t('close')}</span><b>{formatTradingChartPrice(hover.close)}</b></div>
            <div className="tt-row"><span>{t('volume')}</span><b>{hover.volume ?? '—'}</b></div>
            {hover.trades !== null && (
              <div className="tt-row"><span>{t('trades')}</span><b>{hover.trades}</b></div>
            )}
          </div>
        )}

        {/* Typed non-data overlays — the frame stays mounted and interactive */}
        {state !== 'data' && (
          <div
            data-testid={state === 'empty' ? 'trade-chart-empty' : `trade-chart-${state}`}
            style={{
              position: 'absolute',
              inset: 0,
              zIndex: 2,
              display: 'grid',
              placeItems: 'center',
              textAlign: 'center',
              padding: 24,
              pointerEvents: 'none',
            }}
          >
            <div style={{ maxWidth: 420 }}>
              <div style={{ fontFamily: 'var(--df)', fontWeight: 800, fontSize: 14, color: 'var(--ink)' }}>
                {state === 'no-market' && t('noMarketTitle')}
                {state === 'loading' && t('loadingTitle')}
                {state === 'error' && t('errorTitle')}
                {state === 'unavailable' && t('unavailableTitle')}
                {state === 'empty' && t('emptyTitle')}
              </div>
              <div className="mono" style={{ marginTop: 6, fontSize: 12, color: 'var(--ink-2)', lineHeight: 1.5 }}>
                {state === 'no-market' && t('noMarketBody')}
                {state === 'loading' && t('loadingBody', { symbol: symbol ?? '—' })}
                {state === 'error' && (
                  <>
                    {t('errorBody')}
                    {error?.message ? ` (${error.message.slice(0, 90)})` : ''}
                  </>
                )}
                {state === 'unavailable' && t('unavailableBody')}
                {state === 'empty' && t(`empty.${source}`, { symbol: symbol ?? '—' })}
              </div>
              {state === 'empty' && symbol && (
                <div className="mono" style={{ marginTop: 8, fontSize: 11, color: 'var(--ink-2)' }}>
                  {t('emptyMeta', {
                    symbol,
                    chainId: chainId ?? '—',
                    resolution,
                    seconds: nextRefreshSec,
                  })}
                </div>
              )}
              {(state === 'error' || state === 'unavailable') && (
                <button
                  type="button"
                  className="src-refresh"
                  style={{ marginTop: 10, pointerEvents: 'auto' }}
                  onClick={() => refetch()}
                  data-testid="trade-chart-retry"
                >
                  <Icon name="refresh" size={11} /> {t('nextRefresh', { seconds: nextRefreshSec })}
                </button>
              )}
            </div>
          </div>
        )}

        {/* zIndex 0 gives the chart its own stacking context so the library's
            internal z-indexed canvases can't sit above (and swallow clicks
            meant for) the timeframe buttons in .chart-overlay. */}
        <div ref={containerRef} data-testid="trade-candle-chart" style={{ position: 'absolute', inset: 0, zIndex: 0 }} />
      </div>

      {/* Data-source status bar: source · endpoint · state · updated · cadence */}
      <div className="chart-status-bar src-meta" data-testid="trade-chart-status">
        <span
          className="src-chip src-state"
          style={{
            background: state === 'data' ? 'var(--g)' : state === 'error' || state === 'unavailable' ? 'var(--o)' : 'var(--y)',
            color: state === 'error' || state === 'unavailable' ? '#fff' : 'var(--ink)',
          }}
        >
          {state === 'data'
            ? t('candles', { count: chartData.length })
            : state === 'error'
              ? t('apiError')
              : state === 'unavailable'
                ? t('apiUnavailable')
                : t('apiEmpty')}
        </span>
        <span className="src-chip" data-testid="trade-chart-source-chip">{sourceLabel}</span>
        {providerChip && <span className="src-chip">{providerChip}</span>}
        <span className="src-chip">{endpoint}</span>
        {chainId !== undefined && isOnchain && <span className="src-chip">{t('chainChip', { chainId })}</span>}
        <span className="src-chip">{resolution}</span>
        {dataUpdatedAt > 0 && (
          <span className="src-chip" data-testid="trade-chart-updated">
            {t('updated', { time: clockFormatter.format(dataUpdatedAt) })}
          </span>
        )}
        <span className="src-chip">{t('nextRefresh', { seconds: nextRefreshSec })}</span>
        <button
          type="button"
          className="src-refresh"
          onClick={() => refetch()}
          disabled={isFetching}
          aria-label={t('nextRefresh', { seconds: nextRefreshSec })}
          data-testid="trade-chart-refresh"
        >
          <Icon name="refresh" size={11} />
        </button>
      </div>
    </div>
  );
}
