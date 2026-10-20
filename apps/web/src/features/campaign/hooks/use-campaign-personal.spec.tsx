import { act, cleanup, renderHook } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { QueryMeResponse } from '../model/contract.generated';
import { CampaignAPIError, type CampaignSession } from '../model/ports';
import { createCampaignHTTP } from '../adapters/http';
import { useCampaignHistory, useCampaignMe } from './use-campaign-personal';
import { clock, deferred, mockAPI, sample, sessionA, sessionB } from './read.test-fixtures';

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date(clock.serverNow));
  vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible');
  vi.spyOn(navigator, 'onLine', 'get').mockReturnValue(true);
});
afterEach(() => { cleanup(); vi.useRealTimers(); vi.restoreAllMocks(); });
const flush = () => act(async () => {});

describe('private campaign read ownership', () => {
  it('isolates A to B to A and drops both old requests even when they ignore abort', async () => {
    const api = mockAPI();
    const a1 = deferred<QueryMeResponse>();
    const b2 = deferred<QueryMeResponse>();
    const a3 = deferred<QueryMeResponse>();
    api.me.mockReturnValueOnce(a1.promise).mockReturnValueOnce(b2.promise).mockReturnValueOnce(a3.promise);
    const view = renderHook(({ session }) => useCampaignMe({ api, session, ids: ['campaign-1'] }), { initialProps: { session: sessionA as CampaignSession | null } });
    await flush();
    const oldRefresh = view.result.current.refresh;
    view.rerender({ session: sessionB });
    expect(view.result.current.items).toEqual([]);
    await flush();
    expect(api.me.mock.calls[0][2]?.aborted).toBe(true);
    view.rerender({ session: { ...sessionA, generation: 3, token: 'token-a-3' } });
    expect(view.result.current.items).toEqual([]);
    await flush();
    expect(api.me.mock.calls[1][2]?.aborted).toBe(true);
    await act(async () => a1.resolve({ ...sample('QueryMeResponse'), ...clock }));
    expect(view.result.current.items).toEqual([]);
    const current = sample('QueryMeResponse');
    current.items[0].participation.status = 'left';
    current.items[0].participation.stateVersion = '3';
    await act(async () => a3.resolve({ ...current, ...clock }));
    await act(async () => b2.resolve({ ...sample('QueryMeResponse'), ...clock }));
    expect(view.result.current.items[0].participation.stateVersion).toBe('3');
    expect(view.result.current.items[0].participation.status).toBe('left');
    await act(async () => oldRefresh());
    expect(api.me).toHaveBeenCalledTimes(3);
    view.rerender({ session: null });
    expect(view.result.current.items).toEqual([]);
    expect(view.result.current.status).toBe('idle');
  });

  it('masks an already loaded owner synchronously when wallet or generation changes', async () => {
    const api = mockAPI();
    const pending = deferred<QueryMeResponse>();
    const view = renderHook(({ session }) => useCampaignMe({ api, session, ids: ['campaign-1'] }), { initialProps: { session: sessionA } });
    await flush();
    expect(view.result.current.items).toHaveLength(1);
    api.me.mockReturnValue(pending.promise);
    view.rerender({ session: sessionB });
    expect(view.result.current.items).toEqual([]);
    await flush();
    view.unmount();
    expect(api.me.mock.calls[1][2]?.aborted).toBe(true);
    await act(async () => pending.resolve(sample('QueryMeResponse')));
    expect(vi.getTimerCount()).toBe(0);
  });

  it.each([401, 503])('makes refresh error %s unknown without inventing absent or false', async (status) => {
    const api = mockAPI();
    const view = renderHook(() => useCampaignMe({ api, session: sessionA, ids: ['campaign-1'] }));
    await flush();
    expect(view.result.current.items[0].participation.state).toBe('known');
    api.me.mockRejectedValueOnce(new CampaignAPIError('READ_FAILED', status));
    await act(async () => view.result.current.refresh());
    expect(view.result.current.status).toBe('error');
    expect(view.result.current.items).toEqual([]);
    expect(view.result.current.error?.status).toBe(status);
  });

  it('deduplicates and sorts IDs but treats missing Me rows as unknown', async () => {
    const api = mockAPI();
    const view = renderHook(({ ids }) => useCampaignMe({ api, session: sessionA, ids }), { initialProps: { ids: ['campaign-1', 'campaign-1'] } });
    await flush();
    expect(api.me.mock.calls[0][1]).toEqual(['campaign-1']);
    view.rerender({ ids: ['campaign-1'] });
    await flush();
    expect(api.me).toHaveBeenCalledTimes(1);
    view.rerender({ ids: ['campaign-2', 'campaign-1'] });
    expect(view.result.current.items).toEqual([]);
    await flush();
    expect(api.me.mock.calls[1][1]).toEqual(['campaign-1', 'campaign-2']);
    expect(view.result.current.status).toBe('error');
    expect(view.result.current.items).toEqual([]);
  });

  it('retains personal version monotonicity while accepting explicit server unknown', async () => {
    const api = mockAPI();
    const high = sample('QueryMeResponse');
    high.items[0].participation.stateVersion = '5';
    high.items[0].participation.status = 'left';
    api.me.mockResolvedValueOnce({ ...high, ...clock });
    const view = renderHook(() => useCampaignMe({ api, session: sessionA, ids: ['campaign-1'] }));
    await flush();
    await act(async () => view.result.current.refresh());
    expect(view.result.current.items[0].participation.stateVersion).toBe('5');
    const unknown = sample('QueryMeResponse');
    unknown.items[0].participation = { ...unknown.items[0].reminder, state: 'unknown', stateVersion: null };
    api.me.mockResolvedValue({ ...unknown, ...clock });
    await act(async () => view.result.current.refresh());
    expect(view.result.current.items[0].participation.state).toBe('unknown');
    expect(view.result.current.items[0].participation.stateVersion).toBeNull();
  });

  it('uses the real raw decoder so malformed private JSON cannot become an empty success', async () => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(new Response('{"schemaVersion":1,"schemaVersion":1}'));
    const api = createCampaignHTTP({ fetch: fetcher });
    const view = renderHook(() => useCampaignMe({ api, session: sessionA, ids: ['campaign-1'] }));
    await flush();
    expect(view.result.current.status).toBe('error');
    expect(view.result.current.error?.code).toBe('CAMPAIGN_RESPONSE_INVALID');
    expect(view.result.current.items).toEqual([]);
  });

  it('preserves explicit absence at version zero and rejects a mismatched personal status family', async () => {
    const api = mockAPI();
    const absent = sample('QueryMeResponse');
    absent.items[0].participation = { ...absent.items[0].reminder };
    api.me.mockResolvedValueOnce({ ...absent, ...clock });
    const view = renderHook(() => useCampaignMe({ api, session: sessionA, ids: ['campaign-1'] }));
    await flush();
    expect(view.result.current.status).toBe('ready');
    expect(view.result.current.items[0].participation).toMatchObject({ state: 'absent', stateVersion: '0', status: null });
    const mismatched = sample('QueryMeResponse');
    mismatched.items[0].participation.status = 'set';
    api.me.mockResolvedValueOnce({ ...mismatched, ...clock });
    await act(async () => view.result.current.refresh());
    expect(view.result.current.status).toBe('error');
    expect(view.result.current.items).toEqual([]);
  });

  it('retains history independently of the current public IDs and isolates its pages by session', async () => {
    const api = mockAPI();
    const first = sample('QueryHistoryResponse');
    const second = sample('QueryHistoryResponse');
    second.items[0].interactionVersion = '2';
    second.nextCursor = null;
    api.history.mockResolvedValueOnce({ ...first, ...clock }).mockResolvedValueOnce({ ...second, ...clock });
    const view = renderHook(({ session }) => useCampaignHistory({ api, session }), { initialProps: { session: sessionA } });
    await flush();
    await act(async () => view.result.current.loadMore());
    expect(api.history.mock.calls[1][1]).toEqual({ cursor: first.nextCursor });
    expect(view.result.current.items).toHaveLength(2);
    api.history.mockReturnValue(deferred<ReturnType<typeof sample<'QueryHistoryResponse'>>>().promise);
    view.rerender({ session: sessionB });
    expect(view.result.current.items).toEqual([]);
    expect(view.result.current.nextCursor).toBeNull();
  });
});
