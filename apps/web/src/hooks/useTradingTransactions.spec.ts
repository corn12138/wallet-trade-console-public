import { createElement, type ReactNode } from 'react';
import { act, renderHook, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { decodeFunctionData, erc20Abi, parseUnits } from 'viem';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { CONTRACT_ABIS } from '@/lib/web3/contracts';
import { useTradingTransactions, type UseTradingTransactionsParams } from './useTradingTransactions';
import type { AggregatedPosition } from './web3/usePerpPositions';
import type { TradingMarketView } from './trading.types';

const OWNER = '0x1111111111111111111111111111111111111111' as const;
const MANAGER = '0x2222222222222222222222222222222222222222' as const;
const TOKEN = '0x3333333333333333333333333333333333333333' as const;
const INDEX = '0x4444444444444444444444444444444444444444' as const;
const OTHER_INDEX = '0x5555555555555555555555555555555555555555' as const;
const HASH = `0x${'a'.repeat(64)}` as `0x${string}`;
const HASH2 = `0x${'b'.repeat(64)}` as `0x${string}`;
const CHAIN = 11155111;
const PRICE = parseUnits('2000', 30);
const AMOUNT = parseUnits('100', 18);
const NOW = 1_790_000_000_000;
const wallet = {
  address: OWNER as `0x${string}` | undefined,
  connectorId: 'injected', chainId: CHAIN, allowance: AMOUNT as bigint | undefined,
  allowanceError: false,
  receipt: undefined as { status: 'success' | 'reverted'; transactionHash: `0x${string}` } | undefined,
  receiptError: null as Error | null,
};
const sendMock = vi.fn();
const eventsMock = vi.fn();
const persistMock = vi.fn();

vi.mock('wagmi', () => ({
  useAccount: () => ({ address: wallet.address, connector: { id: wallet.connectorId } }),
  useChainId: () => wallet.chainId,
  useReadContract: () => ({ data: wallet.allowance, isError: wallet.allowanceError }),
  useSendTransaction: () => ({ mutateAsync: sendMock }),
  useWaitForTransactionReceipt: ({ hash }: { hash?: string }) => ({
    data: hash === wallet.receipt?.transactionHash ? wallet.receipt : undefined,
    isError: Boolean(hash && wallet.receiptError), error: hash ? wallet.receiptError : null,
  }),
}));
vi.mock('@/lib/api/events', () => ({ getEventsByTxHash: (...args: unknown[]) => eventsMock(...args) }));
vi.mock('./web3/usePersistTransactionLifecycle', () => ({
  usePersistTransactionLifecycle: (...args: unknown[]) => persistMock(...args),
}));

const market: TradingMarketView = {
  symbol: 'ETH-USD', indexToken: INDEX, collateralToken: TOKEN,
  indexDecimals: 18, collateralDecimals: 18, pricePrecision: 30, chainId: CHAIN,
  fundingRate: '0', longOpenInterest: '0', shortOpenInterest: '0', volume24h: '0',
};
const position: AggregatedPosition = {
  size: parseUnits('200', 30), collateral: parseUnits('100', 30), averagePrice: PRICE,
  entryFundingRate: 0n, reserveAmount: 0n, realisedPnl: 0n, lastUpdatedAt: 1n,
  hasPosition: true, isLong: true, market,
};

function renderTrading(overrides: Partial<UseTradingTransactionsParams> = {}) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  const initialProps: UseTradingTransactionsParams = {
    address: OWNER, addrs: { positionManager: MANAGER, usdc: TOKEN }, chainId: CHAIN,
    currentPrice: PRICE, selectedMarket: market, positions: [position], viewerKey: OWNER,
    openParams: { collateralAmount: '100', leverage: 2, isLong: true, slippagePercent: 0.5, deadlineMinutes: 20 },
    refetchAll: vi.fn(), refetchActivity: vi.fn(), ...overrides,
  };
  const wrapper = ({ children }: { children: ReactNode }) =>
    createElement(QueryClientProvider, { client }, children);
  return { ...renderHook(useTradingTransactions, { wrapper, initialProps }), initialProps, client };
}

beforeEach(() => {
  vi.spyOn(Date, 'now').mockReturnValue(NOW);
  wallet.address = OWNER; wallet.connectorId = 'injected'; wallet.chainId = CHAIN;
  wallet.allowance = AMOUNT; wallet.receipt = undefined; wallet.receiptError = null;
  wallet.allowanceError = false;
  sendMock.mockReset(); sendMock.mockResolvedValue(HASH);
  eventsMock.mockReset(); eventsMock.mockResolvedValue([]);
  persistMock.mockReset();
});
afterEach(() => vi.restoreAllMocks());

describe('trade review and the shared transaction engine', () => {
  it('approves exactly the reviewed amount, then requires a separate open review and signature', async () => {
    wallet.allowance = 0n;
    const { result, rerender, initialProps } = renderTrading();
    act(() => { expect(result.current.reviewOpen()).toBeNull(); });
    const approve = result.current.intent.activeStep;
    expect(approve?.kind).toBe('approve');
    expect(sendMock).not.toHaveBeenCalled();
    await act(async () => { await result.current.intent.send(); });
    const sent = sendMock.mock.calls[0][0];
    expect(sent).toEqual({ to: TOKEN, data: approve?.calldata, value: 0n, chainId: CHAIN,
      account: OWNER, connector: { id: 'injected' } });
    expect(decodeFunctionData({ abi: erc20Abi, data: sent.data })).toEqual({
      functionName: 'approve', args: [MANAGER, AMOUNT],
    });
    wallet.allowance = AMOUNT;
    wallet.receipt = { status: 'success', transactionHash: HASH };
    rerender(initialProps);
    expect(result.current.intent.state).toBe('FINALIZED');
    expect(result.current.intent.indexing).toBe('not-expected');
    expect(eventsMock).not.toHaveBeenCalled();
    expect(sendMock).toHaveBeenCalledTimes(1);
    act(() => { expect(result.current.reviewOpen()).toBeNull(); });
    expect(result.current.intent.activeStep?.actionType).toBe('open-position');
    expect(result.current.intent.state).toBe('READY_FOR_SIGNATURE');
    expect(sendMock).toHaveBeenCalledTimes(1);
    sendMock.mockResolvedValue(HASH2);
    await act(async () => { await result.current.intent.send(); });
    expect(sendMock).toHaveBeenCalledTimes(2);
    expect(result.current.intent.state).toBe('SUBMITTED');
  });

  it('sends the exact reviewed open calldata and records its frozen owner', async () => {
    const { result } = renderTrading();
    act(() => { expect(result.current.reviewOpen()).toBeNull(); });
    const step = result.current.intent.activeStep;
    expect(sendMock).not.toHaveBeenCalled();
    await act(async () => { await result.current.intent.send(); });
    const sent = sendMock.mock.calls[0][0];
    expect(sent).toEqual({ to: MANAGER, data: step?.calldata, value: 0n, chainId: CHAIN,
      account: OWNER, connector: { id: 'injected' } });
    expect(decodeFunctionData({ abi: CONTRACT_ABIS.PositionManager, data: sent.data })).toEqual({
      functionName: 'openPosition',
      args: [INDEX, TOKEN, AMOUNT, parseUnits('200', 30), true, parseUnits('2010', 30), BigInt(NOW / 1000 + 1200)],
    });
    expect(persistMock).toHaveBeenLastCalledWith(expect.objectContaining({
      hash: HASH, fromAddress: OWNER, txType: 'open-position', enabled: true,
    }));
  });

  it.each([false, true])('rejects an edited ticket, including a captured modal callback (%s)', async (captured) => {
    const { result, rerender, initialProps } = renderTrading();
    act(() => { result.current.reviewOpen(); });
    // A modal can retain the old Sign callback even after the ticket re-renders.
    const oldSend = result.current.intent.send;
    rerender({ ...initialProps, openParams: { ...initialProps.openParams, collateralAmount: '99' } });
    await act(async () => { await (captured ? oldSend : result.current.intent.send)(); });
    expect(sendMock).not.toHaveBeenCalled();
    expect(result.current.intent.invalidationReason).toBe('DRIFT_AT_SEND');
  });

  it('retains frozen price and deadline when a new oracle price stays inside the guard', async () => {
    const { result, rerender, initialProps } = renderTrading();
    act(() => { result.current.reviewOpen(); });
    const reviewed = result.current.intent.activeStep;
    vi.mocked(Date.now).mockReturnValue(NOW + 30_000);
    rerender({ ...initialProps, currentPrice: parseUnits('2005', 30) });
    await act(async () => { await result.current.intent.send(); });
    expect(sendMock).toHaveBeenCalledWith(expect.objectContaining({ data: reviewed?.calldata }));
    const decoded = decodeFunctionData({ abi: CONTRACT_ABIS.PositionManager, data: sendMock.mock.calls[0][0].data });
    expect(decoded.args?.slice(-2)).toEqual([parseUnits('2010', 30), BigInt(NOW / 1000 + 1200)]);
  });

  it.each([parseUnits('2011', 30), undefined, 0n])('refuses a non-executable live oracle price %s', async (currentPrice) => {
    const { result, rerender, initialProps } = renderTrading();
    act(() => { result.current.reviewOpen(); });
    rerender({ ...initialProps, currentPrice });
    await act(async () => { await result.current.intent.send(); });
    expect(sendMock).not.toHaveBeenCalled();
    expect(result.current.intent.invalidationReason).toBe('DRIFT_AT_SEND');
  });

  it('returns a stable price-unavailable code before constructing an unguarded review', () => {
    const { result } = renderTrading({ currentPrice: undefined });
    act(() => { expect(result.current.reviewOpen()).toBe('PRICE_UNAVAILABLE'); });
    expect(result.current.intent.activeStep).toBeNull();
    expect(sendMock).not.toHaveBeenCalled();
  });

  it('blocks an open review when a failed allowance read retains stale data', () => {
    wallet.allowanceError = true;
    const { result } = renderTrading();
    act(() => { expect(result.current.reviewOpen()).toBe('UNKNOWN'); });
    expect(result.current.intent.activeStep).toBeNull();
    expect(sendMock).not.toHaveBeenCalled();
  });

  it('blocks an already reviewed open when the allowance read becomes unavailable', async () => {
    const { result, rerender, initialProps } = renderTrading();
    act(() => { expect(result.current.reviewOpen()).toBeNull(); });
    // Query errors can retain their last successful allowance value.
    wallet.allowanceError = true;
    rerender(initialProps);
    await act(async () => { await result.current.intent.send(); });
    expect(sendMock).not.toHaveBeenCalled();
    expect(result.current.intent.invalidationReason).toBe('DRIFT_AT_SEND');
  });

  it('rejects a one-minute deadline inside the wallet safety margin with a stable code', () => {
    const { result } = renderTrading({ openParams: {
      collateralAmount: '100', leverage: 2, isLong: true, deadlineMinutes: 1,
    } });
    act(() => { expect(result.current.reviewOpen()).toBe('DEADLINE_EXPIRED'); });
    act(() => { expect(result.current.reviewClose(position)).toBe('DEADLINE_EXPIRED'); });
    expect(result.current.intent.activeStep).toBeNull();
    expect(sendMock).not.toHaveBeenCalled();
  });

  it('gates the wrong wallet chain and only rebuilds a review after switching', async () => {
    const { result, rerender, initialProps } = renderTrading();
    act(() => { result.current.reviewOpen(); });
    wallet.chainId = 1;
    rerender(initialProps);
    await act(async () => { await result.current.intent.send(); });
    expect(result.current.intent.errorCode).toBe('WRONG_CHAIN');
    expect(sendMock).not.toHaveBeenCalled();
    wallet.chainId = CHAIN;
    rerender(initialProps);
    act(() => { expect(result.current.retry()).toBeNull(); });
    expect(result.current.intent.state).toBe('READY_FOR_SIGNATURE');
    expect(sendMock).not.toHaveBeenCalled();
  });
});

describe('a close review follows the authoritative position', () => {
  it('sends the reviewed full position size and the close-side price guard', async () => {
    const { result } = renderTrading();
    act(() => { expect(result.current.reviewClose(position)).toBeNull(); });
    expect(sendMock).not.toHaveBeenCalled();
    await act(async () => { await result.current.intent.send(); });
    expect(decodeFunctionData({ abi: CONTRACT_ABIS.PositionManager, data: sendMock.mock.calls[0][0].data })).toEqual({
      functionName: 'closePosition',
      args: [INDEX, TOKEN, 0n, position.size, true, parseUnits('1990', 30), BigInt(NOW / 1000 + 1200)],
    });
  });

  it.each(['size', 'market', 'removed'] as const)('blocks signing after the authoritative position changes: %s', async (change) => {
    const { result, rerender, initialProps } = renderTrading();
    act(() => { expect(result.current.reviewClose(position)).toBeNull(); });
    const positions = change === 'removed' ? [] : [{ ...position,
      size: change === 'size' ? position.size + 1n : position.size,
      market: change === 'market' ? { ...market, indexToken: OTHER_INDEX } : market,
    }];
    rerender({ ...initialProps, positions });
    await act(async () => { await result.current.intent.send(); });
    expect(sendMock).not.toHaveBeenCalled();
    expect(result.current.intent.invalidationReason).toBe('DRIFT_AT_SEND');
  });

  it('cannot price an old-market close with the newly selected market oracle', async () => {
    const { result, rerender, initialProps } = renderTrading();
    act(() => { result.current.reviewClose(position); });
    rerender({ ...initialProps, selectedMarket: { ...market, symbol: 'BTC-USD', indexToken: OTHER_INDEX } });
    await act(async () => { await result.current.intent.send(); });
    expect(sendMock).not.toHaveBeenCalled();
  });

  it.each(['lastUpdatedAt', 'collateral', 'averagePrice'] as const)(
    'invalidates a same-size position with changed %s and retries from the latest snapshot', async (field) => {
      const { result, rerender, initialProps } = renderTrading();
      act(() => { expect(result.current.reviewClose(position)).toBeNull(); });
      const latestPosition = { ...position, [field]: position[field] + 1n };
      rerender({ ...initialProps, positions: [latestPosition] });
      await act(async () => { await result.current.intent.send(); });
      expect(sendMock).not.toHaveBeenCalled();
      expect(result.current.intent.invalidationReason).toBe('DRIFT_AT_SEND');
      act(() => { expect(result.current.retry()).toBeNull(); });
      expect(result.current.intent.state).toBe('READY_FOR_SIGNATURE');
      expect(sendMock).not.toHaveBeenCalled();
      await act(async () => { await result.current.intent.send(); });
      expect(sendMock).toHaveBeenCalledTimes(1);
      expect(result.current.intent.state).toBe('SUBMITTED');
    },
  );

  it('re-reviews a changed close size before signing the authoritative new size', async () => {
    const { result, rerender, initialProps } = renderTrading();
    act(() => { result.current.reviewClose(position); });
    const latestPosition = { ...position, size: position.size + 1n };
    rerender({ ...initialProps, positions: [latestPosition] });
    await act(async () => { await result.current.intent.send(); });
    expect(sendMock).not.toHaveBeenCalled();
    act(() => { expect(result.current.retry()).toBeNull(); });
    expect(sendMock).not.toHaveBeenCalled();
    await act(async () => { await result.current.intent.send(); });
    const decoded = decodeFunctionData({ abi: CONTRACT_ABIS.PositionManager, data: sendMock.mock.calls[0][0].data });
    expect(decoded.functionName).toBe('closePosition');
    expect(decoded.args?.[3]).toBe(latestPosition.size);
  });
});

describe('trade completion is evidence based', () => {
  it.each([
    [{ code: 4001 }, 'REJECTED_BY_WALLET', 'REJECTED_BY_WALLET'],
    [{ name: 'InsufficientFundsError', message: 'insufficient funds' }, 'FAILED', 'INSUFFICIENT_FUNDS'],
  ] as const)('reports wallet failures through stable state and code: %s', async (error, state, code) => {
    sendMock.mockRejectedValue(error);
    const { result } = renderTrading();
    act(() => { result.current.reviewOpen(); });
    await act(async () => { await result.current.intent.send(); });
    expect(result.current.intent.state).toBe(state);
    expect(result.current.intent.errorCode).toBe(code);
    expect(result.current.intent.hash).toBeNull();
  });

  it('does not report an open complete until the indexer returns that transaction', async () => {
    const { result, rerender, initialProps, client } = renderTrading();
    act(() => { result.current.reviewOpen(); });
    await act(async () => { await result.current.intent.send(); });
    wallet.receipt = { status: 'success', transactionHash: HASH };
    rerender(initialProps);
    await waitFor(() => expect(eventsMock).toHaveBeenCalledWith(HASH, CHAIN));
    expect(result.current.intent.state).toBe('CONFIRMING');
    expect(result.current.intent.indexing).toBe('pending');
    expect(initialProps.refetchAll).not.toHaveBeenCalled();
    eventsMock.mockResolvedValue([{ id: 1 }]);
    await act(async () => { await client.invalidateQueries({ queryKey: ['tx-intent-indexed', CHAIN, HASH] }); });
    await waitFor(() => expect(result.current.intent.state).toBe('FINALIZED'));
    expect(result.current.intent.indexing).toBe('indexed');
    expect(initialProps.refetchAll).toHaveBeenCalledTimes(1);
    expect(initialProps.refetchActivity).toHaveBeenCalledTimes(1);
  });

  it('a reverted receipt does not become an indexed success', async () => {
    const { result, rerender, initialProps } = renderTrading();
    act(() => { result.current.reviewOpen(); });
    await act(async () => { await result.current.intent.send(); });
    wallet.receipt = { status: 'reverted', transactionHash: HASH };
    rerender(initialProps);
    expect(result.current.intent.state).toBe('REVERTED');
    expect(eventsMock).not.toHaveBeenCalled();
    expect(initialProps.refetchAll).not.toHaveBeenCalled();
  });
});
