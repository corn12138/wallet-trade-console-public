import { fireEvent, screen, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { erc20Abi } from 'viem';
import { renderWithIntl, renderWithIntlZh } from '@/test/renderWithIntl';
import { freezeStep } from '@/hooks/web3/txIntent/intent';
import type { UseTxIntentResult } from '@/hooks/web3/txIntent/useTxIntent';
import { TradeIntentDialog } from './TradeIntentDialog';

const switchMock = vi.fn();
const toast = vi.fn();
const preflight = vi.fn();
vi.mock('wagmi', () => ({ useConnect: () => ({ connectors: [], connect: vi.fn() }),
  useSwitchChain: () => ({ switchChainAsync: switchMock }) }));
vi.mock('@/hooks/web3/useTokenBalance', () => ({ useTokenBalance: () => ({ balance: 0n, formatted: '0' }) }));
vi.mock('@/lib/web3/contracts', () => ({ getPerpAddresses: () => ({}) }));
vi.mock('@/lib/web3', () => ({ supportedChains: [] }));
vi.mock('../AppContext', () => ({ useApp: () => ({ toast }) }));
vi.mock('../TxPreflight', () => ({ TxPreflight: ({ input }: { input: unknown }) => { preflight(input); return <div data-testid="preflight" />; } }));

const step = freezeStep({ kind: 'action', actionType: 'open-position',
  owner: '0x0000000000000000000000000000000000000001', targetChainId: 11155111,
  target: '0x0000000000000000000000000000000000000002', abi: erc20Abi, functionName: 'approve',
  args: ['0x0000000000000000000000000000000000000003', 100n],
  guard: { acceptablePrice: 2010n * 10n ** 30n, deadline: 1800000000n },
  metadata: { market: 'ETH-USD', isLong: true, size: (1000n * 10n ** 30n).toString() },
  expectsIndexing: true, now: 1700000000000 });
function transaction(overrides: Partial<UseTxIntentResult> = {}) {
  const intent: UseTxIntentResult = {
    state: 'READY_FOR_SIGNATURE', phase: 'review', steps: [step], activeStepIndex: 0,
    activeStep: step, hash: null, replacedByHash: null, confirmations: 0, errorCode: null,
    invalidationReason: null, complete: false, review: vi.fn(), send: vi.fn(), reset: vi.fn(),
    busy: true, wrongChain: false, receipt: undefined, rawError: null, indexing: 'not-expected', now: Date.now,
    ...overrides,
  };
  return { intent, locked: intent.busy, reviewOpen: vi.fn(), reviewClose: vi.fn(), retry: vi.fn() };
}
beforeEach(() => { switchMock.mockReset().mockResolvedValue({ id: 11155111 }); toast.mockClear(); preflight.mockClear(); });

describe('trade review dialog', () => {
  it('shows frozen market/side/size and sends the exact frozen request to preflight', () => {
    const tx = transaction();
    renderWithIntl(<TradeIntentDialog transaction={tx} close={vi.fn()} />);
    expect(screen.getByTestId('tx-intent-summary')).toHaveTextContent('ETH-USD');
    expect(screen.getByTestId('tx-intent-summary')).toHaveTextContent('1000 USD');
    expect(screen.getByTestId('tx-intent-facts')).toHaveTextContent('2010');
    expect(preflight).toHaveBeenCalledWith({ operationType: 'custom', fromAddress: step.owner,
      chainId: step.targetChainId, tx: { to: step.target, data: step.calldata, value: '0' } });
    fireEvent.click(screen.getByTestId('tx-intent-sign'));
    expect(tx.intent.send).toHaveBeenCalledTimes(1);
  });
  it('a wrong-chain click switches only and requires a new review', async () => {
    const tx = transaction({ wrongChain: true });
    const close = vi.fn();
    renderWithIntl(<TradeIntentDialog transaction={tx} close={close} />);
    expect(screen.getByTestId('tx-intent-sign')).toHaveTextContent('Switch to chain 11155111');
    fireEvent.click(screen.getByTestId('tx-intent-sign'));
    await waitFor(() => expect(switchMock).toHaveBeenCalledWith({ chainId: 11155111 }));
    expect(tx.intent.send).not.toHaveBeenCalled();
    await waitFor(() => expect(tx.intent.reset).toHaveBeenCalledOnce());
    expect(close).toHaveBeenCalledOnce();
    expect(toast).toHaveBeenCalledWith('Network changed. Review the transaction again before signing.', 'ok');
  });
  it('invalidated reviews offer re-review without an active sign button', () => {
    const tx = transaction({ state: 'DRAFT', phase: 'review', busy: false, invalidationReason: 'ACCOUNT_CHANGED' });
    renderWithIntl(<TradeIntentDialog transaction={tx} close={vi.fn()} />);
    expect(screen.queryByTestId('tx-intent-sign')).toBeNull();
    fireEvent.click(screen.getByTestId('tx-intent-retry'));
    expect(tx.retry).toHaveBeenCalledOnce();
    expect(tx.intent.send).not.toHaveBeenCalled();
  });
  it('an uncertain broadcast never offers a retry that may duplicate a transaction', () => {
    const tx = transaction({ state: 'FAILED', phase: 'failed', busy: true, hash: '0xabc', errorCode: 'UNKNOWN' });
    renderWithIntl(<TradeIntentDialog transaction={tx} close={vi.fn()} />);
    expect(screen.queryByTestId('tx-intent-retry')).toBeNull();
    expect(screen.queryByTestId('tx-intent-sign')).toBeNull();
  });
  it('approval tells the user to review again and does not send the next step', () => {
    const approve = { ...step, kind: 'approve' as const, actionType: 'approve', expectsIndexing: false };
    const tx = transaction({ state: 'FINALIZED', phase: 'done', busy: false, activeStep: approve, steps: [approve, step] });
    renderWithIntl(<TradeIntentDialog transaction={tx} close={vi.fn()} />);
    expect(screen.getByTestId('tx-intent-summary')).toHaveTextContent('After approval, review the position again');
    expect(tx.intent.send).not.toHaveBeenCalled();
  });
  it('Chinese review uses the server catalog without untranslated paths', () => {
    renderWithIntlZh(<TradeIntentDialog transaction={transaction()} close={vi.fn()} />);
    expect(screen.getByRole('heading')).toHaveTextContent('确认开仓交易');
    expect(document.body.textContent).not.toMatch(/trade\.intent|atlasShell\.modals/);
  });
});
