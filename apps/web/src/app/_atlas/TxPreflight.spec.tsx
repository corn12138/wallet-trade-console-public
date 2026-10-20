import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { NextIntlClientProvider } from 'next-intl';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { AtlasTxReviewInput } from '@/lib/api/atlas';
import { TxPreflight } from './TxPreflight';

let mockSessionGeneration = 1;
vi.mock('@/lib/web3/auth-provider', () => ({
  useAuth: () => ({ sessionGeneration: mockSessionGeneration, isRestoring: false }),
}));

const mockReview = vi.fn();
const mockExplain = vi.fn();

vi.mock('@/lib/api/atlas', () => ({
  reviewSecurityTransaction: (input: unknown) => mockReview(input),
  explainSecurityTransaction: (input: unknown) => mockExplain(input),
}));

let mockAiStatus = 'disabled';
vi.mock('./ProductStatus', () => ({
  useProductStatus: () => ({ data: { aiExplain: { status: mockAiStatus } } }),
}));

import messages from '../../../../../services/api-go/internal/i18n/baseline/en.json';

const APPROVE_INPUT: AtlasTxReviewInput = {
  operationType: 'approve',
  fromAddress: '0x1111111111111111111111111111111111111111',
  chainId: 11155111,
  tokenAddress: '0x2222222222222222222222222222222222222222',
  spender: '0x3333333333333333333333333333333333333333',
  amount: '1000',
  tokenDecimals: 18,
};

function reviewResult(overrides: Record<string, unknown> = {}) {
  return {
    reviewStatus: 'warning',
    operationType: 'approve',
    chainId: 11155111,
    riskScore: 24,
    simulation: { mode: 'rpc' },
    generatedTx: { chainId: 11155111, to: '0x2222222222222222222222222222222222222222', value: '0', data: '0x095ea7b3' },
    allowanceChange: null,
    connectedSite: null,
    recommendedActions: [],
    checks: [
      { id: 'unknown-spender', severity: 'high', status: 'warn', title: 'Unknown spender', summary: 'Not in the deployments registry.' },
      { id: 'token-check', severity: 'info', status: 'pass', title: 'No token risk flags', summary: 'Nothing raised.' },
    ],
    ...overrides,
  };
}

function renderPreflight(input: AtlasTxReviewInput | null) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <NextIntlClientProvider locale="en" messages={messages}>
        <TxPreflight input={input} />
      </NextIntlClientProvider>
    </QueryClientProvider>,
  );
}

describe('TxPreflight', () => {
  beforeEach(() => {
    mockSessionGeneration = 1;
    mockReview.mockReset();
    mockExplain.mockReset();
    mockAiStatus = 'disabled';
    mockReview.mockResolvedValue(reviewResult());
  });

  it('reviews nothing until the form describes a transaction', () => {
    renderPreflight(null);
    // A verdict on an incomplete form would be a claim about a transaction
    // that does not exist yet.
    expect(mockReview).not.toHaveBeenCalled();
    expect(screen.queryByTestId('tx-preflight')).toBeNull();
  });

  it('shows the status and the checks that deserve a second look', async () => {
    renderPreflight(APPROVE_INPUT);
    expect(await screen.findByTestId('tx-preflight')).toBeTruthy();
    expect(screen.getByTestId('tx-preflight-status').textContent).toBe('warning');
    expect(screen.getByText('Unknown spender')).toBeTruthy();
    // Passing checks are omitted — the strip is for what needs attention.
    expect(screen.queryByText('No token risk flags')).toBeNull();
  });

  it('sends exactly the input it was handed', async () => {
    renderPreflight(APPROVE_INPUT);
    await waitFor(() => expect(mockReview).toHaveBeenCalled());
    expect(mockReview.mock.calls[0][0]).toEqual(APPROVE_INPUT);
  });

  it('shows decoded contract facts and their limits even when AI is disabled', async () => {
    mockReview.mockResolvedValue(reviewResult({
      checks: [
        { id: 'contract-position-call', severity: 'info', status: 'pass', title: 'Position calldata decoded', summary: 'openPosition; sizeDelta=90071992547409931234567890 (USD scaled by 1e30).' },
        { id: 'contract-price-bound', severity: 'info', status: 'pass', title: 'Execution price bound encoded', summary: 'Maximum execution price, not a current quote.' },
        { id: 'contract-deadline', severity: 'info', status: 'pass', title: 'Deadline encoded', summary: 'The contract checks block time at execution.' },
        { id: 'contract-semantics-scope', severity: 'info', status: 'pass', title: 'Decoded intent scope', summary: 'Not proof of execution success or a security audit.' },
      ],
    }));
    renderPreflight(APPROVE_INPUT);
    expect(await screen.findByText('Position calldata decoded')).toBeTruthy();
    expect(screen.getByText(/90071992547409931234567890/)).toBeTruthy();
    expect(screen.getByText('Execution price bound encoded')).toBeTruthy();
    expect(screen.getByText('Deadline encoded')).toBeTruthy();
    expect(screen.getByText(/Not proof of execution success/)).toBeTruthy();
    expect(mockExplain).not.toHaveBeenCalled();
  });

  /**
   * The strip is advisory. A failed review must not become an alarm about the
   * user's transaction, and it must never be the reason a flow looks broken —
   * the real gates (allowance, quote executability, chain state) are unchanged.
   */
  it('stays silent when the review cannot be fetched', async () => {
    mockReview.mockRejectedValue(new Error('network down'));
    renderPreflight(APPROVE_INPUT);
    await waitFor(() => expect(mockReview).toHaveBeenCalled());
    await waitFor(() => expect(screen.queryByTestId('tx-preflight')).toBeNull());
    expect(screen.queryByText(/error|failed/i)).toBeNull();
  });

  it('offers no explain affordance when the backend says the layer is off', async () => {
    renderPreflight(APPROVE_INPUT);
    await screen.findByTestId('tx-preflight');
    expect(screen.queryByTestId('tx-preflight-explain')).toBeNull();
    expect(mockExplain).not.toHaveBeenCalled();
  });

  it('adds the summary without removing the checks', async () => {
    mockAiStatus = 'healthy';
    mockExplain.mockResolvedValue({
      review: reviewResult(),
      explanation: {
        headline: 'This lets the router move 1000 tokens.',
        whatHappens: ['The router gains permission to transfer that amount.'],
        watchOut: ['The spender is not a contract we deployed.'],
        locale: 'en',
      },
      source: 'ai',
      model: 'deepseek-v4-flash',
    });
    renderPreflight(APPROVE_INPUT);
    fireEvent.click(await screen.findByTestId('tx-preflight-explain'));

    expect(await screen.findByText('This lets the router move 1000 tokens.')).toBeTruthy();
    // The deterministic check is still there; prose supplements, never replaces.
    expect(screen.getByText('Unknown spender')).toBeTruthy();
    expect(screen.getByText(/The checks are the verdict/)).toBeTruthy();
    // Once answered, the button is gone rather than inviting a repeat call.
    expect(screen.queryByTestId('tx-preflight-explain')).toBeNull();
  });

  it('leaves the strip unchanged when the server answers static', async () => {
    mockAiStatus = 'healthy';
    mockExplain.mockResolvedValue({
      review: reviewResult(),
      explanation: null,
      source: 'static',
      reason: 'AI_UNAVAILABLE',
    });
    renderPreflight(APPROVE_INPUT);
    fireEvent.click(await screen.findByTestId('tx-preflight-explain'));

    await waitFor(() => expect(mockExplain).toHaveBeenCalled());
    // Still a complete review, and no failure wording anywhere.
    expect(screen.getByText('Unknown spender')).toBeTruthy();
    expect(screen.getByTestId('tx-preflight-status').textContent).toBe('warning');
    expect(screen.queryAllByText(/failed|unavailable|error/i).filter((node) => !node.closest('[data-testid="simulation-evidence"]'))).toHaveLength(0);
  });

  it('drops a stale explanation when the transaction changes', async () => {
    mockAiStatus = 'healthy';
    mockExplain.mockResolvedValue({
      review: reviewResult(),
      explanation: { headline: 'Old prose.', whatHappens: ['x'], watchOut: [], locale: 'en' },
      source: 'ai',
    });
    const { rerender } = renderPreflight(APPROVE_INPUT);
    fireEvent.click(await screen.findByTestId('tx-preflight-explain'));
    await screen.findByText('Old prose.');

    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    rerender(
      <QueryClientProvider client={client}>
        <NextIntlClientProvider locale="en" messages={messages}>
          <TxPreflight input={{ ...APPROVE_INPUT, amount: '2000' }} />
        </NextIntlClientProvider>
      </QueryClientProvider>,
    );

    // Prose about the 1000-token approval must not survive onto the 2000 one.
    await waitFor(() => expect(screen.queryByText('Old prose.')).toBeNull());
  });
});
