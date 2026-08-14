import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { parseUnits } from 'viem';
import { TOKENS } from '@wallet-trade/shared';
import { renderWithIntl } from '@/test/renderWithIntl';
import { BridgePage } from './BridgePage';

/**
 * The bridge is REAL now: BridgeGateway is deployed, the relayer runs, and
 * `executable` is derived from chain state (route() + paused() + destination
 * liquidity). The predecessor of this file guarded a hidden-roadmap page with a
 * hardcoded `BRIDGE_EXECUTION_SUPPORTED = false`; that gate is gone with its
 * subject.
 *
 * What replaces it is the honesty contract that makes showing the page safe:
 *  - execution is gated on chain state ALONE — no repo capability flag
 *  - a non-executable route lists EVERY reason, not a generic "unavailable"
 *  - the trusted-relayer disclosure must be acknowledged before the first send
 *  - a 409 at click time (liquidity dropped mid-poll) blocks the send and says so
 *  - a stuck transfer surfaces the relayer's verbatim error
 */

const mockGetBridgeRoutes = vi.fn();
const mockBuildBridgeDeposit = vi.fn();
const mockGetBridgeTransfers = vi.fn();
const mockExecute = vi.fn();
const mockApprove = vi.fn();
let mockIsApproved = true;
let mockIsConnected = true;

// vi.mock is hoisted above module-level declarations, so the class the factory
// returns must be hoisted with it — otherwise the factory runs before the class
// binding is initialized.
const { BridgeRouteUnavailableError } = vi.hoisted(() => ({
    BridgeRouteUnavailableError: class extends Error {
        constructor(message: string) {
            super(message);
            this.name = 'BridgeRouteUnavailableError';
        }
    },
}));

vi.mock('@/lib/api/bridge', () => ({
    BridgeRouteUnavailableError,
    getBridgeRoutes: (input: unknown) => mockGetBridgeRoutes(input),
    buildBridgeDeposit: (input: unknown) => mockBuildBridgeDeposit(input),
    getBridgeTransfers: (addr: string) => mockGetBridgeTransfers(addr),
}));

// The pre-sign review strip. Stubbed so these specs stay about route gating
// and execution, and so nothing here reaches the network.
const mockReview = vi.fn((_input?: unknown) => new Promise(() => {}));
vi.mock('@/lib/api/atlas', () => ({
    reviewSecurityTransaction: (input: unknown) => mockReview(input),
    explainSecurityTransaction: (input: unknown) => mockReview(input),
}));
vi.mock('../ProductStatus', () => ({
    useProductStatus: () => ({ data: { aiExplain: { status: 'disabled' } } }),
}));

vi.mock('wagmi', () => ({
    useChainId: () => 11155111,
    useAccount: () => ({ isConnected: mockIsConnected, address: '0xE2cd26322A87d2b6D8312DbCb79C38b7A226Ad81' }),
    createConfig: vi.fn(() => ({})),
    createStorage: vi.fn(() => ({})),
    cookieStorage: {},
    http: vi.fn(),
}));

vi.mock('@/hooks/useDisplayChainId', () => ({
    useDisplayChainId: () => ({ chainId: 11155111, isFallback: false }),
}));

vi.mock('@/hooks/web3/useTxFlow', () => ({
    useTxFlow: () => ({
        execute: mockExecute,
        reset: vi.fn(),
        stage: 'idle',
        hash: undefined,
        receipt: undefined,
        isWorking: false,
        error: null,
    }),
}));

vi.mock('@/hooks/web3/useTokenApproval', () => ({
    useTokenApproval: () => ({
        approve: mockApprove,
        isApproved: () => mockIsApproved,
        isPending: false,
        isConfirming: false,
    }),
}));

vi.mock('../AppContext', () => ({
    useApp: () => ({ toast: vi.fn(), openConnect: vi.fn(), walletState: 'connected' }),
}));

const SRC_TOKEN = '0x57e554d795a18f3ca0a0e9e03a17ac3c509c3bf8';
const GATEWAY = '0x2d68a51fb4c3f3ac26fa48b8a457d150132f185d';

function routeResponse(overrides: Record<string, unknown> = {}, routeOverrides: Record<string, unknown> = {}) {
    return {
        requestedAt: '2026-08-09T00:00:00Z',
        executionMode: 'live',
        executable: true,
        trustModel: 'trusted-relayer',
        chains: [11155111, 31337],
        routes: [
            {
                id: 'gw-11155111-31337-x',
                fromChainId: 11155111,
                toChainId: 31337,
                tokenSymbol: 'mUSDC',
                srcToken: SRC_TOKEN,
                dstToken: '0x2222222222222222222222222222222222222222',
                srcGateway: GATEWAY,
                dstGateway: '0x1111111111111111111111111111111111111111',
                amountIn: '1000000000000000000',
                amountOut: '1000000000000000000',
                minAmount: '0',
                executable: true,
                blockers: [],
                dstLiquidity: '5000000000000000000000',
                paused: false,
                trustModel: 'trusted-relayer',
                ...routeOverrides,
            },
        ],
        ...overrides,
    };
}

function render() {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    return renderWithIntl(
        <QueryClientProvider client={qc}>
            <BridgePage />
        </QueryClientProvider>,
    );
}

/** Type an amount so the routes query is enabled. */
async function enterAmount(value = '1') {
    fireEvent.change(screen.getByTestId('bridge-amount'), { target: { value } });
    await waitFor(() => expect(mockGetBridgeRoutes).toHaveBeenCalled());
}

beforeEach(() => {
    vi.clearAllMocks();
    mockIsApproved = true;
    mockIsConnected = true;
    window.localStorage.clear();
    mockGetBridgeRoutes.mockResolvedValue(routeResponse());
    mockBuildBridgeDeposit.mockResolvedValue({
        chainId: 11155111, to: GATEWAY, data: '0x8b6099db', value: '0',
        approvalTarget: GATEWAY, trustModel: 'trusted-relayer',
    });
    mockGetBridgeTransfers.mockResolvedValue([]);
});

afterEach(() => {
    window.localStorage.clear();
});

describe('BridgePage', () => {
    it('quotes routes in BASE UNITS, never a float amount', async () => {
        render();
        await enterAmount('1');

        // The token list comes from runtime contract config, so derive the
        // expectation from the same source rather than pinning an address.
        const token = (TOKENS[11155111] ?? [])[0];
        expect(token, 'sepolia token config must be loaded for this test').toBeTruthy();

        const [call] = mockGetBridgeRoutes.mock.calls[0];
        expect(call.amount).toBe(parseUnits('1', token.decimals).toString());
        expect(call.amount).not.toContain('.');
        expect(call.srcToken).toBe(token.address);
    });

    it('blocks the first send until the trust model is acknowledged', async () => {
        render();
        await enterAmount();

        await waitFor(() => expect(screen.getByTestId('bridge-trust')).toBeTruthy());
        // Executable route, but the disclosure has not been read yet.
        expect(screen.getByTestId('bridge-cta-disabled')).toBeTruthy();

        fireEvent.click(screen.getByTestId('bridge-trust-ack'));
        await waitFor(() => expect(screen.getByTestId('bridge-cta-deposit')).toBeTruthy());
    });

    it('states the trusted-relayer risks explicitly, not as a generic disclaimer', async () => {
        render();
        await enterAmount();

        const trust = await screen.findByTestId('bridge-trust');
        // The three real properties from BridgeGateway.sol's trust model.
        expect(trust.textContent).toMatch(/drain destination liquidity/i);
        expect(trust.textContent).toMatch(/stranded/i);
        expect(trust.textContent).toMatch(/admin can withdraw/i);
    });

    it('lists EVERY blocker, not just the first', async () => {
        mockGetBridgeRoutes.mockResolvedValue(
            routeResponse({ executable: false, executionMode: 'unavailable' }, {
                executable: false,
                blockers: ['NO_DESTINATION_GATEWAY', 'ROUTE_NOT_CONFIGURED'],
                dstLiquidity: '',
            }),
        );
        render();
        await enterAmount();

        await waitFor(() => expect(screen.getByTestId('bridge-blockers')).toBeTruthy());
        expect(screen.getByTestId('bridge-blocker-NO_DESTINATION_GATEWAY')).toBeTruthy();
        expect(screen.getByTestId('bridge-blocker-ROUTE_NOT_CONFIGURED')).toBeTruthy();
        expect(screen.getByTestId('bridge-cta-disabled')).toBeTruthy();
    });

    it('renders blockers as prose, never as a raw machine code', async () => {
        mockGetBridgeRoutes.mockResolvedValue(
            routeResponse({ executable: false }, { executable: false, blockers: ['GATEWAY_PAUSED'] }),
        );
        render();
        await enterAmount();

        const el = await screen.findByTestId('bridge-blocker-GATEWAY_PAUSED');
        expect(el.textContent).not.toContain('GATEWAY_PAUSED');
        expect(el.textContent).toMatch(/paused/i);
    });

    it('reports unknown destination liquidity as unknown, never as zero', async () => {
        mockGetBridgeRoutes.mockResolvedValue(
            routeResponse({ executable: false }, {
                executable: false,
                blockers: ['DESTINATION_LIQUIDITY_UNKNOWN'],
                dstLiquidity: '',
            }),
        );
        render();
        await enterAmount();

        const liq = await screen.findByTestId('bridge-liquidity');
        expect(liq.textContent).toMatch(/unknown/i);
        expect(liq.textContent).not.toMatch(/^0/);
    });

    it('re-checks with the server at click time and refuses a stale route', async () => {
        render();
        await enterAmount();
        fireEvent.click(await screen.findByTestId('bridge-trust-ack'));

        // Liquidity dropped between the 30s poll and the click.
        mockBuildBridgeDeposit.mockRejectedValue(new BridgeRouteUnavailableError('gone'));
        fireEvent.click(await screen.findByTestId('bridge-cta-deposit'));

        await waitFor(() => expect(screen.getByTestId('bridge-stale')).toBeTruthy());
        // The send must NOT have happened.
        expect(mockExecute).not.toHaveBeenCalled();
    });

    it('sends the deposit only after the server confirms the route', async () => {
        render();
        await enterAmount();
        fireEvent.click(await screen.findByTestId('bridge-trust-ack'));
        fireEvent.click(await screen.findByTestId('bridge-cta-deposit'));

        await waitFor(() => expect(mockExecute).toHaveBeenCalled());
        expect(mockBuildBridgeDeposit).toHaveBeenCalled();
        const call = mockExecute.mock.calls[0][0];
        expect(call.functionName).toBe('deposit');
        expect(call.address).toBe(GATEWAY);
        // args: [srcToken, amount, dstChainId, recipient]
        expect(call.args[1]).toBe(1000000000000000000n);
        expect(call.args[2]).toBe(31337n);
    });

    it('asks for approval before the deposit when allowance is short', async () => {
        mockIsApproved = false;
        render();
        await enterAmount();
        fireEvent.click(await screen.findByTestId('bridge-trust-ack'));

        fireEvent.click(await screen.findByTestId('bridge-cta-approve'));
        expect(mockApprove).toHaveBeenCalledWith(1000000000000000000n);
        expect(mockExecute).not.toHaveBeenCalled();
    });

    // The deterministic bridge-deposit checks exist to be consulted from the
    // real flow, not only from the Security console. This pins that the page
    // asks about the transfer the CTA would actually send.
    it('runs the bridge-deposit review for the transfer it is about to send', async () => {
        render();
        await enterAmount();

        await waitFor(() => expect(mockReview).toHaveBeenCalled());
        const input = mockReview.mock.calls[0][0] as Record<string, unknown>;
        expect(input.operationType).toBe('bridge-deposit');
        expect(input.chainId).toBe(11155111);
        expect(input.bridgeDstChainId).toBe(31337);
        // Base units, matching what deposit() receives on-chain.
        expect(input.amount).toBe('1000000000000000000');
    });

    it('reviews nothing before the form describes a transfer', async () => {
        render();
        // No amount entered — routes aren't even quoted yet, and there is no
        // transaction to have a verdict about.
        await screen.findByTestId('bridge-amount');
        expect(mockReview).not.toHaveBeenCalled();
    });

    it('surfaces a stuck transfer verbatim instead of an indefinite pending', async () => {
        mockGetBridgeTransfers.mockResolvedValue([
            {
                transferId: '0x' + 'ab'.repeat(32),
                status: 'INITIATED',
                srcChainId: 11155111, dstChainId: 31337,
                sender: '0xe2cd', recipient: '0xe2cd',
                srcToken: SRC_TOKEN, dstToken: '0x22',
                amount: '1000000000000000000',
                depositTxHash: '0xdead', depositLogIndex: 0,
                depositedAt: '2026-08-09T00:00:00Z',
                fulfilledAt: null, refundedAt: null,
                lastError: 'insufficient destination liquidity: have 5, need 1000',
                attempts: 3,
                updatedAt: '2026-08-09T00:01:00Z',
            },
        ]);
        render();

        const err = await screen.findByTestId('bridge-transfer-error');
        expect(err.textContent).toContain('insufficient destination liquidity');
    });

    it('offers connect instead of a dead control when no wallet is attached', async () => {
        mockIsConnected = false;
        render();
        await enterAmount();

        expect(screen.getByTestId('bridge-cta-connect')).toBeTruthy();
        expect(screen.queryByTestId('bridge-cta-deposit')).toBeNull();
    });
});
