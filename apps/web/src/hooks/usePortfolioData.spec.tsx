import { act, renderHook, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { StrictMode, type ReactNode } from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { usePortfolioData, type UsePortfolioDataOptions } from './usePortfolioData';
import { normalizePortfolioAssets, normalizePortfolioSummary, PortfolioApiError, type AtlasPortfolioAssetsResponse } from '@/lib/api/portfolio';

const OWNER = '0x000000000000000000000000000000000000dead';
const OTHER = '0x000000000000000000000000000000000000beef';
const wallet = { address: OWNER as string | undefined, isConnected: true, chainId: 11155111 };
const auth = { isAuthenticated: true, authenticatedAddress: OWNER as string | null };
const getAssets = vi.fn();
const getSummary = vi.fn();

vi.mock('wagmi', () => ({ useAccount: () => wallet, useChainId: () => wallet.chainId }));
vi.mock('@/lib/web3/auth-provider', () => ({ useAuth: () => auth }));
vi.mock('@/lib/api/portfolio', async (importOriginal) => ({
  ...await importOriginal<typeof import('@/lib/api/portfolio')>(),
  getPortfolioAssets: (...args: unknown[]) => getAssets(...args),
  getPortfolioSummary: (...args: unknown[]) => getSummary(...args),
}));

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((res) => { resolve = res; });
  return { promise, resolve };
}

function data(id: string) {
  return normalizePortfolioAssets({ items: [{ id, rawBalance: '100', assetType: 'earn' }] });
}

function render(options: UsePortfolioDataOptions = { targetAddress: OWNER }) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const result = renderHook(usePortfolioData, {
    initialProps: options,
    wrapper: ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>,
  });
  return { ...result, client };
}

beforeEach(() => {
  getAssets.mockReset().mockResolvedValue(data('current'));
  getSummary.mockReset().mockResolvedValue(normalizePortfolioSummary({ assetFilters: { visibleCount: 1 } }));
  wallet.address = OWNER; wallet.isConnected = true; wallet.chainId = 11155111;
  auth.isAuthenticated = true; auth.authenticatedAddress = OWNER;
});

describe('private portfolio identity boundaries', () => {
  it('can finish a current read through React Strict Mode effect teardown/restart', async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const { result } = renderHook(() => usePortfolioData({ targetAddress: OWNER }), {
      wrapper: ({ children }: { children: ReactNode }) => <StrictMode><QueryClientProvider client={client}>{children}</QueryClientProvider></StrictMode>,
    });
    await waitFor(() => expect(result.current.assetsQuery.data?.items[0].id).toBe('current'));
  });

  it.each(['disconnected', 'signed-out', 'viewer-mismatch', 'other-profile'] as const)('never reads private data while %s', async (condition) => {
    if (condition === 'disconnected') wallet.isConnected = false;
    if (condition === 'signed-out') auth.isAuthenticated = false;
    if (condition === 'viewer-mismatch') auth.authenticatedAddress = OTHER;
    const { result } = render({ targetAddress: condition === 'other-profile' ? OTHER : OWNER, includeSummary: true });
    await act(async () => {});
    expect(getAssets).not.toHaveBeenCalled();
    expect(getSummary).not.toHaveBeenCalled();
    expect(result.current.isAuthorized).toBe(false);
    expect(result.current.assetsQuery.data).toBeUndefined();
    if (condition === 'other-profile') expect(result.current.accessState).toBe('other-account');
  });

  it('normalizes viewer/target, keeps Profile cross-chain and never puts tokens in keys', async () => {
    auth.authenticatedAddress = OWNER.toUpperCase();
    const { result, client } = render({ targetAddress: OWNER.toUpperCase() });
    await waitFor(() => expect(result.current.assetsQuery.data).toBeDefined());
    expect(getAssets).toHaveBeenCalledWith(OWNER, undefined, expect.any(AbortSignal));
    expect(getSummary).not.toHaveBeenCalled();
    expect(result.current.scope).toBe('cross-chain');
    expect(client.getQueryCache().getAll()[0].queryKey).toEqual(['private-portfolio', 1, OWNER, OWNER, 'all-chains', 11155111, 'assets']);
  });

  it('hides loaded A immediately on account switch and cannot fill B with a late A response', async () => {
    const oldRefresh = deferred<AtlasPortfolioAssetsResponse>();
    const next = deferred<AtlasPortfolioAssetsResponse>();
    const { result, rerender, client } = render();
    await waitFor(() => expect(result.current.assetsQuery.data?.items[0].id).toBe('current'));
    getAssets.mockReturnValueOnce(oldRefresh.promise).mockReturnValueOnce(next.promise);
    act(() => { void result.current.assetsQuery.refetch(); });
    act(() => { wallet.address = OTHER; auth.authenticatedAddress = OTHER; rerender({ targetAddress: OTHER }); });
    expect(result.current.assetsQuery.data).toBeUndefined();
    const oldSignal = getAssets.mock.calls[1][2] as AbortSignal;
    expect(oldSignal.aborted).toBe(true);
    await act(async () => { oldRefresh.resolve(data('stale-A')); });
    expect(result.current.assetsQuery.data).toBeUndefined();
    await act(async () => { next.resolve(data('B')); });
    await waitFor(() => expect(result.current.assetsQuery.data?.items[0].id).toBe('B'));
    expect(client.getQueryCache().getAll().some((query) => query.queryKey[2] === OWNER)).toBe(false);
  });

  it('logout removes cached identity data and an ignored abort cannot repopulate it after login', async () => {
    const pending = deferred<AtlasPortfolioAssetsResponse>();
    getAssets.mockReturnValueOnce(pending.promise).mockResolvedValueOnce(data('fresh-A'));
    const { result, rerender } = render();
    await waitFor(() => expect(getAssets).toHaveBeenCalledTimes(1));
    act(() => { auth.isAuthenticated = false; auth.authenticatedAddress = null; rerender({ targetAddress: OWNER }); });
    expect(result.current.assetsQuery.data).toBeUndefined();
    expect((getAssets.mock.calls[0][2] as AbortSignal).aborted).toBe(true);
    act(() => { auth.isAuthenticated = true; auth.authenticatedAddress = OWNER; rerender({ targetAddress: OWNER }); });
    await waitFor(() => expect(result.current.assetsQuery.data?.items[0].id).toBe('fresh-A'));
    await act(async () => { pending.resolve(data('stale-A')); });
    expect(result.current.assetsQuery.data?.items[0].id).toBe('fresh-A');
  });

  it('a wallet chain change isolates a pending cross-chain request without changing Profile scope', async () => {
    const pending = deferred<AtlasPortfolioAssetsResponse>();
    getAssets.mockReturnValueOnce(pending.promise).mockResolvedValueOnce(data('after-switch'));
    const { result, rerender } = render();
    await waitFor(() => expect(getAssets).toHaveBeenCalledTimes(1));
    act(() => { wallet.chainId = 31337; rerender({ targetAddress: OWNER }); });
    expect(result.current.assetsQuery.data).toBeUndefined();
    expect((getAssets.mock.calls[0][2] as AbortSignal).aborted).toBe(true);
    await waitFor(() => expect(result.current.assetsQuery.data?.items[0].id).toBe('after-switch'));
    await act(async () => { pending.resolve(data('before-switch')); });
    expect(result.current.assetsQuery.data?.items[0].id).toBe('after-switch');
    expect(getAssets).toHaveBeenLastCalledWith(OWNER, undefined, expect.any(AbortSignal));
  });

  it('changing only the requested chain scope cannot reuse the prior chain inventory', async () => {
    const { result, rerender } = render({ targetAddress: OWNER, chainId: 11155111 });
    await waitFor(() => expect(result.current.assetsQuery.data).toBeDefined());
    getAssets.mockReturnValue(new Promise(() => {}));
    act(() => { rerender({ targetAddress: OWNER, chainId: 31337 }); });
    expect(result.current.assetsQuery.data).toBeUndefined();
    expect(getAssets).toHaveBeenLastCalledWith(OWNER, 31337, expect.any(AbortSignal));
  });
});

describe('private portfolio failure and recovery', () => {
  it('retains same-viewer filtered data with its original timestamp after an ordinary refresh failure', async () => {
    const { result } = render();
    await waitFor(() => expect(result.current.assetsQuery.data).toBeDefined());
    const timestamp = result.current.assetsQuery.dataUpdatedAt;
    getAssets.mockRejectedValueOnce(new PortfolioApiError('RPC unavailable', 503, 'PORTFOLIO_SOURCES_UNAVAILABLE'));
    await act(async () => { await result.current.assetsQuery.refetch(); });
    await waitFor(() => expect(result.current.assetsQuery.isStale).toBe(true));
    expect(result.current.assetsQuery.data?.items[0].id).toBe('current');
    expect(result.current.assetsQuery.dataUpdatedAt).toBe(timestamp);
  });

  it.each([
    [401, 'UNAUTHORIZED'], [403, 'FORBIDDEN'],
    [500, 'PORTFOLIO_VISIBILITY_UNAVAILABLE'], [503, 'PORTFOLIO_VISIBILITY_UNAVAILABLE'],
  ] as const)('hides cached data after %i/%s until a successful read', async (status, code) => {
    const { result } = render({ targetAddress: OWNER, includeSummary: true });
    await waitFor(() => expect(result.current.summaryQuery.data).toBeDefined());
    await waitFor(() => expect(result.current.assetsQuery.data).toBeDefined());
    getAssets.mockRejectedValueOnce(new PortfolioApiError('Private data unavailable', status, code));
    await act(async () => { await result.current.assetsQuery.refetch(); });
    await waitFor(() => expect(result.current.assetsQuery.isError).toBe(true));
    expect(result.current.assetsQuery.data).toBeUndefined();
    expect(result.current.summaryQuery.data).toBeUndefined();
    getAssets.mockRejectedValueOnce(new PortfolioApiError('Network still unavailable', 500));
    await act(async () => { await result.current.assetsQuery.refetch(); });
    expect(result.current.assetsQuery.data).toBeUndefined();
    expect(result.current.summaryQuery.data).toBeUndefined();
    getAssets.mockResolvedValueOnce(data('recovered'));
    await act(async () => { await result.current.assetsQuery.refetch(); });
    await waitFor(() => expect(result.current.assetsQuery.data?.items[0].id).toBe('recovered'));
    expect(result.current.assetsQuery.isStale).toBe(false);
  });

  it('an unavailable summary visibility policy also hides assets cached by the sibling query', async () => {
    const { result } = render({ targetAddress: OWNER, includeSummary: true });
    await waitFor(() => expect(result.current.assetsQuery.data).toBeDefined());
    getSummary.mockRejectedValueOnce(new PortfolioApiError('Visibility unavailable', 503, 'PORTFOLIO_VISIBILITY_UNAVAILABLE'));
    await act(async () => { await result.current.summaryQuery.refetch(); });
    await waitFor(() => expect(result.current.assetsQuery.data).toBeUndefined());
    expect(result.current.summaryQuery.error?.code).toBe('PORTFOLIO_VISIBILITY_UNAVAILABLE');
  });
});
