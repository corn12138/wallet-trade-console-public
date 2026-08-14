import { act, fireEvent, screen } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { renderWithIntl } from '@/test/renderWithIntl';
import { TradeCandleChart } from './TradeCandleChart';

/**
 * Regression guards for the /trade candle chart interaction contract:
 *  - a crosshair subscription MUST exist and drive an OHLC/volume/trades
 *    tooltip (removing subscribeCrosshairMove or the tooltip fails here)
 *  - zero rows from the candles API renders the explicit typed empty state
 *    (with symbol/chain/resolution/refresh metadata), not dead blank space
 *  - timeframe buttons propagate resolution changes
 *  - a failed feed renders the error state, distinct from empty
 *
 * Plus the source contract. The chart can draw three genuinely different real
 * series and must never let the user mistake one for another:
 *  - it falls back to spot reference data ONLY when on-chain is truly empty,
 *    and says so
 *  - a manual source pick is never auto-overridden
 *  - the oracle source shows no volume, because an oracle has none
 */

type CrosshairHandler = (param: unknown) => void;

const crosshairHandlers: CrosshairHandler[] = [];
const setDataMock = vi.fn();

vi.mock('lightweight-charts', () => ({
    ColorType: { Solid: 'solid' },
    CandlestickSeries: 'candlestick',
    createChart: vi.fn(() => ({
        addSeries: vi.fn(() => ({ setData: setDataMock })),
        subscribeCrosshairMove: vi.fn((handler: CrosshairHandler) => {
            crosshairHandlers.push(handler);
        }),
        unsubscribeCrosshairMove: vi.fn(),
        applyOptions: vi.fn(),
        remove: vi.fn(),
        timeScale: vi.fn(() => ({ fitContent: vi.fn() })),
    })),
}));

const mockUseTradingCandles = vi.fn();
vi.mock('@/hooks/useTradingCandles', () => ({
    useTradingCandles: (args: unknown) => mockUseTradingCandles(args),
}));

const mockUsePriceCandles = vi.fn();
vi.mock('@/hooks/usePriceCandles', () => ({
    usePriceCandles: (args: unknown) => mockUsePriceCandles(args),
}));

const BUCKET_SEC = 1_751_000_000;
const CANDLE = {
    timestamp: BUCKET_SEC * 1000,
    open: '100',
    high: '110',
    low: '95',
    close: '105',
    volume: '2500000000000000000000000000000000', // 2500 USD30
    trades: 7,
};

/** Reference candles arrive as plain NUMERIC(38,18) decimal strings. */
const PRICE_CANDLE = {
    timestamp: BUCKET_SEC * 1000,
    open: '1907.670000000000000000',
    high: '1907.880000000000000000',
    low: '1905.250000000000000000',
    close: '1906.060000000000000000',
    volume: '147.522300000000000000',
    quoteVolume: '281332.756163000000000000',
    closed: true,
};

function stubTrading(overrides: Record<string, unknown> = {}) {
    mockUseTradingCandles.mockReturnValue({
        candles: [CANDLE],
        error: null,
        isLoading: false,
        isFetching: false,
        dataUpdatedAt: Date.now(),
        refetch: vi.fn(),
        ...overrides,
    });
}

function stubPrices(overrides: Record<string, unknown> = {}) {
    mockUsePriceCandles.mockReturnValue({
        candles: [],
        seriesStatus: 'empty',
        provider: 'gateio',
        error: null,
        isLoading: false,
        isFetching: false,
        dataUpdatedAt: Date.now(),
        refetch: vi.fn(),
        ...overrides,
    });
}

function renderChart(
    tradingOverrides: Record<string, unknown> = {},
    priceOverrides: Record<string, unknown> = {},
) {
    stubTrading(tradingOverrides);
    stubPrices(priceOverrides);
    return renderWithIntl(
        <TradeCandleChart symbol="BTC-USD" chainId={11155111} resolution="15m" onResolutionChange={vi.fn()} />,
    );
}

describe('TradeCandleChart', () => {
    beforeEach(() => {
        vi.clearAllMocks();
        crosshairHandlers.length = 0;
    });

    it('subscribes to crosshair moves and shows an OHLC/volume/trades tooltip on hover', () => {
        renderChart();

        expect(crosshairHandlers.length).toBeGreaterThan(0);
        expect(screen.queryByTestId('trade-chart-tooltip')).toBeNull();

        // seriesData is keyed by the series instance in the real library;
        // mirror the lookup shape with a get() that returns the hovered bar.
        act(() => {
            crosshairHandlers[0]({
                point: { x: 120, y: 80 },
                time: BUCKET_SEC,
                seriesData: { get: () => ({ open: 100, high: 110, low: 95, close: 105 }) },
            });
        });

        const tooltip = screen.getByTestId('trade-chart-tooltip');
        expect(tooltip.textContent).toContain('Open');
        expect(tooltip.textContent).toContain('105');
        expect(tooltip.textContent).toContain('Volume');
        expect(tooltip.textContent).toContain('2.50K');
        expect(tooltip.textContent).toContain('Trades');
        expect(tooltip.textContent).toContain('7');

        // Leaving the pane clears the tooltip.
        act(() => {
            crosshairHandlers[0]({ point: undefined, time: undefined, seriesData: { get: () => undefined } });
        });
        expect(screen.queryByTestId('trade-chart-tooltip')).toBeNull();
    });

    it('renders the last-candle legend when data exists and nothing is hovered', () => {
        renderChart();
        const legend = screen.getByTestId('trade-chart-legend');
        expect(legend.textContent).toContain('On-chain trades');
        expect(legend.textContent).toContain('Trades');
    });

    it('renders the explicit typed empty state with symbol/chain/resolution metadata', () => {
        // No on-chain trades AND no reference rows → the chart stays on the
        // on-chain source and explains that specific emptiness.
        renderChart({ candles: [] });
        const empty = screen.getByTestId('trade-chart-empty');
        expect(empty.textContent).toContain('No candle data yet');
        expect(empty.textContent).toContain('BTC-USD');
        expect(empty.textContent).toContain('11155111');
        expect(empty.textContent).toContain('15m');
        expect(screen.queryByTestId('trade-chart-legend')).toBeNull();
    });

    it('renders the error state distinct from empty when the feed fails', () => {
        renderChart({ candles: [], error: new Error('boom') });
        expect(screen.getByTestId('trade-chart-error').textContent).toContain('Candle feed unavailable');
        expect(screen.queryByTestId('trade-chart-empty')).toBeNull();
    });

    it('propagates timeframe switches through the resolution buttons', () => {
        const onResolutionChange = vi.fn();
        stubTrading();
        stubPrices();
        renderWithIntl(
            <TradeCandleChart symbol="BTC-USD" chainId={11155111} resolution="15m" onResolutionChange={onResolutionChange} />,
        );
        fireEvent.click(screen.getByTestId('trade-resolution-1h'));
        expect(onResolutionChange).toHaveBeenCalledWith('1h');
    });

    it('falls back to spot reference data when on-chain is empty, and says so', () => {
        renderChart({ candles: [] }, { candles: [PRICE_CANDLE], seriesStatus: 'ok' });

        // The fallback is announced, not silent — the user must not think
        // these candles are trades that happened on this product.
        const note = screen.getByTestId('trade-chart-fallback-note');
        expect(note.textContent).toContain('reference prices');

        const status = screen.getByTestId('trade-chart-status');
        expect(status.textContent).toContain('Spot reference');
        expect(status.textContent).toContain('GET /api/prices/candles');
        expect(status.textContent).toContain('gateio');
    });

    it('does NOT fall back while on-chain candles exist', () => {
        renderChart({ candles: [CANDLE] }, { candles: [PRICE_CANDLE], seriesStatus: 'ok' });

        expect(screen.queryByTestId('trade-chart-fallback-note')).toBeNull();
        expect(screen.getByTestId('trade-chart-status').textContent).toContain('GET /api/trading/candles');
    });

    it('does NOT fall back when reference data is also empty', () => {
        renderChart({ candles: [] }, { candles: [], seriesStatus: 'empty' });

        expect(screen.queryByTestId('trade-chart-fallback-note')).toBeNull();
        expect(screen.getByTestId('trade-chart-source-chip').textContent).toContain('On-chain trades');
    });

    it('keeps a manually chosen source even when on-chain data is available', () => {
        renderChart({ candles: [CANDLE] }, { candles: [PRICE_CANDLE], seriesStatus: 'ok' });

        fireEvent.click(screen.getByTestId('trade-source-market'));
        expect(screen.getByTestId('trade-chart-source-chip').textContent).toContain('Spot reference');

        // A manual pick is a deliberate choice; nothing may silently undo it.
        expect(screen.queryByTestId('trade-chart-fallback-note')).toBeNull();
    });

    it('shows no volume for the oracle source, because an oracle reports no traded size', () => {
        renderChart(
            { candles: [] },
            { candles: [{ ...PRICE_CANDLE, volume: '0', quoteVolume: '0' }], seriesStatus: 'ok' },
        );

        fireEvent.click(screen.getByTestId('trade-source-oracle'));

        act(() => {
            crosshairHandlers[0]({
                point: { x: 120, y: 80 },
                time: BUCKET_SEC,
                seriesData: { get: () => ({ open: 1907.67, high: 1907.88, low: 1905.25, close: 1906.06 }) },
            });
        });

        const tooltip = screen.getByTestId('trade-chart-tooltip');
        // "—", never a fabricated 0 that would read as "no trades happened".
        expect(tooltip.textContent).toContain('—');
        expect(tooltip.textContent).not.toContain('Trades');
    });

    it('distinguishes an unavailable price store from an empty one', () => {
        renderChart({ candles: [] }, { candles: [], seriesStatus: 'unavailable' });

        fireEvent.click(screen.getByTestId('trade-source-market'));
        expect(screen.getByTestId('trade-chart-unavailable').textContent).toContain('Price store unavailable');
        expect(screen.queryByTestId('trade-chart-empty')).toBeNull();
    });
});
