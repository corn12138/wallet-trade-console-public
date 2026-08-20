import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen } from '@testing-library/react';
import { IntlWrapper } from '@/test/renderWithIntl';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import AdvancedEarnPage from './AdvancedEarnPage';

/**
 * Advanced Earn wallet projection: `Your staked` / `Pending rewards` must be
 * REAL chain reads (StakingPool.stakedBalance / earned) for the connected
 * wallet — not `—` placeholders — and honest `—` when no wallet is connected.
 */

const mockUseAccount = vi.fn();
const mockUseReadContract = vi.fn();

vi.mock('wagmi', () => ({
    useAccount: () => mockUseAccount(),
    useChainId: () => 11155111,
    useReadContract: (config: { functionName: string; query?: { enabled?: boolean } }) =>
        mockUseReadContract(config),
}));

vi.mock('@/lib/web3/contracts', () => ({
    CONTRACT_ABIS: { StakingPool: [] },
    erc20Abi: [],
    getOptionalContractAddress: () => '0x87ef5d972687f107a3f81c6e9b2463787c585c00',
}));

vi.mock('@/hooks/useDisplayChainId', () => ({
    useDisplayChainId: () => ({ chainId: 11155111, isFallback: false }),
}));

vi.mock('@/hooks/web3/useTokenApproval', () => ({
    useTokenApproval: () => ({ isApproved: () => true, approve: vi.fn(), isPending: false, isConfirming: false }),
}));

vi.mock('@/hooks/web3/useTxFlow', () => ({
    useTxFlow: () => ({ execute: vi.fn(), stage: 'idle', isWorking: false }),
}));

vi.mock('@/app/_atlas/AppContext', () => ({
    useApp: () => ({ walletState: 'connected', toast: vi.fn(), openConnect: vi.fn() }),
}));

vi.mock('@/app/_atlas/Icon', () => ({
    Icon: () => null,
}));

vi.mock('@/app/_atlas/Common', () => ({
    PageHeader: () => null,
    TabBar: () => null,
    Empty: ({ body }: { body: string }) => <div>{body}</div>,
    MetricCard: ({ label, value, sub }: { label: string; value: string; sub?: string }) => (
        <div data-testid={`metric-${label}`}>
            <span>{label}</span>
            <strong>{value}</strong>
            <em>{sub}</em>
        </div>
    ),
}));

function renderPage() {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    return render(
        <IntlWrapper>
            <QueryClientProvider client={client}>
                <AdvancedEarnPage />
            </QueryClientProvider>
        </IntlWrapper>,
    );
}

describe('AdvancedEarnPage wallet staking metrics', () => {
    beforeEach(() => {
        vi.clearAllMocks();
        vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: false }));
    });
    afterEach(() => {
        vi.unstubAllGlobals();
    });

    it('renders real stakedBalance/earned values for a connected wallet', () => {
        mockUseAccount.mockReturnValue({
            address: '0x1111111111111111111111111111111111111111',
            isConnected: true,
        });
        mockUseReadContract.mockImplementation(({ functionName }: { functionName: string }) => {
            if (functionName === 'stakedBalance') {
                return { data: 5_000_000_000_000_000_000n, refetch: vi.fn() }; // 5 STK
            }
            if (functionName === 'earned') {
                return { data: 2_500_000_000_000_000_000n, refetch: vi.fn() }; // 2.5 RWD
            }
            return { data: undefined, refetch: vi.fn() };
        });

        renderPage();

        const staked = screen.getByTestId('metric-Your staked');
        expect(staked).toHaveTextContent('5.0000');
        expect(staked).toHaveTextContent('StakingPool.stakedBalance');

        const rewards = screen.getByTestId('metric-Pending rewards');
        expect(rewards).toHaveTextContent('2.5000');
        expect(rewards).toHaveTextContent('StakingPool.earned');
    });

    it('shows honest placeholders when no wallet is connected', () => {
        mockUseAccount.mockReturnValue({ address: undefined, isConnected: false });
        mockUseReadContract.mockReturnValue({ data: undefined, refetch: vi.fn() });

        renderPage();

        expect(screen.getByTestId('metric-Your staked')).toHaveTextContent('—');
        expect(screen.getByTestId('metric-Your staked')).toHaveTextContent('connect wallet to view');
        expect(screen.getByTestId('metric-Pending rewards')).toHaveTextContent('—');
    });
});
