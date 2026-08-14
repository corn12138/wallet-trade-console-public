import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { act, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { renderWithIntl } from '@/test/renderWithIntl';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { PriceChart } from './PriceChart';

/**
 * Regression guards for the token chart:
 *  - lightweight-charts v5 series API (addSeries), not the removed v4 API
 *  - candles come from the real /token/address/{address}/candles endpoint
 *  - timeframe buttons change the requested resolution
 *  - typed empty/error states instead of a blank or fake chart
 *  - crosshair tooltip surfaces OHLC + volume + trades on real candle data
 *  - a data-source status bar renders endpoint / candle count / refresh
 */

const mockAddSeries = vi.fn();
const mockSetData = vi.fn();
const mockSeries = { setData: mockSetData };
let crosshairHandler: ((param: unknown) => void) | undefined;

const mockChart = {
    addSeries: mockAddSeries,
    applyOptions: vi.fn(),
    remove: vi.fn(),
    subscribeCrosshairMove: vi.fn((cb: (param: unknown) => void) => {
        crosshairHandler = cb;
    }),
    unsubscribeCrosshairMove: vi.fn(),
    timeScale: () => ({ fitContent: vi.fn() }),
};

vi.mock('lightweight-charts', () => ({
    createChart: vi.fn(() => mockChart),
    CandlestickSeries: 'CandlestickSeries',
    ColorType: { Solid: 'solid' },
}));

const TOKEN = '0x00000000000000000000000000000000000000aa';

function jsonResponse(payload: unknown) {
    return { ok: true, json: async () => payload } as Response;
}

function renderChart() {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    return renderWithIntl(
        <QueryClientProvider client={client}>
            <PriceChart tokenAddress={TOKEN} />
        </QueryClientProvider>,
    );
}

describe('token PriceChart', () => {
    const fetchMock = vi.fn();

    beforeEach(() => {
        vi.clearAllMocks();
        crosshairHandler = undefined;
        mockAddSeries.mockReturnValue(mockSeries);
        vi.stubGlobal('fetch', fetchMock);
    });

    it('uses the lightweight-charts v5 addSeries API with real candle data', async () => {
        fetchMock.mockResolvedValue(jsonResponse([
            { timestamp: 1750000000000, open: '1.0', high: '1.2', low: '0.9', close: '1.1', volume: '10', trades: 3 },
        ]));

        renderChart();

        expect(mockAddSeries).toHaveBeenCalledWith('CandlestickSeries', expect.any(Object));

        await waitFor(() => {
            expect(mockSetData).toHaveBeenCalledWith([
                expect.objectContaining({ open: 1, high: 1.2, low: 0.9, close: 1.1 }),
            ]);
        });

        const requestedUrl = String(fetchMock.mock.calls[0][0]);
        expect(requestedUrl).toContain(`/token/address/${TOKEN}/candles`);
        expect(requestedUrl).toContain('resolution=15m');
    });

    it('changes the requested resolution when a timeframe button is clicked', async () => {
        fetchMock.mockResolvedValue(jsonResponse([]));
        const user = userEvent.setup();

        renderChart();
        await user.click(screen.getByTestId('token-resolution-1h'));

        await waitFor(() => {
            const urls = fetchMock.mock.calls.map((call) => String(call[0]));
            expect(urls.some((url) => url.includes('resolution=1h'))).toBe(true);
        });
    });

    it('shows an explicit empty state instead of a fake chart', async () => {
        fetchMock.mockResolvedValue(jsonResponse([]));

        renderChart();

        expect(await screen.findByText('No trades yet')).toBeInTheDocument();
        expect(screen.getByTestId('token-chart-empty')).toBeInTheDocument();
        // Status bar renders the honest zero-row state, not a fabricated count.
        expect(screen.getByTestId('token-chart-state')).toHaveTextContent('0 rows');
    });

    it('surfaces API failures instead of hiding them', async () => {
        fetchMock.mockResolvedValue({ ok: false, json: async () => ({}) } as Response);

        renderChart();

        expect(await screen.findByText('Chart unavailable')).toBeInTheDocument();
        expect(screen.getByTestId('token-chart-error')).toBeInTheDocument();
    });

    it('renders a status bar with endpoint + candle count on real data', async () => {
        fetchMock.mockResolvedValue(jsonResponse([
            { timestamp: 1750000000000, open: '1.0', high: '1.2', low: '0.9', close: '1.1', volume: '10', trades: 3 },
        ]));

        renderChart();

        await waitFor(() => {
            expect(screen.getByTestId('token-chart-state')).toHaveTextContent('1 candles');
        });
        expect(screen.getByTestId('token-chart-status')).toHaveTextContent(
            'GET /api/token/address/:address/candles',
        );
        expect(screen.getByTestId('token-chart-refresh')).toBeInTheDocument();
    });

    it('shows a crosshair tooltip with OHLC + volume + trades on hover', async () => {
        fetchMock.mockResolvedValue(jsonResponse([
            { timestamp: 1750000000000, open: '1.0', high: '1.2', low: '0.9', close: '1.1', volume: '42', trades: 7 },
        ]));

        renderChart();

        // Wait until the real candle has loaded (status bar flips to 1 candle)
        // so candleByTimeRef is populated before we simulate the crosshair.
        await waitFor(() => {
            expect(screen.getByTestId('token-chart-state')).toHaveTextContent('1 candles');
        });
        expect(crosshairHandler).toBeTypeOf('function');

        act(() => {
            crosshairHandler?.({
                point: { x: 100, y: 100 },
                time: 1750000000,
                seriesData: new Map([[mockSeries, { open: 1, high: 1.2, low: 0.9, close: 1.1 }]]),
            });
        });

        const tooltip = await screen.findByTestId('token-chart-tooltip');
        expect(tooltip).toHaveTextContent('42'); // volume
        expect(tooltip).toHaveTextContent('7'); // trades
        expect(tooltip).toHaveTextContent('1.1'); // close
    });
});
