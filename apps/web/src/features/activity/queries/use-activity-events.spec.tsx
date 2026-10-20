import { act, renderHook, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import type { PropsWithChildren } from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { getUserEvents, type Web3Event } from '@/lib/api/events';
import { useActivityEvents, type ActivityScope } from './use-activity-events';

vi.mock('@/lib/api/events', () => ({ getUserEvents: vi.fn() }));
const read = vi.mocked(getUserEvents);
const a = '0x1111111111111111111111111111111111111111';
const b = '0x2222222222222222222222222222222222222222';
const base: ActivityScope = { address: a, chainId: 1, sessionGeneration: 1, isRestoring: false };
const event = (id: string) => ({ id, eventName: 'Swap' } as Web3Event);

function render(scope = base) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  const wrapper = ({ children }: PropsWithChildren) => <QueryClientProvider client={client}>{children}</QueryClientProvider>;
  return { client, ...renderHook((props: ActivityScope) => useActivityEvents(props), { initialProps: scope, wrapper }) };
}

beforeEach(() => { read.mockReset(); });

describe('transaction activity scope', () => {
  it('ignores A1 and B2 responses after switching back to A3', async () => {
    const requests: Array<{ resolve: (events: Web3Event[]) => void; signal: AbortSignal | undefined }> = [];
    read.mockImplementation((_address, _chain, _limit, signal) => new Promise((resolve) => requests.push({ resolve, signal })));
    const view = render();
    await waitFor(() => expect(requests).toHaveLength(1));
    view.rerender({ ...base, address: b, sessionGeneration: 2 });
    await waitFor(() => expect(requests).toHaveLength(2));
    view.rerender({ ...base, sessionGeneration: 3 });
    await waitFor(() => expect(requests).toHaveLength(3));
    expect(requests[0].signal?.aborted).toBe(true);
    expect(requests[1].signal?.aborted).toBe(true);
    await act(async () => { requests[2].resolve([event('A3')]); });
    await waitFor(() => expect(view.result.current.data[0]?.id).toBe('A3'));
    await act(async () => { requests[0].resolve([event('A1')]); requests[1].resolve([event('B2')]); });
    expect(view.result.current.data.map((item) => item.id)).toEqual(['A3']);
    view.unmount();
  });

  it('clears displayed facts during restore and prevents manual refresh bypass', async () => {
    read.mockResolvedValue([event('known')]);
    const view = render();
    await waitFor(() => expect(view.result.current.data).toHaveLength(1));
    view.rerender({ ...base, sessionGeneration: 2, isRestoring: true });
    expect(view.result.current.data).toEqual([]);
    expect(view.result.current.isLoading).toBe(true);
    await act(async () => { await view.result.current.refetch(); });
    expect(read).toHaveBeenCalledTimes(1);
    view.rerender({ ...base, address: undefined, sessionGeneration: 3 });
    await act(async () => { await view.result.current.refetch(); });
    expect(read).toHaveBeenCalledTimes(1);
    view.unmount();
  });

  it('has an independent query source and exposes read failures', async () => {
    read.mockRejectedValue(new Error('chain read unavailable'));
    const view = render();
    await waitFor(() => expect(view.result.current.isError).toBe(true));
    expect(view.result.current.error?.message).toBe('chain read unavailable');
    expect(view.client.getQueryCache().getAll().map((query) => query.queryKey[0])).toEqual(['transaction-activity']);
    view.unmount();
  });

  it('aborts the owned request on unmount', async () => {
    let signal: AbortSignal | undefined;
    read.mockImplementation((_address, _chain, _limit, value) => { signal = value; return new Promise(() => {}); });
    const view = render();
    await waitFor(() => expect(signal).toBeDefined());
    view.unmount();
    expect(signal?.aborted).toBe(true);
  });
});
