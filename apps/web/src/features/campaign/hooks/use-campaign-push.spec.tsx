import { act, cleanup, renderHook } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { feed } from '../model/event.test-fixtures';
import { CampaignAPIError, type CampaignAPI, type CampaignPush, type CampaignPushKey, type CampaignPushListener, type CampaignSession } from '../model/ports';
import { useCampaignMe } from './use-campaign-personal';
import { useCampaignDetail, useCampaignList } from './use-campaign-public';
import { useCampaignHistory } from './use-campaign-personal';
import { campaign, clock, deferred, mockAPI, sample, sessionA, sessionB } from './read.test-fixtures';

let visibility: DocumentVisibilityState = 'visible';
beforeEach(() => {
  visibility = 'visible';
  vi.useFakeTimers();
  vi.setSystemTime(new Date(clock.serverNow));
  vi.spyOn(document, 'visibilityState', 'get').mockImplementation(() => visibility);
  vi.spyOn(navigator, 'onLine', 'get').mockReturnValue(true);
});
afterEach(() => { cleanup(); vi.useRealTimers(); vi.restoreAllMocks(); });
const flush = () => act(async () => {});

type Watch = { key: CampaignPushKey; session: CampaignSession | null; listener: CampaignPushListener; live: boolean };
function fakePush() {
  const watches: Watch[] = [];
  const push: CampaignPush = {
    watch: vi.fn((key, session, listener) => {
      const watch = { key, session, listener, live: true };
      watches.push(watch);
      return () => { watch.live = false; };
    }),
  };
  const live = () => watches.filter((w) => w.live);
  return { push, watches, live };
}
function pushAPI() {
  const fake = fakePush();
  const api = { ...mockAPI(), push: fake.push,
    events: vi.fn<NonNullable<CampaignAPI['events']>>().mockImplementation(async (_id, family, query) => feed(family, { resetRequired: !query.cursor })),
    meEvents: vi.fn<NonNullable<CampaignAPI['meEvents']>>().mockImplementation(async (_s, _id, query) => feed('personal', { resetRequired: !query.cursor })),
  };
  return { api, ...fake };
}

describe('campaign push integration with authoritative reads', () => {
  it('watches both public families for a detail scope and repairs through REST on every hint', async () => {
    const { api, live } = pushAPI();
    const view = renderHook(() => useCampaignDetail({ api, locale: 'en', id: 'campaign-1' }));
    await flush();
    expect(live().map((w) => [w.key, w.session])).toEqual([[{ campaignId: 'campaign-1', family: 'publication' }, null], [{ campaignId: 'campaign-1', family: 'interaction' }, null]]);
    expect(api.detail).toHaveBeenCalledTimes(1);
    // Acknowledgement: a catch-up runs after the server baseline exists.
    await act(async () => live()[0].listener.subscribed());
    expect(api.detail).toHaveBeenCalledTimes(2);
    api.detail.mockResolvedValueOnce({ ...clock, item: campaign('campaign-1', '2') });
    await act(async () => live()[1].listener.invalidate(3n));
    expect(api.detail).toHaveBeenCalledTimes(3);
    expect(view.result.current.item?.publicationVersion).toBe('2');
    expect(api.events.mock.calls.some(([, , query]) => query.cursor)).toBe(true);
  });

  it('coalesces a burst of hints with the in-flight read instead of looping', async () => {
    // Snapshot-only scope: the hook's own single-flight bound, without the
    // feed recovery's generation check as a second layer.
    const { api: full, live } = pushAPI();
    const { events: _events, meEvents: _meEvents, ...api } = full;
    renderHook(() => useCampaignDetail({ api, locale: 'en', id: 'campaign-1' }));
    await flush();
    const slow = deferred<Awaited<ReturnType<CampaignAPI['detail']>>>();
    api.detail.mockReturnValueOnce(slow.promise);
    await act(async () => live()[0].listener.invalidate(2n));
    for (let head = 3n; head < 50n; head++) live()[0].listener.invalidate(head);
    live()[1].listener.reset('session_unavailable');
    await act(async () => slow.resolve({ ...clock, item: campaign() }));
    await flush();
    expect(api.detail).toHaveBeenCalledTimes(3); // initial, the hinted read, one queued repair
  });

  it('a hint alone never marks data fresh or enables writes', async () => {
    const { api, live } = pushAPI();
    api.detail.mockRejectedValue(new CampaignAPIError('CAMPAIGN_UNAVAILABLE', 503));
    const view = renderHook(() => useCampaignDetail({ api, locale: 'en', id: 'campaign-1' }));
    await flush();
    await act(async () => { live()[0].listener.subscribed(); live()[0].listener.invalidate(9n); });
    await flush();
    expect(view.result.current.status).toBe('error');
    expect(view.result.current.writesReady).toBe(false);
    expect(view.result.current.item).toBeNull();
  });

  it('drops watches while hidden and re-watches with a synchronizing read when visible', async () => {
    const { api, live, watches } = pushAPI();
    renderHook(() => useCampaignDetail({ api, locale: 'en', id: 'campaign-1' }));
    await flush();
    const first = live();
    visibility = 'hidden';
    await act(async () => document.dispatchEvent(new Event('visibilitychange')));
    expect(live()).toHaveLength(0);
    const calls = api.detail.mock.calls.length;
    first[0].listener.invalidate(5n); // late hint from the dropped watch
    await flush();
    expect(api.detail).toHaveBeenCalledTimes(calls);
    expect(vi.getTimerCount()).toBe(0);
    visibility = 'visible';
    await act(async () => document.dispatchEvent(new Event('visibilitychange')));
    expect(live()).toHaveLength(2);
    expect(watches).toHaveLength(4);
    expect(api.detail).toHaveBeenCalledTimes(calls + 1);
  });

  it('follows personal scope A→B→A, disposing the old watch before the new owner renders', async () => {
    const { api, live, watches } = pushAPI();
    const owners: string[] = [];
    api.me.mockImplementation(async (session) => { owners.push(session.owner); return { ...sample('QueryMeResponse'), ...clock }; });
    const view = renderHook(({ session }) => useCampaignMe({ api, session, ids: ['campaign-1'] }), { initialProps: { session: sessionA as CampaignSession | null } });
    await flush();
    expect(live().map((w) => [w.key.family, w.session?.owner])).toEqual([['personal', sessionA.owner]]);
    const watchA = live()[0];
    view.rerender({ session: sessionB });
    expect(watchA.live).toBe(false);
    expect(view.result.current.items).toEqual([]);
    await flush();
    const before = owners.length;
    watchA.listener.invalidate(7n); // late A hint after the switch
    watchA.listener.subscribed();
    await flush();
    expect(owners.slice(before)).toEqual([]);
    expect(live().map((w) => w.session?.owner)).toEqual([sessionB.owner]);
    view.rerender({ session: sessionA });
    await flush();
    expect(live().map((w) => w.session?.owner)).toEqual([sessionA.owner]);
    expect(watches.map((w) => w.session?.owner)).toEqual([sessionA.owner, sessionB.owner, sessionA.owner]);
    view.rerender({ session: null });
    expect(live()).toHaveLength(0);
  });

  it('unauthorized reset repairs once through REST and stops polling on 401', async () => {
    const { api, live } = pushAPI();
    const view = renderHook(() => useCampaignMe({ api, session: sessionA, ids: ['campaign-1'] }));
    await flush();
    api.me.mockRejectedValue(new CampaignAPIError('UNAUTHORIZED', 401));
    api.meEvents.mockRejectedValue(new CampaignAPIError('UNAUTHORIZED', 401));
    await act(async () => live()[0].listener.reset('unauthorized'));
    await flush();
    expect(view.result.current.error?.status).toBe(401);
    expect(view.result.current.items).toEqual([]);
    const calls = api.me.mock.calls.length;
    await act(async () => vi.advanceTimersByTimeAsync(60_000));
    expect(api.me).toHaveBeenCalledTimes(calls);
  });

  it('multiple mounts and rapid remounts each own one watch and release it on unmount', async () => {
    const { api, live } = pushAPI();
    const one = renderHook(() => useCampaignDetail({ api, locale: 'en', id: 'campaign-1' }));
    const two = renderHook(() => useCampaignDetail({ api, locale: 'en', id: 'campaign-1' }));
    await flush();
    expect(live()).toHaveLength(4);
    one.unmount();
    expect(live()).toHaveLength(2);
    for (let i = 0; i < 5; i++) renderHook(() => useCampaignDetail({ api, locale: 'en', id: 'campaign-1' })).unmount();
    expect(live()).toHaveLength(2);
    two.unmount();
    expect(live()).toHaveLength(0);
  });

  it('list, history and multi-campaign personal reads keep polling without push identities', async () => {
    const { api, live } = pushAPI();
    renderHook(() => useCampaignList({ api, locale: 'en' }));
    renderHook(() => useCampaignHistory({ api, session: sessionA }));
    renderHook(() => useCampaignMe({ api, session: sessionA, ids: ['campaign-1', 'campaign-2'] }));
    renderHook(() => useCampaignMe({ api, session: null, ids: ['campaign-1'] }));
    await flush();
    expect(live()).toHaveLength(0);
    expect(vi.getTimerCount()).toBe(3);
  });
});
