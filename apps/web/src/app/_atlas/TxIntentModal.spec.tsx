import { fireEvent, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { renderWithIntl } from '@/test/renderWithIntl';
import { TxIntentModal, type TxIntentModalProps } from './Common';
import type { TxReviewFacts } from '@/hooks/web3/txIntent/types';

vi.mock('wagmi', () => ({ useConnect: () => ({ connectors: [], connect: vi.fn() }) }));
vi.mock('@/hooks/web3/useTokenBalance', () => ({ useTokenBalance: () => ({ balance: 0n, formatted: '0' }) }));
vi.mock('@/lib/web3/contracts', () => ({ getPerpAddresses: () => ({}) }));
vi.mock('@/lib/web3', () => ({ supportedChains: [] }));
vi.mock('./AppContext', () => ({ useApp: () => ({ modal: null, closeModal: vi.fn() }) }));

const review: TxReviewFacts = {
  owner: '0x000000000000000000000000000000000000dead',
  targetChainId: 11155111,
  target: '0xe0c55ff91ece0acc7ddafa9069ba5b0cdbbc3b00',
  calldataHash: '0x38ed1739000000000000000000000000000000000000000000000000000000000000abcd',
  value: 0n,
  token: { address: '0x57e554d795a18f3ca0a0e9e03a17ac3c509c3bf8', symbol: 'USDC', decimals: 18, amount: 100n * 10n ** 18n },
  guard: { slippageBps: 50, minAmountOut: 992n * 10n ** 17n, outToken: { symbol: 'WETH', decimals: 18 }, deadline: 1_800_000_000n },
};

function base(overrides: Partial<TxIntentModalProps>): TxIntentModalProps {
  return {
    mode: 'intent', phase: 'review', state: 'READY_FOR_SIGNATURE', title: 'Transaction',
    stepTitle: 'Swap USDC → WETH', step: { current: 1, total: 2 }, review, expectsIndexing: true,
    indexing: 'not-expected', hash: null, ...overrides,
  };
}

function renderModal(props: TxIntentModalProps) {
  const close = vi.fn();
  renderWithIntl(<TxIntentModal props={props} onBg={() => {}} close={close} />);
  return { close };
}

/** No rendered text may be a raw catalogue path — that is what a missing key looks like. */
function expectNoRawKeys() {
  expect(document.body.textContent).not.toMatch(/atlasShell\.modals/);
}

describe('TxIntentModal — every phase renders translated copy and a way out', () => {
  it('review: shows the frozen facts and a Sign button that hands off to the engine', () => {
    const onSign = vi.fn();
    renderModal(base({ onSign }));
    expect(screen.getByTestId('tx-intent-lead').textContent).toMatch(/frozen/i);
    const facts = screen.getByTestId('tx-intent-facts').textContent ?? '';
    expect(facts).toContain('100 USDC');
    expect(facts).toContain('99.2 WETH');
    expect(facts).toContain('0.5%');
    expect(facts).toContain('0x38ed17…00abcd');
    expect(screen.getByTestId('tx-intent-step').textContent).toBe('Step 1 of 2');
    fireEvent.click(screen.getByTestId('tx-intent-sign'));
    expect(onSign).toHaveBeenCalledTimes(1);
    expectNoRawKeys();
  });

  it('wallet / pending / indexing: no Sign button, phase-specific lead, hash once known', () => {
    for (const [phase, state, expectHash] of [['wallet', 'AWAITING_WALLET', false], ['pending', 'SUBMITTED', true], ['indexing', 'CONFIRMING', true]] as const) {
      const { unmount } = renderWithIntl(
        <TxIntentModal props={base({ phase, state, hash: expectHash ? '0xabc' : null, onSign: vi.fn() })} onBg={() => {}} close={vi.fn()} />,
      );
      expect(screen.queryByTestId('tx-intent-sign')).toBeNull();
      expect(screen.getByTestId('tx-intent-lead').textContent?.length).toBeGreaterThan(10);
      if (expectHash) expect(screen.getByTestId('tx-modal-hash').textContent).toContain('0xabc');
      expectNoRawKeys();
      unmount();
    }
  });

  it('done: distinguishes indexed, not-yet-visible, and not-tracked', () => {
    for (const [indexing, fragment] of [['indexed', /visible/i], ['timed-out', /not yet visible/i], ['not-expected', /confirmed on chain\./i]] as const) {
      const { unmount } = renderWithIntl(
        <TxIntentModal props={base({ phase: 'done', state: 'FINALIZED', indexing, hash: '0xabc' })} onBg={() => {}} close={vi.fn()} />,
      );
      expect(screen.getByTestId('tx-intent-lead').textContent).toMatch(fragment);
      expectNoRawKeys();
      unmount();
    }
  });

  it('every failure state has its own translated reason, a retry, and a close', () => {
    const cases: Array<[TxIntentModalProps['state'], Partial<TxIntentModalProps>, RegExp]> = [
      ['REJECTED_BY_WALLET', { errorCode: 'REJECTED_BY_WALLET' }, /rejected/i],
      ['REVERTED', { errorCode: 'REVERTED', hash: '0xabc' }, /reverted/i],
      ['REPLACED', { hash: '0xabc', replacedByHash: '0xdef' }, /replaced/i],
      ['REORGED', { hash: '0xabc' }, /canonical chain/i],
      ['EXPIRED', { errorCode: 'INTENT_EXPIRED' }, /expired/i],
      ['FAILED', { errorCode: 'WRONG_CHAIN' }, /different chain/i],
      ['FAILED', { errorCode: 'UNKNOWN', technicalHint: 'execution reverted: something' }, /failed/i],
      ['DRAFT', { invalidationReason: 'QUOTE_OUT_OF_BAND' }, /quote moved/i],
    ];
    for (const [state, extra, fragment] of cases) {
      const onRetry = vi.fn();
      const { close } = renderModal(base({ phase: 'failed', state, onRetry, ...extra }));
      expect(screen.getByTestId('tx-intent-lead').textContent).toMatch(fragment);
      expect(screen.queryByTestId('tx-intent-sign')).toBeNull();
      fireEvent.click(screen.getByTestId('tx-intent-retry'));
      expect(onRetry).toHaveBeenCalledTimes(1);
      fireEvent.click(screen.getByText('Close'));
      expect(close).toHaveBeenCalled();
      expectNoRawKeys();
      document.body.innerHTML = '';
    }
  });

  it('a technical hint is collapsed by default and never shown for a success', () => {
    renderModal(base({ phase: 'failed', state: 'FAILED', errorCode: 'UNKNOWN', technicalHint: 'HTTP request failed [url]' }));
    expect(screen.queryByTestId('tx-intent-technical')).toBeNull();
    fireEvent.click(screen.getByText('Technical details'));
    expect(screen.getByTestId('tx-intent-technical').textContent).toContain('[url]');
  });

  it('an approve (no indexing expected) shows a four-step strip without an Indexing step', () => {
    renderModal(base({ expectsIndexing: false, stepTitle: 'Approve USDC' }));
    expect(screen.queryByText('INDEXING')).toBeNull();
    expect(screen.getByText('REVIEW')).toBeInTheDocument();
    expect(screen.getByText('DONE')).toBeInTheDocument();
  });
});
