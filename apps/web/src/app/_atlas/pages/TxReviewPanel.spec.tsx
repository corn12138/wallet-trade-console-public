import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { renderWithIntl } from '@/test/renderWithIntl';
import { TxReviewPanel } from './TxReviewPanel';

/**
 * The tx-review panel is the only UI that can reach the deterministic
 * bridge-deposit checks (txreview/bridge.go). What these specs pin:
 *
 *  - the bridge mode sends `operationType: 'bridge-deposit'` with the fields
 *    the server's prepare step requires — a payload shaped for a different op
 *    would 400 and the checks would never run
 *  - the amount is passed through as BASE UNITS, unscaled: the route minimum
 *    and destination liquidity are read in base units on-chain, so any
 *    client-side scaling is a rounding seam between UI and chain
 *  - a result never outlives the mode that produced it
 *  - the server's check text is rendered verbatim — the panel does not
 *    re-word, re-rank, or drop what the deterministic engine decided
 */

const mockReview = vi.fn();
const mockExplain = vi.fn();

vi.mock('@/lib/api/atlas', () => ({
  reviewSecurityTransaction: (input: unknown) => mockReview(input),
  explainSecurityTransaction: (input: unknown) => mockExplain(input),
}));

const mockToast = vi.fn();
vi.mock('../AppContext', () => ({
  useApp: () => ({ toast: mockToast }),
}));

let mockAiStatus = 'disabled';
vi.mock('../ProductStatus', () => ({
  useProductStatus: () => ({ data: { aiExplain: { status: mockAiStatus } } }),
}));

const FROM = '0xE2cd26322A87d2b6D8312DbCb79C38b7A226Ad81';
const TOKEN = '0x57e554d795a18f3ca0a0e9e03a17ac3c509c3bf8';

function reviewResult(overrides: Record<string, unknown> = {}) {
  return {
    reviewStatus: 'warning',
    riskScore: 24,
    operationType: 'bridge-deposit',
    checks: [
      {
        id: 'bridge-trust-model',
        severity: 'medium',
        status: 'warn',
        title: 'Trusted-relayer bridge',
        summary: 'A compromised relayer can drain destination liquidity.',
      },
    ],
    simulation: { gasEstimate: '21000', callSucceeded: true, errorMessage: null },
    recommendedActions: [],
    ...overrides,
  };
}

function renderPanel() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderWithIntl(
    <QueryClientProvider client={client}>
      <TxReviewPanel fromAddress={FROM} chainId={11155111} />
    </QueryClientProvider>,
  );
}

async function switchToBridge() {
  fireEvent.click(screen.getByRole('button', { name: 'Bridge deposit' }));
  await screen.findByText('Source token');
}

function fillBridgeForm({ recipient = '' }: { recipient?: string } = {}) {
  const inputs = document.querySelectorAll('input');
  fireEvent.change(inputs[0], { target: { value: TOKEN } });
  fireEvent.change(inputs[1], { target: { value: '1000000000000000000' } });
  fireEvent.change(inputs[2], { target: { value: '84532' } });
  if (recipient) fireEvent.change(inputs[3], { target: { value: recipient } });
}

describe('TxReviewPanel bridge-deposit mode', () => {
  beforeEach(() => {
    mockReview.mockReset();
    mockExplain.mockReset();
    mockToast.mockReset();
    mockAiStatus = 'disabled';
    mockReview.mockResolvedValue(reviewResult());
  });

  it('defaults to raw-calldata mode and reviews a custom tx', async () => {
    renderPanel();
    expect(screen.getByText('To')).toBeTruthy();

    const inputs = document.querySelectorAll('input');
    fireEvent.change(inputs[0], { target: { value: '0x000000000000000000000000000000000000beef' } });
    fireEvent.click(screen.getByRole('button', { name: 'Simulate' }));

    await waitFor(() => expect(mockReview).toHaveBeenCalled());
    expect(mockReview.mock.calls[0][0].operationType).toBe('custom');
  });

  it('sends the bridge-deposit payload the server prepare step requires', async () => {
    renderPanel();
    await switchToBridge();
    fillBridgeForm();
    fireEvent.click(screen.getByRole('button', { name: 'Simulate' }));

    await waitFor(() => expect(mockReview).toHaveBeenCalled());
    const payload = mockReview.mock.calls[0][0];
    expect(payload.operationType).toBe('bridge-deposit');
    expect(payload.tokenAddress).toBe(TOKEN);
    expect(payload.bridgeDstChainId).toBe(84532);
    expect(payload.chainId).toBe(11155111);
    expect(payload.fromAddress).toBe(FROM);
    // Base units, verbatim — NOT scaled by decimals.
    expect(payload.amount).toBe('1000000000000000000');
    // Omitted recipient means "my own wallet"; the server defaults it, so the
    // client must not invent an address here.
    expect(payload.recipient).toBeUndefined();
    // Raw-calldata fields must not leak into a bridge review.
    expect(payload.tx).toBeUndefined();
  });

  it('passes an explicit recipient through', async () => {
    renderPanel();
    await switchToBridge();
    fillBridgeForm({ recipient: '0x000000000000000000000000000000000000beef' });
    fireEvent.click(screen.getByRole('button', { name: 'Simulate' }));

    await waitFor(() => expect(mockReview).toHaveBeenCalled());
    expect(mockReview.mock.calls[0][0].recipient).toBe('0x000000000000000000000000000000000000beef');
  });

  it('keeps submit disabled until every required bridge field is present', async () => {
    renderPanel();
    await switchToBridge();
    const submit = screen.getByRole('button', { name: 'Simulate' }) as HTMLButtonElement;
    expect(submit.disabled).toBe(true);

    const inputs = document.querySelectorAll('input');
    fireEvent.change(inputs[0], { target: { value: TOKEN } });
    fireEvent.change(inputs[1], { target: { value: '1000' } });
    expect((screen.getByRole('button', { name: 'Simulate' }) as HTMLButtonElement).disabled).toBe(true);

    fireEvent.change(inputs[2], { target: { value: '84532' } });
    expect((screen.getByRole('button', { name: 'Simulate' }) as HTMLButtonElement).disabled).toBe(false);
  });

  it('renders the server checks verbatim', async () => {
    renderPanel();
    await switchToBridge();
    fillBridgeForm();
    fireEvent.click(screen.getByRole('button', { name: 'Simulate' }));

    expect(await screen.findByText('Trusted-relayer bridge')).toBeTruthy();
    expect(screen.getByText('A compromised relayer can drain destination liquidity.')).toBeTruthy();
    expect(screen.getByText('warning')).toBeTruthy();
  });

  it('drops a result when the mode changes', async () => {
    renderPanel();
    await switchToBridge();
    fillBridgeForm();
    fireEvent.click(screen.getByRole('button', { name: 'Simulate' }));
    await screen.findByText('Trusted-relayer bridge');

    fireEvent.click(screen.getByRole('button', { name: 'Raw calldata' }));
    // A bridge verdict rendered next to a raw-calldata form would be a claim
    // about a transaction the form no longer describes.
    expect(screen.queryByText('Trusted-relayer bridge')).toBeNull();
  });
});

/**
 * The AI explanation layer. It ships dark, and every one of these specs is
 * really the same assertion from a different side: the panel is fully usable
 * whether or not a model ever answers, and the checks stay the verdict.
 */
describe('TxReviewPanel explanation layer', () => {
  const explanation = {
    headline: 'This deposits tokens into the bridge gateway.',
    whatHappens: ['The gateway takes custody of your tokens.'],
    watchOut: ['The gateway is currently paused.'],
    locale: 'en',
  };

  beforeEach(() => {
    mockReview.mockReset();
    mockExplain.mockReset();
    mockToast.mockReset();
    mockReview.mockResolvedValue(reviewResult());
    mockExplain.mockResolvedValue({
      review: reviewResult(),
      explanation,
      source: 'ai',
      model: 'deepseek-v4-flash',
    });
    mockAiStatus = 'healthy';
  });

  it('offers no explain affordance when the backend says the layer is off', async () => {
    mockAiStatus = 'disabled';
    renderPanel();
    // Not a disabled button, not an error message — simply absent, because a
    // control that can only ever answer "unavailable" is a dead affordance.
    expect(screen.queryByRole('button', { name: 'Explain in plain language' })).toBeNull();

    const inputs = document.querySelectorAll('input');
    fireEvent.change(inputs[0], { target: { value: '0x000000000000000000000000000000000000beef' } });
    fireEvent.click(screen.getByRole('button', { name: 'Simulate' }));
    await screen.findByText('Trusted-relayer bridge');
    expect(mockExplain).not.toHaveBeenCalled();
  });

  it('sends the review input plus locale, never a client-built review', async () => {
    renderPanel();
    const inputs = document.querySelectorAll('input');
    fireEvent.change(inputs[0], { target: { value: '0x000000000000000000000000000000000000beef' } });
    fireEvent.click(screen.getByRole('button', { name: 'Explain in plain language' }));

    await waitFor(() => expect(mockExplain).toHaveBeenCalled());
    const payload = mockExplain.mock.calls[0][0];
    expect(payload.operationType).toBe('custom');
    expect(payload.locale).toBe('en');
    expect(payload.fromAddress).toBe(FROM);
    // The server derives its own verdict; sending one would let the prose
    // describe a review the server never made.
    expect(payload.review).toBeUndefined();
    expect(payload.checks).toBeUndefined();
  });

  it('renders the summary above the checks, with the checks still shown', async () => {
    renderPanel();
    const inputs = document.querySelectorAll('input');
    fireEvent.change(inputs[0], { target: { value: '0x000000000000000000000000000000000000beef' } });
    fireEvent.click(screen.getByRole('button', { name: 'Explain in plain language' }));

    expect(await screen.findByText(explanation.headline)).toBeTruthy();
    expect(screen.getByText('The gateway takes custody of your tokens.')).toBeTruthy();
    // The deterministic checks remain — the summary supplements, never replaces.
    expect(screen.getByText('Trusted-relayer bridge')).toBeTruthy();
    expect(screen.getByText('warning')).toBeTruthy();
    // And it is labelled as generated, with the checks named as the verdict.
    expect(screen.getByText(/The checks are the verdict/)).toBeTruthy();
  });

  it('shows nothing extra when the server answers static', async () => {
    mockExplain.mockResolvedValue({
      review: reviewResult(),
      explanation: null,
      source: 'static',
      reason: 'AI_UNAVAILABLE',
    });
    renderPanel();
    const inputs = document.querySelectorAll('input');
    fireEvent.change(inputs[0], { target: { value: '0x000000000000000000000000000000000000beef' } });
    fireEvent.click(screen.getByRole('button', { name: 'Explain in plain language' }));

    // The review still lands — a missing explanation costs the user nothing.
    expect(await screen.findByText('Trusted-relayer bridge')).toBeTruthy();
    expect(screen.queryByText('Plain-language summary')).toBeNull();
    // No failure wording anywhere: static is a valid answer, not a breakage.
    expect(screen.queryByText(/failed|unavailable|error/i)).toBeNull();
    expect(mockToast).not.toHaveBeenCalled();
  });

  it('drops a stale explanation when the review is re-run', async () => {
    renderPanel();
    const inputs = document.querySelectorAll('input');
    fireEvent.change(inputs[0], { target: { value: '0x000000000000000000000000000000000000beef' } });
    fireEvent.click(screen.getByRole('button', { name: 'Explain in plain language' }));
    await screen.findByText(explanation.headline);

    fireEvent.change(inputs[0], { target: { value: '0x000000000000000000000000000000000000dead' } });
    fireEvent.click(screen.getByRole('button', { name: 'Simulate' }));

    // Prose about the previous transaction must not survive a new review.
    await waitFor(() => expect(screen.queryByText(explanation.headline)).toBeNull());
  });
});
