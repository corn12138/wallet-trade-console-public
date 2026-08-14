import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import { renderWithIntl } from '@/test/renderWithIntl';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import en from '../../../../../../services/api-go/internal/i18n/baseline/en.json';
import { SwapPage } from './SwapPage';

/**
 * Executable-quote gating for /swap:
 *  - a fallback quote (executable:false / routeSource "fallback") must render
 *    a disabled CTA and never reach useTxFlow.execute — even when the wallet
 *    is connected and the token is already approved
 *  - a live router quote (executable:true) with approval in place submits the
 *    real swapExactTokensForTokens write
 *  - NEXT_PUBLIC_ALLOW_FALLBACK_SWAP_EXECUTION is dev-only: in production
 *    builds (NODE_ENV=production) it must NOT make fallback quotes executable
 */

const mockGetSwapQuote = vi.fn();
const mockExecute = vi.fn();
const mockOpenConnect = vi.fn();

const mockReview = vi.fn();

vi.mock('@/lib/api/atlas', () => ({
    getSwapQuote: (input: unknown) => mockGetSwapQuote(input),
    reviewSecurityTransaction: (input: unknown) => mockReview(input),
    explainSecurityTransaction: (input: unknown) => mockReview(input),
}));

// The pre-sign review strip asks the backend whether the explanation layer is
// live. Stubbed here so these specs stay about quote gating and hit no network.
vi.mock('../ProductStatus', () => ({
    useProductStatus: () => ({ data: { aiExplain: { status: 'disabled' } } }),
}));

vi.mock('wagmi', () => ({
    useChainId: () => 11155111,
    useAccount: () => ({ address: '0x1111111111111111111111111111111111111111', isConnected: true }),
    createConfig: vi.fn(() => ({})),
    createStorage: vi.fn(() => ({})),
    cookieStorage: {},
    http: vi.fn(),
}));

vi.mock('@wallet-trade/shared', async (importOriginal) => ({
    ...(await importOriginal<Record<string, unknown>>()),
    TOKENS: {
        11155111: [
            { symbol: 'USDC', name: 'Mock USDC', address: '0x57e554d795a18f3ca0a0e9e03a17ac3c509c3bf8', decimals: 18 },
            { symbol: 'WETH', name: 'Mock WETH', address: '0xffea240cd1eb135c8aa2597ca203efd629ac5fcd', decimals: 18 },
        ],
    },
}));

vi.mock('../AppContext', () => ({
    useApp: () => ({
        chainId: 11155111,
        walletState: 'connected',
        openConnect: mockOpenConnect,
        toast: vi.fn(),
        setModal: vi.fn(),
        switchChain: vi.fn(),
    }),
}));

const mockGetOptionalContractAddress = vi.fn(
    (): string | undefined => '0xe0c55ff91ece0acc7ddafa9069ba5b0cdbbc3b00',
);
vi.mock('@/lib/web3/contracts', () => ({
    getOptionalContractAddress: (...args: unknown[]) => mockGetOptionalContractAddress(...(args as [])),
    routerAbi: [],
}));

vi.mock('@/hooks/useDisplayChainId', () => ({
    useDisplayChainId: () => ({ chainId: 11155111, isFallback: false }),
}));

vi.mock('@/hooks/web3/useTokenApproval', () => ({
    useTokenApproval: () => ({
        isApproved: () => true,
        approve: vi.fn(),
        isPending: false,
        isConfirming: false,
    }),
}));

vi.mock('@/hooks/web3/useTxFlow', () => ({
    useTxFlow: () => ({
        execute: mockExecute,
        stage: 'idle',
        isWorking: false,
    }),
}));

vi.mock('@/lib/trading-defaults', () => ({
    useTradingDefaults: () => ({ slippagePercent: '0.5', deadlineMinutes: '20' }),
}));

const baseQuote = {
    chainId: 11155111,
    amountIn: '100',
    amountOut: '99.7',
    amountOutRaw: '0',
    minimumReceived: '99.2',
    minimumReceivedRaw: '0',
    priceImpactPct: 0,
    slippageBps: 50,
    path: ['0x57e554d795a18f3ca0a0e9e03a17ac3c509c3bf8', '0xffea240cd1eb135c8aa2597ca203efd629ac5fcd'],
};

function renderSwap() {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    return renderWithIntl(
        <QueryClientProvider client={client}>
            <SwapPage />
        </QueryClientProvider>,
    );
}

describe('SwapPage quote gating', () => {
    beforeEach(() => {
        vi.clearAllMocks();
        // A review that never resolves keeps the strip in its pending state, so
        // these specs observe the CTA gating without the strip's own rendering.
        mockReview.mockReturnValue(new Promise(() => {}));
        mockGetOptionalContractAddress.mockReturnValue('0xe0c55ff91ece0acc7ddafa9069ba5b0cdbbc3b00');
        vi.stubEnv('NEXT_PUBLIC_ALLOW_FALLBACK_SWAP_EXECUTION', '');
    });
    afterEach(() => {
        vi.unstubAllEnvs();
    });

    it('disables the swap CTA on a non-executable fallback quote and never executes', async () => {
        mockGetSwapQuote.mockResolvedValue({
            ...baseQuote,
            routeSource: 'fallback',
            quoteStatus: 'fallback',
            executable: false,
            routerAddress: null,
            warnings: ['Live quote unavailable, using fallback estimate'],
        });

        renderSwap();

        const cta = await screen.findByRole('button', {
            name: /Live quote unavailable — swap disabled/,
        });
        expect(cta).toBeDisabled();

        fireEvent.click(cta);
        expect(mockExecute).not.toHaveBeenCalled();
        expect(screen.queryByRole('button', { name: /^Swap 100/ })).not.toBeInTheDocument();
    });

    it('ignores the fallback-execution override in production builds', async () => {
        vi.stubEnv('NODE_ENV', 'production');
        vi.stubEnv('NEXT_PUBLIC_ALLOW_FALLBACK_SWAP_EXECUTION', '1');
        mockGetSwapQuote.mockResolvedValue({
            ...baseQuote,
            routeSource: 'fallback',
            quoteStatus: 'fallback',
            executable: false,
            routerAddress: null,
            warnings: ['Live quote unavailable, using fallback estimate'],
        });

        renderSwap();

        // Even with the override env var set, production must keep the CTA
        // disabled and never render an enabled swap button.
        const cta = await screen.findByRole('button', {
            name: /Live quote unavailable — swap disabled/,
        });
        expect(cta).toBeDisabled();
        fireEvent.click(cta);
        expect(mockExecute).not.toHaveBeenCalled();
        expect(screen.queryByRole('button', { name: /^Swap 100/ })).not.toBeInTheDocument();
    });

    it('honors the override for local demos outside production', async () => {
        vi.stubEnv('NODE_ENV', 'development');
        vi.stubEnv('NEXT_PUBLIC_ALLOW_FALLBACK_SWAP_EXECUTION', '1');
        mockGetSwapQuote.mockResolvedValue({
            ...baseQuote,
            routeSource: 'fallback',
            quoteStatus: 'fallback',
            executable: false,
            routerAddress: null,
            warnings: ['Live quote unavailable, using fallback estimate'],
        });

        renderSwap();

        // Dev-only escape hatch: the swap CTA becomes available.
        await screen.findByRole('button', { name: /^Swap 100 USDC/ });
        expect(
            screen.queryByRole('button', { name: /Live quote unavailable — swap disabled/ }),
        ).not.toBeInTheDocument();
    });

    it('submits swapExactTokensForTokens from an executable live quote', async () => {
        mockGetSwapQuote.mockResolvedValue({
            ...baseQuote,
            routeSource: 'router',
            quoteStatus: 'live',
            executable: true,
            routerAddress: '0xe0c55ff91ece0acc7ddafa9069ba5b0cdbbc3b00',
            warnings: [],
        });

        renderSwap();

        const cta = await screen.findByRole('button', { name: /^Swap 100 USDC/ });
        fireEvent.click(cta);

        await waitFor(() => expect(mockExecute).toHaveBeenCalledTimes(1));
        const call = mockExecute.mock.calls[0][0];
        expect(call.functionName).toBe('swapExactTokensForTokens');
        expect(call.address).toBe('0xe0c55ff91ece0acc7ddafa9069ba5b0cdbbc3b00');
    });

    it('shows the live diagnostics status for an executable router quote', async () => {
        mockGetSwapQuote.mockResolvedValue({
            ...baseQuote,
            routeSource: 'router',
            quoteStatus: 'live',
            executable: true,
            routerAddress: '0xe0c55ff91ece0acc7ddafa9069ba5b0cdbbc3b00',
            warnings: [],
        });

        renderSwap();

        await waitFor(() =>
            expect(screen.getByTestId('swap-quote-status').textContent).toBe('live · executable'),
        );
        expect(screen.queryByTestId('swap-fallback-panel')).not.toBeInTheDocument();
    });

    it('renders the router-not-configured state when no router is deployed', async () => {
        mockGetOptionalContractAddress.mockReturnValue(undefined);
        mockGetSwapQuote.mockResolvedValue({
            ...baseQuote,
            routeSource: 'fallback',
            quoteStatus: 'fallback',
            executable: false,
            routerAddress: null,
            warnings: [],
        });

        renderSwap();

        const cta = await screen.findByRole('button', {
            name: /Router not configured on chain 11155111/,
        });
        expect(cta).toBeDisabled();
        expect(screen.getByTestId('swap-quote-status').textContent).toBe('router not configured');
        expect(mockExecute).not.toHaveBeenCalled();
    });

    it('explains the fallback state with API reasons and a retry action', async () => {
        mockGetSwapQuote.mockResolvedValue({
            ...baseQuote,
            routeSource: 'fallback',
            quoteStatus: 'fallback',
            executable: false,
            routerAddress: null,
            warnings: ['Live quote unavailable, using fallback estimate'],
        });

        renderSwap();

        const panel = await screen.findByTestId('swap-fallback-panel');
        expect(panel.textContent).toContain('Why is swap disabled?');
        expect(panel.textContent).toContain('Live quote unavailable, using fallback estimate');
        expect(screen.getByTestId('swap-quote-status').textContent).toBe('fallback · display-only');

        const before = mockGetSwapQuote.mock.calls.length;
        fireEvent.click(screen.getByTestId('swap-retry-quote'));
        await waitFor(() => expect(mockGetSwapQuote.mock.calls.length).toBeGreaterThan(before));
    });

    it('localizes the flip control and passes warnings through the diagnostics layer', async () => {
        mockGetSwapQuote.mockResolvedValue({
            ...baseQuote,
            routeSource: 'fallback',
            quoteStatus: 'fallback',
            executable: false,
            routerAddress: null,
            warnings: [
                'Live quote unavailable, using fallback estimate',
                'Custom upstream note not in the known map',
            ],
        });

        renderSwap();
        await screen.findByTestId('swap-fallback-panel');

        // The flip tooltip/aria-label must come from messages — a hardcoded
        // data-tip="Flip" regression makes this name lookup fail.
        const flip = screen.getByRole('button', { name: en.swap.flipAction });
        expect(flip.getAttribute('data-tip')).toBe(en.swap.flipAction);

        // Known warning renders the en.json diagnostic copy; the unknown one
        // stays visible raw (English mode) instead of being dropped.
        expect(
            screen.getAllByText(new RegExp(en.diagnostics.messages.swapFallbackEstimate)).length,
        ).toBeGreaterThan(0);
        expect(
            screen.getAllByText(/Custom upstream note not in the known map/).length,
        ).toBeGreaterThan(0);
    });

    it('classifies a failed quote request and offers retry', async () => {
        mockGetSwapQuote.mockRejectedValue(new Error('HTTP 500'));

        renderSwap();

        const errorPanel = await screen.findByTestId('swap-quote-error');
        expect(errorPanel.textContent).toContain('Quote API unreachable');
        expect(screen.getByTestId('swap-quote-status').textContent).toBe('quote API unreachable');
        expect(screen.queryByRole('button', { name: /^Swap 100/ })).not.toBeInTheDocument();

        const before = mockGetSwapQuote.mock.calls.length;
        fireEvent.click(screen.getByTestId('swap-retry-quote'));
        await waitFor(() => expect(mockGetSwapQuote.mock.calls.length).toBeGreaterThan(before));
    });
});
