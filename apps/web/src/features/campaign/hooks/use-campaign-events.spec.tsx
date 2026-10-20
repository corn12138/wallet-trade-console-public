import { act, cleanup, renderHook } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { EventFeedResponse } from '../model/contract.generated';
import { CampaignAPIError, type CampaignAPI, type CampaignSession } from '../model/ports';
import { feed, hint } from '../model/event.test-fixtures';
import { useCampaignDetail } from './use-campaign-public';
import { useCampaignMe } from './use-campaign-personal';
import { campaign, clock, deferred, mockAPI, sample, sessionA, sessionB } from './read.test-fixtures';

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date(clock.serverNow));
  vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible');
  vi.spyOn(navigator, 'onLine', 'get').mockReturnValue(true);
});
afterEach(() => { cleanup(); vi.useRealTimers(); vi.restoreAllMocks(); });
const flush = () => act(async () => {});
function eventAPI() {
  return { ...mockAPI(),
    events: vi.fn<NonNullable<CampaignAPI['events']>>().mockImplementation(async (_id, family, query) => feed(family, { resetRequired: !query.cursor })),
    meEvents: vi.fn<NonNullable<CampaignAPI['meEvents']>>().mockImplementation(async (_session, _id, query) => feed('personal', { resetRequired: !query.cursor })),
  };
}

describe('campaign event recovery lifecycle', () => {
  it('wires both public families through one bounded detail read timer and closes the bootstrap race', async () => {
    const api = eventAPI();
    const order: string[] = [];
    api.events.mockImplementation(async (_id, family, query) => {
      order.push(`${family}:${query.cursor ?? 'anchor'}`);
      return feed(family, { resetRequired: !query.cursor, ...(query.cursor && family === 'publication' ? { headVersion: '2', cursor: 'cursor2', events: [hint(2)] } : {}) });
    });
    api.detail.mockImplementationOnce(async () => { order.push('REST1'); return { ...clock, item: campaign() }; });
    api.detail.mockImplementationOnce(async () => { order.push('REST2'); return { ...clock, item: campaign('campaign-1', '2') }; });
    const view = renderHook(() => useCampaignDetail({ api, locale: 'en', id: 'campaign-1' }));
    await flush();
    expect(order).toEqual(['publication:anchor', 'interaction:anchor', 'REST1', 'publication:cursor1', 'interaction:cursor1', 'REST2']);
    expect(view.result.current.item?.publicationVersion).toBe('2');
    expect(view.result.current.writesReady).toBe(true);
    expect(vi.getTimerCount()).toBe(1);
    await act(async () => vi.advanceTimersByTimeAsync(14_999));
    expect(api.detail).toHaveBeenCalledTimes(2);
    await act(async () => vi.advanceTimersByTimeAsync(1));
    expect(api.detail).toHaveBeenCalledTimes(3);
    expect(vi.getTimerCount()).toBe(1);
  });

  it('does not count slow event requests as REST clock skew or delay a known transition', async () => {
    const api = eventAPI();
    const anchor = deferred<EventFeedResponse>();
    api.events.mockReturnValueOnce(anchor.promise);
    api.detail.mockImplementationOnce(async () => ({ ...clock, serverNow: new Date().toISOString(), item: {
      ...campaign(), nextTransitionAt: new Date(Date.now() + 5_000).toISOString(),
    } }));
    const view = renderHook(() => useCampaignDetail({ api, locale: 'en', id: 'campaign-1' }));
    await flush();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(4_000);
      anchor.resolve(feed());
    });
    expect(view.result.current.clockOffsetMs).toBeCloseTo(0, 0);
    await act(async () => vi.advanceTimersByTimeAsync(4_999));
    expect(api.detail).toHaveBeenCalledTimes(1);
    await act(async () => vi.advanceTimersByTimeAsync(1));
    expect(api.detail).toHaveBeenCalledTimes(2);
  });

  it('withholds admission and repairs a time-only boundary crossed during post-snapshot catch-up', async () => {
    const api = eventAPI();
    const catchup = deferred<EventFeedResponse>();
    const repaired = deferred<Awaited<ReturnType<CampaignAPI['detail']>>>();
    api.events.mockImplementation(async (_id, family, query) => {
      if (family === 'publication' && query.cursor) return catchup.promise;
      return feed(family, { resetRequired: !query.cursor });
    });
    api.detail.mockResolvedValueOnce({ ...clock, item: { ...campaign(), nextTransitionAt: '2026-10-08T00:00:05Z' } })
      .mockReturnValueOnce(repaired.promise);
    const view = renderHook(() => useCampaignDetail({ api, locale: 'en', id: 'campaign-1' }));
    await flush();
    expect(api.detail).toHaveBeenCalledTimes(1);
    await act(async () => {
      await vi.advanceTimersByTimeAsync(8_000);
      catchup.resolve(feed('publication', { resetRequired: false }));
    });
    expect(api.detail).toHaveBeenCalledTimes(2);
    expect(view.result.current.freshIds).toEqual([]);
    expect(view.result.current.writesReady).toBe(false);
    await act(async () => repaired.resolve({ ...clock, serverNow: new Date().toISOString(), item: { ...campaign(), effectivePhase: 'ended' } }));
    expect(view.result.current.item?.effectivePhase).toBe('ended');
    expect(view.result.current.writesReady).toBe(true);
    expect(vi.getTimerCount()).toBe(1);
  });

  it('coalesces focus and manual refresh behind a pending feed without overlapping snapshots', async () => {
    const api = eventAPI();
    const anchor = deferred<EventFeedResponse>();
    api.events.mockReturnValueOnce(anchor.promise);
    const view = renderHook(() => useCampaignDetail({ api, locale: 'en', id: 'campaign-1' }));
    await flush();
    await act(async () => {
      view.result.current.refresh();
      window.dispatchEvent(new Event('focus'));
      await vi.advanceTimersByTimeAsync(45_000);
    });
    expect(api.events).toHaveBeenCalledTimes(1);
    expect(api.detail).not.toHaveBeenCalled();
    await act(async () => anchor.resolve(feed()));
    expect(api.detail).toHaveBeenCalledTimes(2);
    expect(vi.getTimerCount()).toBe(1);
    view.unmount();
    expect(vi.getTimerCount()).toBe(0);
  });

  it('suspends offline, ignores a late feed, and resumes by anchoring before a fresh REST snapshot', async () => {
    const api = eventAPI();
    const anchor = deferred<EventFeedResponse>();
    api.meEvents.mockReturnValueOnce(anchor.promise);
    const view = renderHook(() => useCampaignMe({ api, session: sessionA, ids: ['campaign-1'] }));
    await flush();
    await act(async () => {
      vi.spyOn(navigator, 'onLine', 'get').mockReturnValue(false);
      window.dispatchEvent(new Event('offline'));
      await vi.advanceTimersByTimeAsync(60_000);
      anchor.resolve(feed('personal'));
    });
    expect(api.meEvents.mock.calls[0][3]?.aborted).toBe(true);
    expect(api.me).not.toHaveBeenCalled();
    expect(view.result.current.writesReady).toBe(false);
    await act(async () => {
      vi.spyOn(navigator, 'onLine', 'get').mockReturnValue(true);
      window.dispatchEvent(new Event('online'));
    });
    expect(api.meEvents.mock.calls[1][2]).toEqual({ limit: 50 });
    expect(api.me).toHaveBeenCalledTimes(1);
    expect(view.result.current.items).toHaveLength(1);
    view.unmount();
    await act(async () => { window.dispatchEvent(new Event('focus')); await vi.advanceTimersByTimeAsync(60_000); });
    expect(api.me).toHaveBeenCalledTimes(1);
    expect(vi.getTimerCount()).toBe(0);
  });

  it('keeps cursor and late responses separate across A to B to A session generations', async () => {
    const api = eventAPI();
    const oldA = deferred<EventFeedResponse>();
    const oldB = deferred<EventFeedResponse>();
    api.meEvents.mockReturnValueOnce(oldA.promise).mockReturnValueOnce(oldB.promise);
    const view = renderHook(({ session }) => useCampaignMe({ api, session, ids: ['campaign-1'] }), { initialProps: { session: sessionA as CampaignSession | null } });
    await flush();
    view.rerender({ session: sessionB });
    expect(view.result.current.items).toEqual([]);
    await flush();
    const newSession = { ...sessionA, generation: 3, token: 'token-a3' };
    view.rerender({ session: newSession });
    expect(view.result.current.items).toEqual([]);
    await flush();
    expect(api.me.mock.calls[0][0]).toEqual(newSession);
    expect(api.meEvents.mock.calls.slice(0, 3).map((call) => call[2])).toEqual([{ limit: 50 }, { limit: 50 }, { limit: 50 }]);
    await act(async () => { oldA.resolve(feed('personal')); oldB.resolve(feed('personal')); });
    expect(api.me).toHaveBeenCalledTimes(1);
    expect(api.meEvents.mock.calls[0][3]?.aborted).toBe(true);
    expect(api.meEvents.mock.calls[1][3]?.aborted).toBe(true);
    view.rerender({ session: null });
    expect(view.result.current.items).toEqual([]);
    expect(vi.getTimerCount()).toBe(0);
  });

  it('hides loaded personal data and stops periodic polling after feed session expiry', async () => {
    const api = eventAPI();
    const view = renderHook(() => useCampaignMe({ api, session: sessionA, ids: ['campaign-1'] }));
    await flush();
    expect(view.result.current.items).toHaveLength(1);
    api.meEvents.mockRejectedValueOnce(new CampaignAPIError('UNAUTHORIZED', 401));
    await act(async () => vi.advanceTimersByTimeAsync(15_000));
    expect(view.result.current.error?.status).toBe(401);
    expect(view.result.current.items).toEqual([]);
    expect(view.result.current.writesReady).toBe(false);
    expect(vi.getTimerCount()).toBe(0);
    expect(api.me).toHaveBeenCalledTimes(1);
  });

  it('keeps multi-campaign personal reads batched without opening per-row feeds', async () => {
    const api = eventAPI();
    const data = sample('QueryMeResponse');
    data.items.push({ ...data.items[0], id: 'campaign-2' });
    api.me.mockResolvedValue(data);
    const view = renderHook(() => useCampaignMe({ api, session: sessionA, ids: ['campaign-1', 'campaign-2'] }));
    await flush();
    expect(view.result.current.items).toHaveLength(2);
    expect(api.meEvents).not.toHaveBeenCalled();
    expect(api.me).toHaveBeenCalledTimes(1);
    expect(vi.getTimerCount()).toBe(1);
  });
});
