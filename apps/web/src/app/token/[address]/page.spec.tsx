import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render } from '@testing-library/react';
import { IntlWrapper } from '@/test/renderWithIntl';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import TokenDetailPage from './page';

/**
 * Regression guard for /token/[address] chain policy: with no wallet
 * connected, on-chain reads (bonding curve, price, reserves) must target the
 * Sepolia display chain — not wagmi's first-configured chain (mainnet 1).
 */

const TOKEN = '0x00000000000000000000000000000000000000bb';
const readContractCalls: Array<Record<string, unknown>> = [];
const mockUseChainId = vi.fn();
const mockUseAccount = vi.fn();

vi.mock('wagmi', () => ({
    useChainId: () => mockUseChainId(),
    useAccount: () => mockUseAccount(),
    useReadContract: (config: Record<string, unknown>) => {
        readContractCalls.push(config);
        return { data: undefined, refetch: vi.fn() };
    },
    useWriteContract: () => ({ writeContract: vi.fn(), data: undefined, isPending: false, error: null }),
    useWaitForTransactionReceipt: () => ({ isLoading: false, isSuccess: false, data: undefined }),
}));

vi.mock('next/navigation', () => ({
    useParams: () => ({ address: TOKEN }),
}));

vi.mock('@wallet-trade/shared', async (importOriginal) => ({
    ...(await importOriginal<Record<string, unknown>>()),
    TOKENS: { 11155111: [{ symbol: 'USDC' }] },
}));

vi.mock('@/hooks/web3/usePersistTransactionLifecycle', () => ({
    usePersistTransactionLifecycle: vi.fn(),
}));

vi.mock('@/components/token/PriceChart', () => ({
    PriceChart: () => <div data-testid="price-chart" />,
}));
vi.mock('@/components/token/TrendingTicker', () => ({
    TrendingTicker: () => <div />,
}));
vi.mock('./TokenDetailHeader', () => ({ TokenDetailHeader: () => <div /> }));
vi.mock('./TokenDetailSidebar', () => ({ TokenDetailSidebar: () => <div /> }));
vi.mock('./TokenInfoTabs', () => ({ TokenInfoTabs: () => <div /> }));
vi.mock('./TokenTradePanel', () => ({ TokenTradePanel: () => <div /> }));
vi.mock('./TokenBondingCurveCard', () => ({ TokenBondingCurveCard: () => <div /> }));

function renderPage() {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    return render(
        <IntlWrapper>
            <QueryClientProvider client={client}>
                <TokenDetailPage />
            </QueryClientProvider>
        </IntlWrapper>,
    );
}

describe('/token/[address] chain policy', () => {
    beforeEach(() => {
        vi.clearAllMocks();
        readContractCalls.length = 0;
        vi.stubGlobal('fetch', vi.fn().mockResolvedValue({
            ok: true,
            json: async () => ({ id: 't1', symbol: 'SEED', name: 'Seed Token', bondingCurve: null }),
        } as Response));
    });

    it('targets Sepolia for on-chain reads when no wallet is connected', () => {
        mockUseChainId.mockReturnValue(1);
        mockUseAccount.mockReturnValue({ isConnected: false, address: undefined });

        renderPage();

        const readChainIds = readContractCalls
            .filter((c) => c.functionName !== 'allowance')
            .map((c) => c.chainId);
        expect(readChainIds.length).toBeGreaterThan(0);
        expect(readChainIds.every((id) => id === 11155111)).toBe(true);
    });

    it('follows the wallet chain for reads once connected', () => {
        mockUseChainId.mockReturnValue(31337);
        mockUseAccount.mockReturnValue({
            isConnected: true,
            address: '0x1111111111111111111111111111111111111111',
        });

        renderPage();

        const readChainIds = readContractCalls.map((c) => c.chainId);
        expect(readChainIds.length).toBeGreaterThan(0);
        expect(readChainIds.every((id) => id === 31337)).toBe(true);
    });
});
