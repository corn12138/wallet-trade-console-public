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
const mockOpenConnect = vi.fn();
const mockSetModal = vi.fn();

// The shared engine is mocked wholesale, the way useTxFlow was: the wagmi stub
// below has no write hooks. `capturedBuild` is the page's freeze function, so
// a spec can call it and assert the exact frozen bytes without a wallet.
const mockReviewIntent = vi.fn<() => string | null>(() => null);
const mockSendIntent = vi.fn(async () => undefined);
const intentState = {
    state: 'DRAFT' as string,
    phase: 'review' as string,
    busy: false,
};
let capturedBuild: ((now: number) => unknown) | null = null;

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
        setModal: mockSetModal,
        switchChain: vi.fn(),
    }),
}));

const mockGetOptionalContractAddress = vi.fn(
    (): string | undefined => '0xe0c55ff91ece0acc7ddafa9069ba5b0cdbbc3b00',
);
vi.mock('@/lib/web3/contracts', () => ({
    getOptionalContractAddress: (...args: unknown[]) => mockGetOptionalContractAddress(...(args as [])),
    // Just enough of the real router ABI for the page to freeze a real
    // swapExactTokensForTokens call, so a spec can assert the frozen bytes.
    routerAbi: [
        {
            type: 'function',
            name: 'swapExactTokensForTokens',
            stateMutability: 'nonpayable',
            inputs: [
                { type: 'uint256', name: 'amountIn' },
                { type: 'uint256', name: 'amountOutMin' },
                { type: 'address[]', name: 'path' },
                { type: 'address', name: 'to' },
                { type: 'uint256', name: 'deadline' },
            ],
            outputs: [{ type: 'uint256[]', name: 'amounts' }],
        },
    ],
}));

vi.mock('@/hooks/useDisplayChainId', () => ({
    useDisplayChainId: () => ({ chainId: 11155111, isFallback: false }),
}));

// Mutable so a spec can start from a wallet that has NOT approved the router.
const approvalState = { approved: true };
vi.mock('@/hooks/web3/useTokenApproval', () => ({
    useTokenApproval: () => ({
        allowance: approvalState.approved ? 10n ** 30n : 0n,
        isApproved: () => approvalState.approved,
        approve: vi.fn(),
        isPending: false,
        isConfirming: false,
        isSuccess: false,
    }),
}));

// Balance for the pay side. Mutable so individual specs can drain the wallet;
// mocked because the real hook reads wagmi's useBalance/useReadContract, which
// the wagmi stub above does not provide.
const mockBalanceState = { balance: 1000n * 10n ** 18n as bigint | undefined };
vi.mock('@/hooks/web3/useTokenBalance', () => ({
    useTokenBalance: () => ({
        balance: mockBalanceState.balance,
        formatted:
            mockBalanceState.balance !== undefined
                ? (Number(mockBalanceState.balance) / 1e18).toString()
                : '0',
        symbol: 'USDC',
        decimals: 18,
        isLoading: false,
        error: null,
    }),
}));

vi.mock('@/hooks/web3/txIntent/useTxIntent', () => ({
    useTxIntent: (options: { build: (now: number) => unknown }) => {
        capturedBuild = options.build;
        return {
            state: intentState.state,
            phase: intentState.phase,
            steps: [],
            activeStepIndex: 0,
            activeStep: null,
            hash: null,
            replacedByHash: null,
            confirmations: 0,
            errorCode: null,
            invalidationReason: null,
            complete: false,
            review: mockReviewIntent,
            send: mockSendIntent,
            reset: vi.fn(),
            busy: intentState.busy,
            wrongChain: false,
            receipt: undefined,
            rawError: null,
            indexing: 'not-expected',
            now: Date.now,
        };
    },
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

function renderSwap({ amountIn = '100' }: { amountIn?: string } = {}) {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const view = renderWithIntl(
        <QueryClientProvider client={client}>
            <SwapPage />
        </QueryClientProvider>,
    );
    // The amount field starts empty now (no arbitrary pre-seeded trade); these
    // specs type the amount the old default used to hardcode.
    fireEvent.change(screen.getByTestId('swap-amount-in'), { target: { value: amountIn } });
    return view;
}

describe('SwapPage quote gating', () => {
    beforeEach(() => {
        vi.clearAllMocks();
        intentState.state = 'DRAFT';
        intentState.phase = 'review';
        intentState.busy = false;
        capturedBuild = null;
        approvalState.approved = true;
        mockReviewIntent.mockReturnValue(null);
        // A review that never resolves keeps the strip in its pending state, so
        // these specs observe the CTA gating without the strip's own rendering.
        mockReview.mockReturnValue(new Promise(() => {}));
        mockGetOptionalContractAddress.mockReturnValue('0xe0c55ff91ece0acc7ddafa9069ba5b0cdbbc3b00');
        mockBalanceState.balance = 1000n * 10n ** 18n;
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
        expect(screen.queryByDisplayValue('99.7')).not.toBeInTheDocument();
        expect(screen.queryByText(/99.2 USDC/)).not.toBeInTheDocument();

        fireEvent.click(cta);
        expect(mockReviewIntent).not.toHaveBeenCalled();
        expect(mockSendIntent).not.toHaveBeenCalled();
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
        expect(mockReviewIntent).not.toHaveBeenCalled();
        expect(mockSendIntent).not.toHaveBeenCalled();
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

    it('freezes a real swapExactTokensForTokens step from an executable live quote, and only reviews on click', async () => {
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

        // One click reviews. The wallet is only asked from the modal's Sign.
        await waitFor(() => expect(mockReviewIntent).toHaveBeenCalledTimes(1));
        expect(mockSendIntent).not.toHaveBeenCalled();

        // The page's freeze function produces the exact bytes the wallet will
        // get: the router as target, the swap selector, an exact (not
        // unlimited) input amount, and a frozen deadline.
        expect(capturedBuild).toBeTypeOf('function');
        const steps = capturedBuild!(Date.now()) as Array<{
            kind: string; actionType: string; target: string; calldata: string; expectsIndexing: boolean;
            review: { token?: { amount: bigint }; guard?: { deadline?: bigint; minAmountOut?: bigint } };
        }>;
        expect(steps).toHaveLength(1);
        const swap = steps[0];
        expect(swap.kind).toBe('action');
        expect(swap.actionType).toBe('swap');
        expect(swap.target).toBe('0xe0c55ff91ece0acc7ddafa9069ba5b0cdbbc3b00');
        expect(swap.calldata.startsWith('0x38ed1739')).toBe(true); // swapExactTokensForTokens selector
        expect(swap.review.token?.amount).toBe(100n * 10n ** 18n);
        expect(swap.review.guard?.minAmountOut).toBe(992n * 10n ** 17n);
        expect(swap.review.guard?.deadline).toBeTypeOf('bigint');
        expect(swap.expectsIndexing).toBe(true);
    });

    it('with no allowance, the first step is an approve for EXACTLY the input amount — never unlimited — followed by the swap', async () => {
        approvalState.approved = false;
        mockGetSwapQuote.mockResolvedValue({
            ...baseQuote,
            routeSource: 'router',
            quoteStatus: 'live',
            executable: true,
            routerAddress: '0xe0c55ff91ece0acc7ddafa9069ba5b0cdbbc3b00',
            warnings: [],
        });

        renderSwap();

        // The approve CTA is visible while the quote is still loading, and a
        // click then is a no-op (the page never freezes without a live quote).
        // Wait for the live quote so the click is the real one.
        await waitFor(() => expect(screen.getByTestId('swap-quote-status').textContent).toBe('live · executable'));
        const cta = await screen.findByTestId('swap-cta-approve');
        fireEvent.click(cta);
        await waitFor(() => expect(mockReviewIntent).toHaveBeenCalledTimes(1));

        const steps = capturedBuild!(Date.now()) as Array<{
            kind: string; target: string; calldata: string; expectsIndexing: boolean;
            review: { token?: { amount: bigint } };
        }>;
        expect(steps.map((s) => s.kind)).toEqual(['approve', 'action']);
        const approve = steps[0];
        expect(approve.target).toBe('0x57e554d795a18f3ca0a0e9e03a17ac3c509c3bf8'); // the token, not the router
        expect(approve.calldata.startsWith('0x095ea7b3')).toBe(true); // approve(address,uint256)
        // The encoded amount is the exact input: 100e18, not 2^256-1.
        const encodedAmount = BigInt('0x' + approve.calldata.slice(10 + 64, 10 + 128));
        expect(encodedAmount).toBe(100n * 10n ** 18n);
        expect(approve.expectsIndexing).toBe(false);
    });

    it('a busy engine disables the CTA so a second prompt cannot become a second transaction', async () => {
        mockGetSwapQuote.mockResolvedValue({
            ...baseQuote,
            routeSource: 'router',
            quoteStatus: 'live',
            executable: true,
            routerAddress: '0xe0c55ff91ece0acc7ddafa9069ba5b0cdbbc3b00',
            warnings: [],
        });
        intentState.state = 'AWAITING_WALLET';
        intentState.phase = 'wallet';
        intentState.busy = true;

        renderSwap();

        const busy = await screen.findByTestId('swap-cta-busy');
        expect(busy).toBeDisabled();
        fireEvent.click(busy);
        expect(mockReviewIntent).not.toHaveBeenCalled();
        expect(mockSendIntent).not.toHaveBeenCalled();
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
        expect(mockReviewIntent).not.toHaveBeenCalled();
        expect(mockSendIntent).not.toHaveBeenCalled();
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
        expect(screen.getByTestId('swap-quote-status').textContent).toBe('fallback · unavailable');

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

    it('blocks the swap with an insufficient-balance CTA when the wallet cannot cover it', async () => {
        mockBalanceState.balance = 1n; // effectively drained
        mockGetSwapQuote.mockResolvedValue({
            ...baseQuote,
            routeSource: 'router',
            quoteStatus: 'live',
            executable: true,
            routerAddress: '0xe0c55ff91ece0acc7ddafa9069ba5b0cdbbc3b00',
            warnings: [],
        });

        renderSwap();

        const cta = await screen.findByTestId('swap-cta-insufficient');
        expect(cta).toBeDisabled();
        expect(cta.textContent).toContain('Insufficient USDC balance');
        expect(screen.queryByTestId('swap-cta-submit')).not.toBeInTheDocument();
        expect(mockReviewIntent).not.toHaveBeenCalled();
        expect(mockSendIntent).not.toHaveBeenCalled();
    });

    it('fills the amount from the balance Max button', async () => {
        mockGetSwapQuote.mockResolvedValue({
            ...baseQuote,
            routeSource: 'router',
            quoteStatus: 'live',
            executable: true,
            routerAddress: '0xe0c55ff91ece0acc7ddafa9069ba5b0cdbbc3b00',
            warnings: [],
        });

        renderSwap();

        fireEvent.click(screen.getByTestId('swap-balance-max'));
        expect((screen.getByTestId('swap-amount-in') as HTMLInputElement).value).toBe('1000');
    });

    it('requires an explicit acknowledgement before a high-impact swap arms', async () => {
        mockGetSwapQuote.mockResolvedValue({
            ...baseQuote,
            priceImpactPct: 8.4,
            routeSource: 'router',
            quoteStatus: 'live',
            executable: true,
            routerAddress: '0xe0c55ff91ece0acc7ddafa9069ba5b0cdbbc3b00',
            warnings: [],
        });

        renderSwap();

        const blocked = await screen.findByTestId('swap-cta-impact-blocked');
        expect(blocked).toBeDisabled();
        expect(screen.getByTestId('swap-impact-panel')).toBeInTheDocument();

        fireEvent.click(screen.getByTestId('swap-impact-ack'));

        const cta = await screen.findByTestId('swap-cta-submit');
        fireEvent.click(cta);
        // Acknowledged impact lets the click reach the engine's review — and
        // only review; the wallet is asked from the modal, never from the CTA.
        await waitFor(() => expect(mockReviewIntent).toHaveBeenCalledTimes(1));
        expect(mockSendIntent).not.toHaveBeenCalled();
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
