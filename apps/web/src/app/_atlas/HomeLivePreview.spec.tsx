import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { screen, waitFor } from '@testing-library/react';
import { renderWithIntl } from '@/test/renderWithIntl';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { HomeLivePreview } from './HomeLivePreview';

/**
 * Regression guard for the home terminal preview: it must be a real read-only
 * view of the trading service (markets + stats + live candle chart), never the
 * old fake preview with Math.random candles and the static 67,421.55 / $2.41B /
 * ATLAS orderbook sample values.
 */

const mockGetMarkets = vi.fn();
const mockGetStats = vi.fn();

vi.mock('@/lib/api', () => ({
    getTradingMarketsByChain: (chainId?: number) => mockGetMarkets(chainId),
    getTradingStats: (chainId?: number) => mockGetStats(chainId),
}));

vi.mock('./pages/TradeCandleChart', () => ({
    TradeCandleChart: (props: { symbol?: string }) => (
        <div data-testid="trade-candle-chart-stub" data-symbol={props.symbol ?? ''} />
    ),
}));

vi.mock('@/hooks/useDisplayChainId', () => ({
    useDisplayChainId: () => ({ chainId: 11155111, isFallback: false }),
}));

function renderPreview() {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    return renderWithIntl(
        <QueryClientProvider client={client}>
            <HomeLivePreview />
        </QueryClientProvider>,
    );
}

const MARKET = {
    symbol: 'BTC-USD',
    chainId: 11155111,
    indexToken: '0x0000000000000000000000000000000000000001',
    collateralToken: '0x0000000000000000000000000000000000000002',
    longOpenInterest: '0',
    shortOpenInterest: '0',
    fundingRate: '0.0001',
    volume24h: '1000',
};

describe('HomeLivePreview', () => {
    beforeEach(() => {
        vi.clearAllMocks();
    });

    it('renders the real trading chart + stats and no static sample data', async () => {
        mockGetMarkets.mockResolvedValue([MARKET]);
        mockGetStats.mockResolvedValue({ totalVolume: '1000000', totalOpenInterest: '50000' });

        renderPreview();

        await waitFor(() =>
            expect(screen.getByTestId('trade-candle-chart-stub')).toHaveAttribute('data-symbol', 'BTC-USD'),
        );
        expect(mockGetMarkets).toHaveBeenCalledWith(11155111);

        // The old fake sample values must not appear anywhere.
        expect(screen.queryByText(/67,421\.55/)).not.toBeInTheDocument();
        expect(screen.queryByText(/\$2\.41B/)).not.toBeInTheDocument();
    });

    it('renders an honest empty state when the trading service has no markets', async () => {
        mockGetMarkets.mockResolvedValue([]);
        mockGetStats.mockResolvedValue({ totalVolume: '0', totalOpenInterest: '0' });

        renderPreview();

        expect(await screen.findByText('No live markets yet')).toBeInTheDocument();
        expect(screen.queryByTestId('trade-candle-chart-stub')).not.toBeInTheDocument();
    });
});
