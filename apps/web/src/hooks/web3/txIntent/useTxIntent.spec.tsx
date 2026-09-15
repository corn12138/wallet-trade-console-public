import { act, renderHook, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { erc20Abi } from 'viem';
import type { ReactNode } from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { freezeStep } from './intent';
import { useTxIntent } from './useTxIntent';
import type { TxStep } from './types';

const OWNER = '0x000000000000000000000000000000000000dead' as const;
const OTHER = '0x000000000000000000000000000000000000beef' as const;
const TOKEN = '0x0000000000000000000000000000000000000001' as const;
const SPENDER = '0x0000000000000000000000000000000000000002' as const;
const HASH = '0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' as const;
const HASH2 = '0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb' as const;
const CHAIN = 11155111;

const wallet = {
  address: OWNER as `0x${string}` | undefined,
  connectorId: 'injected' as string | undefined,
  chainId: CHAIN,
  receipt: undefined as { status: 'success' | 'reverted'; transactionHash: string } | undefined,
  receiptError: null as Error | null,
  onReplaced: null as ((r: { transaction: { hash: `0x${string}` } }) => void) | null,
};
const sendMock = vi.fn();
const eventsMock = vi.fn();

vi.mock('wagmi', () => ({
  useAccount: () => ({ address: wallet.address, connector: wallet.connectorId ? { id: wallet.connectorId } : undefined }),
  useChainId: () => wallet.chainId,
  useSendTransaction: () => ({ mutateAsync: sendMock }),
  useWaitForTransactionReceipt: (params: { hash?: string; onReplaced?: typeof wallet.onReplaced }) => {
    wallet.onReplaced = params.onReplaced ?? null;
    return {
      data: params.hash ? wallet.receipt : undefined,
      isError: Boolean(params.hash && wallet.receiptError),
      error: params.hash ? wallet.receiptError : null,
    };
  },
}));
vi.mock('@/lib/api/events', () => ({ getEventsByTxHash: (...args: unknown[]) => eventsMock(...args) }));
vi.mock('../usePersistTransactionLifecycle', () => ({ usePersistTransactionLifecycle: () => undefined }));

function approveStep(amount: bigint, extra: Partial<Parameters<typeof freezeStep>[0]> = {}): TxStep {
  return freezeStep({
    kind: 'approve', actionType: 'approve', owner: OWNER, targetChainId: CHAIN, target: TOKEN,
    abi: erc20Abi, functionName: 'approve', args: [SPENDER, amount],
    token: { address: TOKEN, symbol: 'T', decimals: 18, amount }, expectsIndexing: false, now: Date.now(), ...extra,
  });
}

function wrapper({ children }: { children: ReactNode }) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

function render(build: () => TxStep[] | null, options: Partial<Parameters<typeof useTxIntent>[0]> = {}) {
  return renderHook(
    (props: { build: () => TxStep[] | null; liveAllowance?: bigint; liveMinAmountOut?: bigint; viewerKey?: string | null }) =>
      useTxIntent({ build: props.build, targetChainId: CHAIN, indexingPollMs: 5, indexingTimeoutMs: 60,
        liveAllowance: props.liveAllowance, liveMinAmountOut: props.liveMinAmountOut, viewerKey: props.viewerKey, ...options }),
    {
      wrapper,
      initialProps: { build } as {
        build: () => TxStep[] | null; liveAllowance?: bigint; liveMinAmountOut?: bigint; viewerKey?: string | null;
      },
    },
  );
}

beforeEach(() => {
  wallet.address = OWNER; wallet.connectorId = 'injected'; wallet.chainId = CHAIN;
  wallet.receipt = undefined; wallet.receiptError = null; wallet.onReplaced = null;
  sendMock.mockReset(); sendMock.mockResolvedValue(HASH);
  eventsMock.mockReset(); eventsMock.mockResolvedValue([]);
});

describe('review freezes; send hands the frozen bytes to the wallet', () => {
  it('review moves to READY_FOR_SIGNATURE with the frozen step and marks the CTA busy', () => {
    const { result } = render(() => [approveStep(100n)]);
    expect(result.current.state).toBe('DRAFT');
    act(() => { expect(result.current.review()).toBeNull(); });
    expect(result.current.state).toBe('READY_FOR_SIGNATURE');
    expect(result.current.phase).toBe('review');
    expect(result.current.activeStep?.review.token?.amount).toBe(100n);
    expect(result.current.busy).toBe(true);
  });

  it('send passes {to, data, value, chainId} — the frozen calldata, never abi/args to be re-encoded', async () => {
    const step = approveStep(100n);
    const { result } = render(() => [step]);
    act(() => { result.current.review(); });
    await act(async () => { await result.current.send(); });
    expect(sendMock).toHaveBeenCalledTimes(1);
    const sent = sendMock.mock.calls[0][0];
    expect(sent).toEqual({ to: TOKEN, data: step.calldata, value: 0n, chainId: CHAIN });
    expect(sent).not.toHaveProperty('abi');
    expect(sent).not.toHaveProperty('args');
    expect(result.current.state).toBe('SUBMITTED');
    expect(result.current.hash).toBe(HASH);
  });
});

describe('the pre-send check can actually fail', () => {
  it('refuses to open the wallet when the live inputs no longer produce the reviewed request', async () => {
    let amount = 100n;
    const { result } = render(() => [approveStep(amount)]);
    act(() => { result.current.review(); });
    amount = 101n; // the quote/input moved after review
    await act(async () => { await result.current.send(); });
    expect(sendMock).not.toHaveBeenCalled();
    expect(result.current.state).toBe('DRAFT');
    expect(result.current.invalidationReason).toBe('DRIFT_AT_SEND');
  });

  it('refuses when the frozen step has expired', async () => {
    const deadline = BigInt(Math.floor(Date.now() / 1000) + 5 * 60);
    const step = approveStep(100n, { guard: { deadline } });
    const expired = { ...step, expiresAt: Date.now() - 1 };
    const { result } = render(() => [expired]);
    act(() => { result.current.review(); });
    await act(async () => { await result.current.send(); });
    expect(sendMock).not.toHaveBeenCalled();
    expect(result.current.state).toBe('EXPIRED');
    expect(result.current.errorCode).toBe('INTENT_EXPIRED');
  });

  it('a wallet on the wrong chain is a gate with a code, not a discarded intent', async () => {
    const build = () => [approveStep(100n)];
    const { result, rerender } = render(build);
    act(() => { result.current.review(); });
    wallet.chainId = 1;
    rerender({ build }); // a chain switch always re-renders before the user can click again
    await act(async () => { await result.current.send(); });
    expect(sendMock).not.toHaveBeenCalled();
    expect(result.current.errorCode).toBe('WRONG_CHAIN');
    expect(result.current.wrongChain).toBe(true);
  });
});

describe('invalidation (drafts only)', () => {
  it('account change invalidates a reviewed draft', () => {
    const { result, rerender } = render(() => [approveStep(100n)]);
    act(() => { result.current.review(); });
    wallet.address = OTHER;
    rerender({ build: () => [approveStep(100n)] });
    expect(result.current.state).toBe('DRAFT');
    expect(result.current.invalidationReason).toBe('ACCOUNT_CHANGED');
  });

  it('authenticated-viewer change invalidates a reviewed draft', () => {
    const build = () => [approveStep(100n)];
    const { result, rerender } = render(build);
    rerender({ build, viewerKey: 'viewer-a' });
    act(() => { result.current.review(); });
    rerender({ build, viewerKey: 'viewer-b' });
    expect(result.current.invalidationReason).toBe('VIEWER_CHANGED');
  });

  it('allowance invalidates only when it falls below the frozen amount', () => {
    const swap = freezeStep({
      kind: 'action', actionType: 'swap', owner: OWNER, targetChainId: CHAIN, target: SPENDER,
      abi: erc20Abi, functionName: 'transfer', args: [OTHER, 100n],
      token: { address: TOKEN, symbol: 'T', decimals: 18, amount: 100n }, expectsIndexing: true, now: Date.now(),
    });
    const build = () => [swap];
    const { result, rerender } = render(build, {});
    rerender({ build, liveAllowance: 100n });
    act(() => { result.current.review(); });
    rerender({ build, liveAllowance: 5_000n });
    expect(result.current.state).toBe('READY_FOR_SIGNATURE');
    rerender({ build, liveAllowance: 99n });
    expect(result.current.invalidationReason).toBe('ALLOWANCE_BELOW_REQUIRED');
  });

  it('a quote that improves keeps the intent; one that would revert the frozen minimum invalidates it', () => {
    const swap = freezeStep({
      kind: 'action', actionType: 'swap', owner: OWNER, targetChainId: CHAIN, target: SPENDER,
      abi: erc20Abi, functionName: 'transfer', args: [OTHER, 100n],
      guard: { minAmountOut: 1_000n, slippageBps: 50 }, expectsIndexing: true, now: Date.now(),
    });
    const build = () => [swap];
    const { result, rerender } = render(build);
    rerender({ build, liveMinAmountOut: 1_000n });
    act(() => { result.current.review(); });
    rerender({ build, liveMinAmountOut: 1_200n });
    expect(result.current.state).toBe('READY_FOR_SIGNATURE');
    rerender({ build, liveMinAmountOut: 999n });
    expect(result.current.invalidationReason).toBe('QUOTE_OUT_OF_BAND');
  });
});

describe('outcomes are distinct facts', () => {
  it('a wallet rejection, and a late receipt cannot resurrect it', async () => {
    sendMock.mockRejectedValue({ name: 'UserRejectedRequestError', message: 'User rejected the request.' });
    const { result, rerender } = render(() => [approveStep(100n)]);
    act(() => { result.current.review(); });
    await act(async () => { await result.current.send(); });
    expect(result.current.state).toBe('REJECTED_BY_WALLET');
    expect(result.current.phase).toBe('failed');
    wallet.receipt = { status: 'success', transactionHash: HASH };
    rerender({ build: () => [approveStep(100n)] });
    expect(result.current.state).toBe('REJECTED_BY_WALLET');
  });

  it('an approve (no indexing expected) is done on its receipt without asking the indexer', async () => {
    const { result, rerender } = render(() => [approveStep(100n)]);
    act(() => { result.current.review(); });
    await act(async () => { await result.current.send(); });
    wallet.receipt = { status: 'success', transactionHash: HASH };
    rerender({ build: () => [approveStep(100n)] });
    expect(result.current.state).toBe('FINALIZED');
    expect(result.current.phase).toBe('done');
    expect(result.current.indexing).toBe('not-expected');
    expect(eventsMock).not.toHaveBeenCalled();
  });

  it('a swap (indexing expected) waits in the indexing phase until the product can show it', async () => {
    const swap = approveStep(100n, { kind: 'action', actionType: 'swap', expectsIndexing: true });
    eventsMock.mockResolvedValue([{ id: 1 }]);
    const { result, rerender } = render(() => [swap]);
    act(() => { result.current.review(); });
    await act(async () => { await result.current.send(); });
    wallet.receipt = { status: 'success', transactionHash: HASH };
    rerender({ build: () => [swap] });
    expect(result.current.state).toBe('CONFIRMING');
    expect(result.current.phase).toBe('indexing');
    await waitFor(() => expect(result.current.state).toBe('FINALIZED'));
    expect(result.current.indexing).toBe('indexed');
    expect(eventsMock).toHaveBeenCalledWith(HASH, CHAIN);
  });

  it('finalizes honestly as timed-out when the indexer never shows it', async () => {
    const swap = approveStep(100n, { kind: 'action', actionType: 'swap', expectsIndexing: true });
    const { result, rerender } = render(() => [swap]);
    act(() => { result.current.review(); });
    await act(async () => { await result.current.send(); });
    wallet.receipt = { status: 'success', transactionHash: HASH };
    rerender({ build: () => [swap] });
    await waitFor(() => expect(result.current.state).toBe('FINALIZED'));
    expect(result.current.indexing).toBe('timed-out');
  });

  it('a reverted receipt is REVERTED', async () => {
    const { result, rerender } = render(() => [approveStep(100n)]);
    act(() => { result.current.review(); });
    await act(async () => { await result.current.send(); });
    wallet.receipt = { status: 'reverted', transactionHash: HASH };
    rerender({ build: () => [approveStep(100n)] });
    expect(result.current.state).toBe('REVERTED');
  });

  it('a same-nonce replacement is REPLACED and carries the winning hash', async () => {
    const { result } = render(() => [approveStep(100n)]);
    act(() => { result.current.review(); });
    await act(async () => { await result.current.send(); });
    expect(wallet.onReplaced).toBeTypeOf('function');
    act(() => { wallet.onReplaced?.({ transaction: { hash: HASH2 } }); });
    expect(result.current.state).toBe('REPLACED');
    expect(result.current.replacedByHash).toBe(HASH2);
    expect(result.current.hash).toBe(HASH);
  });

  it('reset returns to DRAFT and clears the frozen step', async () => {
    const { result } = render(() => [approveStep(100n)]);
    act(() => { result.current.review(); });
    act(() => { result.current.reset(); });
    expect(result.current.state).toBe('DRAFT');
    expect(result.current.activeStep).toBeNull();
  });
});
