import { act, cleanup, renderHook } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { QueryDetailResponse, QueryListResponse } from '../model/contract.generated';
import { useCampaignDetail, useCampaignList } from './use-campaign-public';
import { campaign, clock, deferred, mockAPI } from './read.test-fixtures';

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date(clock.serverNow));
  vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible');
  vi.spyOn(navigator, 'onLine', 'get').mockReturnValue(true);
});
afterEach(() => { cleanup(); vi.useRealTimers(); vi.restoreAllMocks(); });
const flush = () => act(async () => {});

describe('public campaign read lifecycle', () => {
  it('polls every 15s and coalesces focus/online while one request is pending', async () => {
    const api = mockAPI();
    const pending = deferred<QueryListResponse>();
    api.list.mockReturnValueOnce(pending.promise);
    const view = renderHook(() => useCampaignList({ api, locale: 'en' }));
    await flush();
    await act(async () => {
      window.dispatchEvent(new Event('focus'));
      window.dispatchEvent(new Event('online'));
      await vi.advanceTimersByTimeAsync(45_000);
    });
    expect(api.list).toHaveBeenCalledTimes(1);
    await act(async () => pending.resolve({ ...clock, items: [campaign()], nextCursor: null }));
    expect(api.list).toHaveBeenCalledTimes(2);
    expect(view.result.current.status).toBe('ready');
    await act(async () => vi.advanceTimersByTimeAsync(14_999));
    expect(api.list).toHaveBeenCalledTimes(2);
    await act(async () => vi.advanceTimersByTimeAsync(1));
    expect(api.list).toHaveBeenCalledTimes(3);
  });

  it('aborts hidden reads, ignores their late data and resumes immediately on visibility', async () => {
    const api = mockAPI();
    const pending = deferred<QueryListResponse>();
    api.list.mockReturnValueOnce(pending.promise);
    const view = renderHook(() => useCampaignList({ api, locale: 'en' }));
    await flush();
    await act(async () => {
      vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('hidden');
      document.dispatchEvent(new Event('visibilitychange'));
      await vi.advanceTimersByTimeAsync(60_000);
    });
    expect(api.list.mock.calls[0][1]?.aborted).toBe(true);
    expect(api.list).toHaveBeenCalledTimes(1);
    await act(async () => pending.resolve({ ...clock, items: [campaign('old-hidden')], nextCursor: null }));
    expect(view.result.current.items).toEqual([]);
    await act(async () => {
      vi.spyOn(document, 'visibilityState', 'get').mockReturnValue('visible');
      document.dispatchEvent(new Event('visibilitychange'));
    });
    expect(api.list).toHaveBeenCalledTimes(2);
    expect(view.result.current.items[0].id).toBe('campaign-1');
    expect(view.result.current.writesReady).toBe(true);
    view.unmount();
    await act(async () => {
      window.dispatchEvent(new Event('focus'));
      await vi.advanceTimersByTimeAsync(60_000);
    });
    expect(api.list).toHaveBeenCalledTimes(2);
    expect(vi.getTimerCount()).toBe(0);
  });

  it('calibrates transition timers from server time despite a skewed client clock', async () => {
    vi.setSystemTime(new Date('2030-01-01T00:00:00Z'));
    const api = mockAPI();
    api.detail.mockResolvedValueOnce({ ...clock, item: { ...campaign(), effectivePhase: 'upcoming', nextTransitionAt: '2026-10-08T00:00:05Z' } });
    api.detail.mockResolvedValueOnce({ ...clock, serverNow: '2026-10-08T00:00:05Z', item: { ...campaign(), effectivePhase: 'active' } });
    const view = renderHook(() => useCampaignDetail({ api, locale: 'en', id: 'campaign-1' }));
    await flush();
    expect(view.result.current.clockOffsetMs).toBeLessThan(-100_000);
    await act(async () => vi.advanceTimersByTimeAsync(4_999));
    expect(api.detail).toHaveBeenCalledTimes(1);
    await act(async () => vi.advanceTimersByTimeAsync(1));
    expect(api.detail).toHaveBeenCalledTimes(2);
    expect(view.result.current.item?.publicationVersion).toBe('1');
    expect(view.result.current.item?.effectivePhase).toBe('active');
  });

  it('prevents lower publication versions and older transport time from rolling back detail', async () => {
    const api = mockAPI();
    api.detail.mockResolvedValueOnce({ ...clock, item: campaign('campaign-1', '5') });
    api.detail.mockResolvedValueOnce({ ...clock, serverNow: '2026-10-08T00:00:01Z', item: campaign('campaign-1', '4') });
    api.detail.mockResolvedValueOnce({ ...clock, serverNow: '2026-10-07T23:59:59Z', item: campaign('campaign-1', '6') });
    const view = renderHook(() => useCampaignDetail({ api, locale: 'en', id: 'campaign-1' }));
    await flush();
    await act(async () => view.result.current.refresh());
    expect(view.result.current.item?.publicationVersion).toBe('5');
    expect(view.result.current.freshIds).toEqual([]);
    expect(view.result.current.writesReady).toBe(false);
    await act(async () => view.result.current.refresh());
    expect(view.result.current.item?.publicationVersion).toBe('5');
  });

  it('invalidates locale/detail scope and rejects a previous route response', async () => {
    const api = mockAPI();
    const old = deferred<QueryDetailResponse>();
    api.detail.mockReturnValueOnce(old.promise);
    const view = renderHook(({ id, locale }) => useCampaignDetail({ api, id, locale }), { initialProps: { id: 'old' as string | null, locale: 'en' } });
    await flush();
    const refreshOld = view.result.current.refresh;
    view.rerender({ id: 'campaign-1', locale: 'zh' });
    expect(view.result.current.item).toBeNull();
    await flush();
    await act(async () => old.resolve({ ...clock, item: campaign('old', '9') }));
    expect(view.result.current.item?.id).toBe('campaign-1');
    await act(async () => refreshOld());
    expect(api.detail).toHaveBeenCalledTimes(2);
    view.rerender({ id: null, locale: 'zh' });
    expect(view.result.current.status).toBe('idle');
    expect(view.result.current.item).toBeNull();
  });

  it('keeps weak pagination stable until explicit refresh acknowledges a changed head', async () => {
    const api = mockAPI();
    api.list.mockResolvedValueOnce({ ...clock, items: [campaign('a')], nextCursor: 'cursor1' });
    api.list.mockResolvedValueOnce({ ...clock, items: [campaign('b')], nextCursor: 'cursor2' });
    api.list.mockResolvedValue({ ...clock, serverNow: '2026-10-08T00:00:01Z', items: [campaign('new-head')], nextCursor: 'cursor3' });
    const view = renderHook(() => useCampaignList({ api, locale: 'en' }));
    await flush();
    await act(async () => view.result.current.loadMore());
    expect(api.list.mock.calls[1][0]).toEqual({ cursor: 'cursor1' });
    expect(view.result.current.items.map((item) => item.id)).toEqual(['a', 'b']);
    expect(view.result.current.freshIds).toEqual(['b']);
    await act(async () => window.dispatchEvent(new Event('focus')));
    expect(view.result.current.hasUpdates).toBe(true);
    expect(view.result.current.hasMore).toBe(false);
    expect(view.result.current.items.map((item) => item.id)).toEqual(['a', 'b']);
    await act(async () => view.result.current.refresh());
    expect(api.list.mock.calls.at(-1)?.[0]).toEqual({});
    expect(view.result.current.items.map((item) => item.id)).toEqual(['new-head']);
    expect(view.result.current.hasUpdates).toBe(false);
  });

  it('clears admission freshness offline and restores only rows covered by the next head read', async () => {
    const api = mockAPI();
    api.list.mockResolvedValueOnce({ ...clock, items: [campaign('a')], nextCursor: 'c1' });
    api.list.mockResolvedValueOnce({ ...clock, items: [campaign('b')], nextCursor: null });
    const view = renderHook(() => useCampaignList({ api, locale: 'en' }));
    await flush();
    await act(async () => view.result.current.loadMore());
    const pending = deferred<QueryListResponse>();
    api.list.mockReturnValueOnce(pending.promise);
    await act(async () => {
      vi.spyOn(navigator, 'onLine', 'get').mockReturnValue(false);
      window.dispatchEvent(new Event('offline'));
    });
    expect(view.result.current.active).toBe(false);
    expect(view.result.current.freshIds).toEqual([]);
    expect(view.result.current.writesReady).toBe(false);
    await act(async () => {
      vi.spyOn(navigator, 'onLine', 'get').mockReturnValue(true);
      window.dispatchEvent(new Event('online'));
    });
    expect(view.result.current.writesReady).toBe(false);
    await act(async () => pending.resolve({ ...clock, items: [campaign('a')], nextCursor: 'c1' }));
    expect(view.result.current.items.map((item) => item.id)).toEqual(['a', 'b']);
    expect(view.result.current.freshIds).toEqual(['a']);
    expect(view.result.current.writesReady).toBe(true);
    expect(view.result.current.hasUpdates).toBe(false);
  });
});
