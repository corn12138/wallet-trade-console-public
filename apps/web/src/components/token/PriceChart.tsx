'use client';

import { buildApiUrl } from '@/lib/api/base-url';
import { useQuery } from '@tanstack/react-query';
import { useTranslations, useLocale } from 'next-intl';
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

interface PriceChartProps {
    tokenAddress: string;
}

// Shape of GET /token/address/{address}/candles — server-side OHLCV
// aggregated from indexed token trades (prices are decimal strings).
interface TokenCandle {
    timestamp: number;
    open: string;
    high: string;
    low: string;
    close: string;
    volume: string;
    trades: number;
}

type HoverCandle = {
    time: number; // seconds
    open: number;
    high: number;
    low: number;
    close: number;
    volume: string;
    trades: number;
    x: number;
    y: number;
};

const RESOLUTIONS = ['1m', '5m', '15m', '1h', '4h', '1d'] as const;
type TokenChartResolution = (typeof RESOLUTIONS)[number];

function formatPrice(value: number): string {
    if (!Number.isFinite(value)) return '—';
    if (value === 0) return '0';
    const abs = Math.abs(value);
    const digits = abs >= 1 ? 4 : abs >= 0.0001 ? 6 : 10;
    return value.toLocaleString('en-US', { maximumFractionDigits: digits });
}

function formatVolume(value: string): string {
    const n = Number(value);
    if (!Number.isFinite(n)) return value;
    return n.toLocaleString('en-US', { maximumFractionDigits: 2 });
}

/**
 * Token price chart. Candles come from the real
 * /token/address/{address}/candles endpoint — never synthetic. Renders typed
 * loading / error / empty / data overlays, a crosshair tooltip (time + OHLC +
 * volume + trade count) matching the /trade terminal, and a data-source status
 * bar (endpoint · resolution · candle count · last updated · refresh).
 */
export function PriceChart({ tokenAddress }: PriceChartProps) {
    const t = useTranslations('tokenChart');
    const locale = useLocale();
    const chartContainerRef = useRef<HTMLDivElement>(null);
    const chartRef = useRef<IChartApi | null>(null);
    const seriesRef = useRef<ISeriesApi<'Candlestick'> | null>(null);
    const candleByTimeRef = useRef<Map<number, TokenCandle>>(new Map());
    const [resolution, setResolution] = useState<TokenChartResolution>('15m');
    const [hover, setHover] = useState<HoverCandle | null>(null);

    const refetchIntervalMs = resolution === '1m' ? 5_000 : 30_000;

    const { data: candles, isLoading, isFetching, error, dataUpdatedAt, refetch } = useQuery({
        queryKey: ['token-candles', tokenAddress, resolution],
        queryFn: async (): Promise<TokenCandle[]> => {
            const res = await fetch(
                buildApiUrl(`/token/address/${tokenAddress}/candles?resolution=${resolution}&limit=200`),
            );
            if (!res.ok) throw new Error('Failed to fetch token candles');
            const payload = await res.json();
            const rows = Array.isArray(payload) ? payload : payload?.data;
            return Array.isArray(rows) ? rows : [];
        },
        refetchInterval: refetchIntervalMs,
        enabled: Boolean(tokenAddress),
    });

    const chartData = useMemo(() => {
        return (candles ?? [])
            .map((candle) => ({
                time: Math.floor(candle.timestamp / 1000) as Time,
                open: Number(candle.open),
                high: Number(candle.high),
                low: Number(candle.low),
                close: Number(candle.close),
            }))
            .filter((candle) =>
                [candle.open, candle.high, candle.low, candle.close].every(
                    (value) => Number.isFinite(value) && value >= 0,
                ) && candle.high > 0,
            )
            .sort((left, right) => Number(left.time) - Number(right.time));
    }, [candles]);

    // Index raw candles by bucket time (seconds) so the crosshair can surface
    // volume + trade count, which the price series does not carry.
    useEffect(() => {
        const map = new Map<number, TokenCandle>();
        for (const candle of candles ?? []) {
            map.set(Math.floor(candle.timestamp / 1000), candle);
        }
        candleByTimeRef.current = map;
    }, [candles]);

    useEffect(() => {
        if (!chartContainerRef.current) return;

        const chart = createChart(chartContainerRef.current, {
            layout: {
                background: { type: ColorType.Solid, color: '#161922' },
                textColor: '#9ca3af',
                attributionLogo: false,
            },
            grid: {
                vertLines: { color: 'rgba(42, 46, 57, 0.5)' },
                horzLines: { color: 'rgba(42, 46, 57, 0.5)' },
            },
            timeScale: { timeVisible: true },
            width: chartContainerRef.current.clientWidth,
            height: 400,
        });
        chartRef.current = chart;

        const series = chart.addSeries(CandlestickSeries, {
            upColor: '#BEFA0A',
            downColor: '#ef4444',
            borderVisible: false,
            wickUpColor: '#BEFA0A',
            wickDownColor: '#ef4444',
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
            const raw = candleByTimeRef.current.get(timeSec);
            setHover({
                time: timeSec,
                open: bar.open,
                high: bar.high,
                low: bar.low,
                close: bar.close,
                volume: raw?.volume ?? '0',
                trades: raw?.trades ?? 0,
                x: param.point.x,
                y: param.point.y,
            });
        };
        chart.subscribeCrosshairMove(onCrosshairMove);

        const handleResize = () => {
            chart.applyOptions({ width: chartContainerRef.current?.clientWidth });
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

    // Drop stale hover when the dataset shape changes (resolution switch).
    useEffect(() => {
        setHover(null);
    }, [resolution, tokenAddress]);

    const state: 'loading' | 'error' | 'empty' | 'data' =
        isLoading && chartData.length === 0
            ? 'loading'
            : error
                ? 'error'
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

    const lastRaw = (candles ?? []).length > 0 ? (candles as TokenCandle[])[(candles as TokenCandle[]).length - 1] : null;
    const lastChart = chartData.length > 0 ? chartData[chartData.length - 1] : null;

    const tipStyle = useMemo(() => {
        if (!hover || !chartContainerRef.current) return undefined;
        const bounds = chartContainerRef.current.getBoundingClientRect();
        const flipX = hover.x > bounds.width - 190;
        const left = flipX ? Math.max(8, hover.x - 180) : Math.min(bounds.width - 172, hover.x + 16);
        const top = Math.max(8, Math.min(400 - 150, hover.y + 12));
        return { left, top } as React.CSSProperties;
    }, [hover]);

    const overlay =
        state === 'loading'
            ? { title: t('loadingTitle'), body: t('loadingBody') }
            : state === 'error'
                ? { title: t('errorTitle'), body: (error as Error | null)?.message || t('errorBody') }
                : state === 'empty'
                    ? { title: t('emptyTitle'), body: t('emptyBody') }
                    : null;

    return (
        <div className="card p-4">
            <div className="mb-4 flex items-center justify-between">
                <h3 className="text-lg font-bold">{t('title')}</h3>
                <div className="flex gap-1">
                    {RESOLUTIONS.map((option) => (
                        <button
                            key={option}
                            type="button"
                            onClick={() => setResolution(option)}
                            className={`rounded px-2 py-1 text-xs font-medium transition-colors ${
                                resolution === option
                                    ? 'bg-(--color-accent-muted) text-(--color-accent)'
                                    : 'text-(--color-text-muted) hover:text-(--color-text-primary)'
                            }`}
                            data-testid={`token-resolution-${option}`}
                        >
                            {option}
                        </button>
                    ))}
                </div>
            </div>
            <div className="relative h-[400px] w-full">
                {/* Last-candle legend when data exists and nothing is hovered */}
                {state === 'data' && lastChart && !hover && (
                    <div
                        data-testid="token-chart-legend"
                        className="pointer-events-none absolute left-2 top-2 z-10 flex flex-wrap gap-2 rounded-md bg-black/40 px-2 py-1 text-[11px] text-slate-300"
                    >
                        <span>{t('lastCandle')}</span>
                        <span>
                            {t('close')} <b className="text-white">{formatPrice(lastChart.close)}</b>
                        </span>
                        {lastRaw && (
                            <span>
                                {t('volume')} <b className="text-white">{formatVolume(lastRaw.volume)}</b>
                            </span>
                        )}
                        {lastRaw && (
                            <span>
                                {t('trades')} <b className="text-white">{lastRaw.trades}</b>
                            </span>
                        )}
                        <span className="opacity-70">{t('hoverHint')}</span>
                    </div>
                )}

                {/* Crosshair tooltip */}
                {hover && (
                    <div
                        data-testid="token-chart-tooltip"
                        className="pointer-events-none absolute z-20 min-w-[150px] rounded-md border border-slate-700 bg-[#0d0f16]/95 px-3 py-2 text-[11px] text-slate-300 shadow-lg"
                        style={tipStyle}
                    >
                        <div className="mb-1 font-semibold text-white">{timeFormatter.format(hover.time * 1000)}</div>
                        <TipRow label={t('open')} value={formatPrice(hover.open)} />
                        <TipRow label={t('high')} value={formatPrice(hover.high)} />
                        <TipRow label={t('low')} value={formatPrice(hover.low)} />
                        <TipRow label={t('close')} value={formatPrice(hover.close)} />
                        <TipRow label={t('volume')} value={formatVolume(hover.volume)} />
                        <TipRow label={t('trades')} value={String(hover.trades)} />
                    </div>
                )}

                {overlay && (
                    <div
                        data-testid={`token-chart-${state}`}
                        className="absolute inset-0 z-10 flex items-center justify-center bg-[#161922]/95 px-6"
                    >
                        <div className="max-w-sm text-center">
                            <p className="text-sm font-medium text-white">{overlay.title}</p>
                            <p className="mt-2 text-sm text-slate-500">{overlay.body}</p>
                            {state === 'error' && (
                                <button
                                    type="button"
                                    onClick={() => refetch()}
                                    className="mt-3 rounded bg-(--color-accent-muted) px-3 py-1 text-xs font-medium text-(--color-accent)"
                                    data-testid="token-chart-retry"
                                >
                                    {t('refresh')}
                                </button>
                            )}
                        </div>
                    </div>
                )}
                <div ref={chartContainerRef} data-testid="token-price-chart" className="h-[400px] w-full" />
            </div>

            {/* Data-source status bar: endpoint · state · resolution · updated · refresh */}
            <div
                data-testid="token-chart-status"
                className="mt-3 flex flex-wrap items-center gap-2 text-[11px] text-slate-400"
            >
                <span
                    data-testid="token-chart-state"
                    className={`rounded px-2 py-0.5 font-medium ${
                        state === 'data'
                            ? 'bg-lime-500/20 text-lime-300'
                            : state === 'error'
                                ? 'bg-red-500/20 text-red-300'
                                : 'bg-slate-600/30 text-slate-300'
                    }`}
                >
                    {state === 'data' ? t('candles', { count: chartData.length }) : state === 'error' ? t('apiError') : t('apiEmpty')}
                </span>
                <span className="rounded bg-slate-800/60 px-2 py-0.5 font-mono">
                    GET /api/token/address/:address/candles
                </span>
                <span className="rounded bg-slate-800/60 px-2 py-0.5 font-mono">{resolution}</span>
                {dataUpdatedAt > 0 && (
                    <span data-testid="token-chart-updated" className="rounded bg-slate-800/60 px-2 py-0.5">
                        {t('updated', { time: clockFormatter.format(dataUpdatedAt) })}
                    </span>
                )}
                <button
                    type="button"
                    onClick={() => refetch()}
                    disabled={isFetching}
                    aria-label={t('refresh')}
                    data-testid="token-chart-refresh"
                    className="rounded bg-slate-800/60 px-2 py-0.5 disabled:opacity-50"
                >
                    ↻ {t('refresh')}
                </button>
            </div>
        </div>
    );
}

function TipRow({ label, value }: { label: string; value: string }) {
    return (
        <div className="flex justify-between gap-4">
            <span>{label}</span>
            <b className="text-white">{value}</b>
        </div>
    );
}
