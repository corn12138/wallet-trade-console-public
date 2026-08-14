import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { renderWithIntl } from '@/test/renderWithIntl';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { SecurityPage } from './SecurityPage';

/**
 * Regression guard for /security revoke: clicking Revoke must submit a real
 * wallet transaction — ERC20 approve(spender, 0) — not merely build an
 * unsigned tx and open a passive modal. A wrong-network wallet must prompt a
 * switch instead of signing on the wrong chain.
 */

const USER = '0x1111111111111111111111111111111111111111' as const;
const TOKEN = '0x00000000000000000000000000000000000000aa' as const;
const SPENDER = '0x00000000000000000000000000000000000000bb' as const;

const writeContract = vi.fn();
const mockUseChainId = vi.fn();
const openChain = vi.fn();
const setModal = vi.fn();
const toast = vi.fn();

vi.mock('wagmi', () => ({
  useAccount: () => ({ address: USER }),
  useChainId: () => mockUseChainId(),
  useWriteContract: () => ({ writeContract, data: undefined, isPending: false, error: null, reset: vi.fn() }),
  useWaitForTransactionReceipt: () => ({ isLoading: false, isSuccess: false, data: undefined, error: null }),
  useConnect: () => ({ connectors: [] }),
  createConfig: vi.fn(() => ({})),
  createStorage: vi.fn(() => ({})),
  cookieStorage: {},
  http: vi.fn(),
}));

vi.mock('@/hooks/web3/usePersistTransactionLifecycle', () => ({
  usePersistTransactionLifecycle: vi.fn(),
}));

const approval = {
  tokenAddress: TOKEN,
  spender: SPENDER,
  allowance: 'unlimited',
  chainId: 11155111,
  lastUpdatedAt: new Date().toISOString(),
  txHash: '0xabc',
};

vi.mock('@/lib/api/atlas', () => ({
  getSecurityApprovals: vi.fn(() => Promise.resolve([approval])),
  getSecurityAlerts: vi.fn(() => Promise.resolve([])),
  getConnectedSites: vi.fn(() => Promise.resolve([])),
  removeConnectedSite: vi.fn(() => Promise.resolve()),
}));

vi.mock('../AppContext', () => ({
  useApp: () => ({
    chainId: 11155111,
    wallet: { address: USER },
    openConnect: vi.fn(),
    openChain,
    setModal,
    toast,
  }),
}));

function renderSecurity() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderWithIntl(
    <QueryClientProvider client={client}>
      <SecurityPage />
    </QueryClientProvider>,
  );
}

describe('SecurityPage revoke', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockUseChainId.mockReturnValue(11155111);
  });

  it('submits a real approve(spender, 0) wallet transaction on revoke', async () => {
    const user = userEvent.setup();
    renderSecurity();

    const button = await screen.findByTestId(`revoke-${TOKEN}-${SPENDER}`.toLowerCase());
    await user.click(button);

    await waitFor(() => expect(writeContract).toHaveBeenCalledTimes(1));
    const [call] = writeContract.mock.calls[0];
    expect(call).toMatchObject({
      chainId: 11155111,
      address: TOKEN,
      functionName: 'approve',
    });
    expect(call.args[0]).toBe(SPENDER);
    expect(call.args[1]).toBe(BigInt(0));
    // The submit modal opens; it is NOT the old passive build-only summary.
    expect(setModal).toHaveBeenCalled();
  });

  it('prompts a network switch instead of signing on the wrong chain', async () => {
    const user = userEvent.setup();
    mockUseChainId.mockReturnValue(1); // wallet on mainnet, approval on Sepolia
    renderSecurity();

    const button = await screen.findByTestId(`revoke-${TOKEN}-${SPENDER}`.toLowerCase());
    await user.click(button);

    expect(writeContract).not.toHaveBeenCalled();
    expect(openChain).toHaveBeenCalledTimes(1);
    expect(toast).toHaveBeenCalled();
  });
});
