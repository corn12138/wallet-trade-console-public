import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { screen } from '@testing-library/react';
import { renderWithIntl } from '@/test/renderWithIntl';
import userEvent from '@testing-library/user-event';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { TradePage } from '../_atlas/pages/TradePage';

/**
 * Regression guards for the /trade real-data closure:
 *  - the chart must be driven by the candle hook (no MiniChart / random SVG)
 *  - timeframe buttons must actually change the candle resolution
 *  - "limit" is an acceptable-price guard and MUST be passed to openPosition
 *  - unsupported STOP orders and pending-order Cancel must not render
 */

const mockUseTrading = vi.fn();
const mockUseTradingCandles = vi.fn();
const mockUsePriceCandles = vi.fn();

vi.mock('@/hooks/useTrading', () => ({
    useTrading: () => mockUseTrading(),
}));

vi.mock('@/hooks/useTradingCandles', () => ({
    useTradingCandles: (options: unknown) => mockUseTradingCandles(options),
}));

// The ticket's pre-sign review strip asks the Go service whether the AI
// explanation layer is live. Stubbed to "off" — its default and a supported
// production state — so these specs stay hermetic instead of reaching for
// localhost:8090.
vi.mock('@/lib/api/atlas', async (importOriginal) => ({
    ...(await importOriginal<Record<string, unknown>>()),
    getProductStatus: vi.fn(async () => ({ aiExplain: { status: 'disabled' }, warnings: [] })),
}));

// The chart also consults the reference price feed to decide whether an empty
// on-chain series can fall back. These specs are about the trade ticket, so the
// reference source is stubbed empty — that keeps the chart on the on-chain
// source and out of the way.
vi.mock('@/hooks/usePriceCandles', () => ({
    usePriceCandles: (options: unknown) => mockUsePriceCandles(options),
}));

vi.mock('lightweight-charts', () => {
    const series = { setData: vi.fn() };
    const chart = {
        addSeries: vi.fn(() => series),
        applyOptions: vi.fn(),
        remove: vi.fn(),
        subscribeCrosshairMove: vi.fn(),
        unsubscribeCrosshairMove: vi.fn(),
        timeScale: () => ({ fitContent: vi.fn() }),
    };
    return {
        createChart: vi.fn(() => chart),
        CandlestickSeries: 'CandlestickSeries',
        ColorType: { Solid: 'solid' },
    };
});

// Real next-intl provider + real en.json via renderWithIntl: assertions on
// visible copy (MARKET / LIMIT GUARD / Collateral) pin the actual messages.

const mockApp = {
    walletState: 'connected' as const,
    wallet: { address: '0x1111111111111111111111111111111111111111', chainId: 11155111, balance: 0 },
    chainId: 11155111,
    openConnect: vi.fn(),
    openChain: vi.fn(),
    closeModal: vi.fn(),
    pickWallet: vi.fn(),
    signSiwe: vi.fn(),
    disconnect: vi.fn(),
    switchChain: vi.fn(),
    drawerOpen: false,
    setDrawerOpen: vi.fn(),
    railOpen: false,
    setRailOpen: vi.fn(),
    modal: null,
    setModal: vi.fn(),
    toast: vi.fn(),
    toasts: [],
};

vi.mock('@/app/_atlas/AppContext', () => ({
    useApp: () => mockApp,
}));

const USD_30 = 10n ** 30n;

function market(overrides: Record<string, unknown> = {}) {
    return {
        symbol: 'ETH-USD',
        indexToken: '0x3333333333333333333333333333333333333333',
        collateralToken: '0x4444444444444444444444444444444444444444',
        indexDecimals: 18,
        collateralDecimals: 18,
        pricePrecision: 30,
        chainId: 11155111,
        fundingRate: '12500000000000000000000000',
        longOpenInterest: (150000n * USD_30).toString(),
        shortOpenInterest: (120000n * USD_30).toString(),
        volume24h: (780000n * USD_30).toString(),
        ...overrides,
    };
}

function tradingState(overrides: Record<string, unknown> = {}) {
    const m = market();
    return {
        markets: [m],
        selectedMarket: m,
        selectedMarketSymbol: m.symbol,
        setSelectedMarketSymbol: vi.fn(),
        isMarketsLoading: false,
        currentPrice: 2000n * USD_30,
        formattedPrice: '2000',
        priceLoading: false,
        orderbook: {
            bids: [[(1999n * USD_30).toString(), (4n * USD_30).toString()]],
            asks: [[(2001n * USD_30).toString(), (3n * USD_30).toString()]],
        },
        isOrderbookLoading: false,
        tradingStats: null,
        recentTrades: [],
        isRecentTradesLoading: false,
        marketStreamStatus: 'connected',
        marketStreamLastMessageAt: null,
        isMarketStreamConnected: true,
        usdcBalance: 1000n * 10n ** 18n,
        formattedBalance: '1000',
        balanceLoading: false,
        positions: [],
        positionsLoading: false,
        portfolioPositions: [],
        isPortfolioLoading: false,
        isActivityAuthorized: true,
        orders: [{
            id: 'order-1',
            token: '0x3333333333333333333333333333333333333333',
            isLong: true,
            orderType: 'LIMIT',
            sizeDelta: (1200n * USD_30).toString(),
            triggerPrice: (1995n * USD_30).toString(),
            status: 'PENDING',
            createdAt: '2026-07-01T08:00:00.000Z',
        }],
        history: [],
        isOrdersLoading: false,
        isHistoryLoading: false,
        address: '0x1111111111111111111111111111111111111111',
        chainId: 11155111,
        perpAddresses: {
            usdc: '0x00000000000000000000000000000000000000aa',
            positionManager: '0x00000000000000000000000000000000000000bb',
        },
        // Allowance already covers the collateral, so these specs exercise the
        // position-open branch and the approval preflight stays out of the way.
        isCollateralApproved: () => true,
        openPosition: vi.fn(),
        closePosition: vi.fn(),
        isApproving: false,
        isApproveSuccess: false,
        isOpenPending: false,
        isOpenConfirming: false,
        isOpenSuccess: false,
        openError: null,
        resetOpen: vi.fn(),
        isClosePending: false,
        isCloseConfirming: false,
        isCloseSuccess: false,
        closeError: null,
        resetClose: vi.fn(),
        ...overrides,
    };
}

function renderTradePage(overrides: Record<string, unknown> = {}) {
    const state = tradingState(overrides);
    mockUseTrading.mockReturnValue(state);
    // The ticket now carries a pre-sign review strip, which polls the Go
    // product-status endpoint through react-query — so the page needs a client
    // even though useTrading itself is mocked here.
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    renderWithIntl(
        <QueryClientProvider client={client}>
            <TradePage />
        </QueryClientProvider>,
    );
    return state;
}

describe('/trade real-data closure', () => {
    beforeEach(() => {
        vi.clearAllMocks();
        window.localStorage.clear();
        mockUseTradingCandles.mockReturnValue({ candles: [], error: null, isLoading: false, isFetching: false, dataUpdatedAt: Date.now(), refetch: vi.fn() });
        mockUsePriceCandles.mockReturnValue({ candles: [], seriesStatus: 'empty', provider: 'gateio', error: null, isLoading: false, isFetching: false, dataUpdatedAt: Date.now(), refetch: vi.fn() });
    });

    it('drives the chart from the trading candles hook (not a static chart)', () => {
        renderTradePage();

        expect(screen.getByTestId('trade-candle-chart')).toBeInTheDocument();
        expect(mockUseTradingCandles).toHaveBeenCalledWith({
            symbol: 'ETH-USD',
            chainId: 11155111,
            resolution: '15m',
            limit: 200,
        });
    });

    it('timeframe buttons change the candle resolution', async () => {
        const user = userEvent.setup();
        renderTradePage();

        await user.click(screen.getByRole('button', { name: '1H' }));

        expect(mockUseTradingCandles).toHaveBeenCalledWith(
            expect.objectContaining({ resolution: '1h' }),
        );
    });

    it('does not offer STOP orders (unsupported by the contract)', () => {
        renderTradePage();

        expect(screen.queryByRole('button', { name: /stop/i })).not.toBeInTheDocument();
        expect(screen.getByRole('button', { name: 'MARKET' })).toBeInTheDocument();
        expect(screen.getByRole('button', { name: 'LIMIT GUARD' })).toBeInTheDocument();
    });

    it('renders pending orders read-only without a cancel control', async () => {
        const user = userEvent.setup();
        renderTradePage();

        await user.click(screen.getByRole('button', { name: /Orders · 1/ }));

        expect(screen.getByText('PENDING')).toBeInTheDocument();
        expect(screen.queryByRole('button', { name: /cancel/i })).not.toBeInTheDocument();
    });

    it('market submit opens a position without an acceptable-price override', async () => {
        const user = userEvent.setup();
        const state = renderTradePage();

        await user.type(screen.getByLabelText('Collateral (USDC)'), '100');
        await user.click(screen.getByRole('button', { name: /Open Long · 10×/ }));

        expect(state.openPosition).toHaveBeenCalledWith({
            collateralAmount: '100',
            leverage: 10,
            isLong: true,
            acceptablePrice: undefined,
            slippagePercent: 0.5,
            deadlineMinutes: 20,
        });
    });

    it('limit guard submit passes the acceptable price to the write hook', async () => {
        const user = userEvent.setup();
        const state = renderTradePage();

        await user.click(screen.getByRole('button', { name: 'LIMIT GUARD' }));
        await user.type(screen.getByLabelText('Collateral (USDC)'), '100');
        await user.type(screen.getByLabelText('Acceptable price'), '2050');
        await user.click(screen.getByRole('button', { name: /Open Long · 10×/ }));

        expect(state.openPosition).toHaveBeenCalledWith({
            collateralAmount: '100',
            leverage: 10,
            isLong: true,
            acceptablePrice: '2050',
            slippagePercent: 0.5,
            deadlineMinutes: 20,
        });
    });

    it('blocks a long limit guard below mark price (would revert on-chain)', async () => {
        const user = userEvent.setup();
        const state = renderTradePage();

        await user.click(screen.getByRole('button', { name: 'LIMIT GUARD' }));
        await user.type(screen.getByLabelText('Collateral (USDC)'), '100');
        await user.type(screen.getByLabelText('Acceptable price'), '1950');

        const submit = screen.getByRole('button', { name: /Open Long · 10×/ });
        expect(submit).toBeDisabled();
        expect(screen.getByText(/Current price is above your long limit/)).toBeInTheDocument();

        await user.click(submit);
        expect(state.openPosition).not.toHaveBeenCalled();
    });

    it('blocks submit and close while execution settings are invalid', async () => {
        const user = userEvent.setup();
        renderTradePage();

        await user.type(screen.getByLabelText('Collateral (USDC)'), '100');
        await user.clear(screen.getByLabelText('Slippage'));
        await user.type(screen.getByLabelText('Slippage'), '9');

        expect(screen.getByText(/Slippage must stay between 0% and 5%/)).toBeInTheDocument();
        expect(screen.getByRole('button', { name: /Open Long · 10×/ })).toBeDisabled();
    });

    it('clicking a book level flips the ticket to limit mode with that price', async () => {
        const user = userEvent.setup();
        renderTradePage();

        // The 1999 bid row is a button labelled with the standard terminal action.
        await user.click(screen.getByRole('button', { name: 'Use 1,999.00 as the limit guard price' }));

        const limitInput = screen.getByLabelText('Acceptable price') as HTMLInputElement;
        expect(limitInput.value).toBe('1999.00');
    });

    it('percentage presets fill the collateral from the wallet balance', async () => {
        const user = userEvent.setup();
        renderTradePage();

        await user.click(screen.getByRole('button', { name: 'Use 25% of balance' }));

        const collateral = screen.getByLabelText('Collateral (USDC)') as HTMLInputElement;
        expect(collateral.value).toBe('250');
    });

    it('renders the recent-trades tape behind the Trades tab', async () => {
        const user = userEvent.setup();
        renderTradePage({
            recentTrades: [{
                id: 'fill-1',
                token: '0x3333333333333333333333333333333333333333',
                isLong: true,
                tradeType: 'OPEN',
                sizeDelta: (1200n * USD_30).toString(),
                price: (2000n * USD_30).toString(),
                fee: '0',
                pnl: null,
                txHash: '0xabc',
                createdAt: '2026-07-01T08:00:00.000Z',
            }],
        });

        await user.click(screen.getByTestId('book-tab-trades'));

        expect(screen.getByTestId('trades-tape')).toBeInTheDocument();
        expect(screen.getByText('2,000.00')).toBeInTheDocument();
    });

    it('shows a sign-in gate instead of a false "no orders" claim', async () => {
        const user = userEvent.setup();
        renderTradePage({ isActivityAuthorized: false, orders: [] });

        await user.click(screen.getByRole('button', { name: /Orders · 0/ }));

        expect(screen.getByTestId('tables-signin-gate')).toBeInTheDocument();
        expect(screen.queryByText(/No chain-backed pending orders/)).not.toBeInTheDocument();
    });

    it('renders the cross-market portfolio: chain rows close, indexed rows switch market', async () => {
        const user = userEvent.setup();
        const chainPosition = {
            size: 1000n * USD_30,
            collateral: 100n * USD_30,
            averagePrice: 1900n * USD_30,
            entryFundingRate: 0n,
            reserveAmount: 0n,
            realisedPnl: 0n,
            lastUpdatedAt: 0n,
            hasPosition: true,
            isLong: true,
            market: market(),
        };
        const indexedPosition = {
            id: 'pos-btc-1',
            chainId: 11155111,
            account: '0x1111111111111111111111111111111111111111',
            token: '0x5555555555555555555555555555555555555555',
            isLong: false,
            size: (2000n * USD_30).toString(),
            collateral: (400n * USD_30).toString(),
            entryPrice: (65000n * USD_30).toString(),
            markPrice: (64000n * USD_30).toString(),
            pnl: (30n * USD_30).toString(),
            status: 'OPEN',
            txHash: '0xdef',
            createdAt: '2026-07-01T08:00:00.000Z',
            updatedAt: '2026-07-01T09:00:00.000Z',
        };
        const state = renderTradePage({
            portfolioPositions: [
                { kind: 'chain', position: chainPosition },
                { kind: 'indexed', position: indexedPosition, marketSymbol: 'BTC-USD' },
            ],
        });

        // Both sources render, count includes both markets.
        expect(screen.getByRole('button', { name: /Positions · 2/ })).toBeInTheDocument();
        expect(screen.getByTestId('position-row-chain')).toBeInTheDocument();
        const indexedRow = screen.getByTestId('position-row-indexed');
        expect(indexedRow.textContent).toContain('BTC-USD');
        // Indexed rows get 5.0× leverage (2000/400) and the stored pnl.
        expect(indexedRow.textContent).toContain('5.0×');

        // The chain row closes for real; the indexed row must NOT offer close —
        // it switches the terminal to its market instead.
        await user.click(screen.getByRole('button', { name: 'Close' }));
        expect(state.closePosition).toHaveBeenCalledWith(
            expect.objectContaining({ position: chainPosition }),
        );

        await user.click(screen.getByRole('button', { name: /Switch the terminal to BTC-USD/ }));
        expect(state.setSelectedMarketSymbol).toHaveBeenCalledWith('BTC-USD');
    });
});
