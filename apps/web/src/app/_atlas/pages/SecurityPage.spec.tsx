import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { renderWithIntl } from '@/test/renderWithIntl';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { reviewSecurityTransaction } from '@/lib/api/atlas';
import { SecurityPage } from './SecurityPage';

/**
 * Regression guard for /security revoke. Two properties, and the second is the
 * reason the first is not simply "one click signs":
 *
 *  1. Confirming a revoke must submit a REAL wallet transaction — ERC20
 *     approve(spender, 0) — not merely build an unsigned tx and open a passive
 *     modal. A wrong-network wallet must prompt a switch instead of signing on
 *     the wrong chain.
 *  2. Selecting a row must NOT sign. It arms the row and shows the pre-sign
 *     review; a review the user only sees after the wallet prompt is useless,
 *     so the arming step is load-bearing, not decoration.
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

const reviewResult = {
  reviewStatus: 'warning',
  operationType: 'revoke-approval',
  chainId: 11155111,
  riskScore: 20,
  simulation: { mode: 'estimate-gas', gasEstimate: '21000' },
  checks: [{ id: 'unknown-spender', severity: 'medium', status: 'warn', title: 'Unknown spender', summary: '…' }],
};

vi.mock('@/lib/api/atlas', () => ({
  getSecurityApprovals: vi.fn(() => Promise.resolve([approval])),
  getSecurityAlerts: vi.fn(() => Promise.resolve([])),
  getConnectedSites: vi.fn(() => Promise.resolve([])),
  removeConnectedSite: vi.fn(() => Promise.resolve()),
  reviewSecurityTransaction: vi.fn(() => Promise.resolve(reviewResult)),
  explainSecurityTransaction: vi.fn(),
  // The explanation layer ships dark; the preflight strip must render its
  // deterministic checks without it.
  getProductStatus: vi.fn(() => Promise.resolve({ aiExplain: { status: 'disabled' }, warnings: [] })),
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

  it('arms the row and reviews the revoke before anything is signed', async () => {
    const user = userEvent.setup();
    renderSecurity();

    await user.click(await screen.findByTestId(`revoke-${TOKEN}-${SPENDER}`.toLowerCase()));

    expect(await screen.findByTestId('revoke-preflight')).toBeTruthy();
    // Pins the baseline copy, not just the testid: a missing catalog key would
    // ship a raw key path as the confirm label.
    expect(screen.getByTestId('revoke-confirm').textContent).toBe('Confirm revoke');
    // The point of the arming step: no wallet prompt has happened yet.
    expect(writeContract).not.toHaveBeenCalled();
    expect(setModal).not.toHaveBeenCalled();

    // Backing out leaves the approval untouched.
    await user.click(screen.getByTestId('revoke-cancel'));
    expect(screen.queryByTestId('revoke-preflight')).toBeNull();
    expect(writeContract).not.toHaveBeenCalled();
  });

  it('reviews the same approve(spender, 0) the wallet is asked to sign', async () => {
    const user = userEvent.setup();
    renderSecurity();

    await user.click(await screen.findByTestId(`revoke-${TOKEN}-${SPENDER}`.toLowerCase()));
    await screen.findByTestId('revoke-preflight');

    await waitFor(() => expect(reviewSecurityTransaction).toHaveBeenCalled());
    expect(reviewSecurityTransaction).toHaveBeenCalledWith({
      operationType: 'revoke-approval',
      fromAddress: USER,
      chainId: 11155111,
      tokenAddress: TOKEN,
      spender: SPENDER,
    });
  });

  it('submits a real approve(spender, 0) wallet transaction on confirm', async () => {
    const user = userEvent.setup();
    renderSecurity();

    await user.click(await screen.findByTestId(`revoke-${TOKEN}-${SPENDER}`.toLowerCase()));
    await user.click(await screen.findByTestId('revoke-confirm'));

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

    await user.click(await screen.findByTestId(`revoke-${TOKEN}-${SPENDER}`.toLowerCase()));
    await user.click(await screen.findByTestId('revoke-confirm'));

    expect(writeContract).not.toHaveBeenCalled();
    expect(openChain).toHaveBeenCalledTimes(1);
    expect(toast).toHaveBeenCalled();
  });
});
