import { describe, expect, it, vi } from 'vitest';
import type { EventFeedResponse } from './contract.generated';
import { deferred } from '../hooks/read.test-fixtures';
import { CampaignAPIError } from './ports';
import { createCampaignRecovery, type CampaignEventStream } from './event-recovery';
import { feed, hint } from './event.test-fixtures';

function setup() {
  const events = vi.fn<CampaignEventStream['read']>().mockImplementation(async (_signal, cursor) => feed('publication', { resetRequired: !cursor }));
  const snapshot = vi.fn(async (_signal: AbortSignal) => ({ count: 10 }));
  const recovery = createCampaignRecovery(snapshot, [{ campaignId: 'campaign-1', family: 'publication', read: events }]);
  return { events, snapshot, recovery, signal: new AbortController().signal };
}

describe('campaign invalidation recovery', () => {
  it('anchors before REST and repairs a mutation in the bootstrap window without applying event payloads', async () => {
    const { events, snapshot, recovery, signal } = setup();
    const order: string[] = [];
    events.mockImplementationOnce(async () => { order.push('anchor'); return feed(); });
    snapshot.mockImplementationOnce(async () => { order.push('snapshot-old'); return { count: 10 }; });
    events.mockImplementationOnce(async (_signal, cursor) => {
      order.push(`catchup-${cursor}`);
      return feed('publication', { resetRequired: false, headVersion: '2', cursor: 'cursor2', events: [hint(2)] });
    });
    snapshot.mockImplementationOnce(async () => { order.push('snapshot-current'); return { count: 11 }; });
    expect(await recovery.read(signal)).toEqual({ count: 11 });
    expect(order).toEqual(['anchor', 'snapshot-old', 'catchup-cursor1', 'snapshot-current']);
  });

  it('deduplicates the same event and version but still refreshes authoritative time-based state', async () => {
    const { events, snapshot, recovery, signal } = setup();
    const changed = feed('publication', { resetRequired: false, headVersion: '2', cursor: 'cursor2', events: [hint(2)] });
    events.mockResolvedValueOnce(feed()).mockResolvedValueOnce(changed).mockResolvedValueOnce(changed);
    await recovery.read(signal);
    await recovery.read(signal);
    expect(snapshot).toHaveBeenCalledTimes(3);
    expect(events.mock.calls.map((call) => call[1])).toEqual([undefined, 'cursor1', 'cursor2']);
  });

  it.each(['gap', 'reordered', 'conflicting-identity', 'wrong-stream', 'overflow'])('re-anchors %s and repairs only from REST', async (kind) => {
    const { events, snapshot, recovery, signal } = setup();
    await recovery.read(signal);
    let malformed = feed('publication', { resetRequired: false, headVersion: '4', cursor: 'cursor4', events: [hint(4)] });
    if (kind === 'reordered') malformed = { ...malformed, events: [hint(3), hint(2)] };
    if (kind === 'conflicting-identity') malformed = { ...malformed, events: [hint(2), { ...hint(3), eventId: hint(2).eventId }] };
    if (kind === 'wrong-stream') malformed = { ...malformed, family: 'personal' };
    if (kind === 'overflow') malformed = { ...malformed, headVersion: '9999999999999999999' };
    events.mockResolvedValueOnce(malformed).mockResolvedValueOnce(feed('publication', { headVersion: '4', cursor: 'cursor4' }))
      .mockResolvedValueOnce(feed('publication', { resetRequired: false, headVersion: '4', cursor: 'cursor4' }));
    expect(await recovery.read(signal)).toEqual({ count: 10 });
    expect(events.mock.calls.slice(2).map((call) => call[1])).toEqual(['cursor1', undefined, 'cursor4']);
    expect(snapshot).toHaveBeenCalledTimes(2);
  });

  it('bounds a backlog with one new head anchor instead of recursively draining pages', async () => {
    const { events, snapshot, recovery, signal } = setup();
    await recovery.read(signal);
    events.mockResolvedValueOnce(feed('publication', { resetRequired: false, headVersion: '1000', cursor: 'cursor2', hasMore: true, events: [hint(2)] }))
      .mockResolvedValueOnce(feed('publication', { headVersion: '1000', cursor: 'cursor1000' }))
      .mockResolvedValueOnce(feed('publication', { resetRequired: false, headVersion: '1000', cursor: 'cursor1000' }));
    await recovery.read(signal);
    expect(events).toHaveBeenCalledTimes(5);
    expect(snapshot).toHaveBeenCalledTimes(2);
  });

  it.each(['CAMPAIGN_UNSUPPORTED', 'NETWORK_ERROR'])('falls back to REST after %s and retries a fresh anchor on the next bounded poll', async (code) => {
    const { events, snapshot, recovery, signal } = setup();
    events.mockRejectedValueOnce(new CampaignAPIError(code, code === 'NETWORK_ERROR' ? 0 : 502));
    await recovery.read(signal);
    await recovery.read(signal);
    expect(snapshot).toHaveBeenCalledTimes(2);
    expect(events.mock.calls.map((call) => call[1])).toEqual([undefined, undefined, 'cursor1']);
  });

  it('repairs a rejected cursor with a new anchor before reading REST', async () => {
    const { events, snapshot, recovery, signal } = setup();
    await recovery.read(signal);
    const order: string[] = [];
    events.mockImplementationOnce(async () => { order.push('rejected'); throw new CampaignAPIError('CAMPAIGN_INVALID_INPUT', 400); })
      .mockImplementationOnce(async (_signal, cursor) => { expect(cursor).toBeUndefined(); order.push('anchor'); return feed(); })
      .mockImplementationOnce(async () => { order.push('catchup'); return feed('publication', { resetRequired: false }); });
    snapshot.mockImplementationOnce(async () => { order.push('REST'); return { count: 10 }; });
    await recovery.read(signal);
    expect(order).toEqual(['rejected', 'anchor', 'REST', 'catchup']);
  });

  it('does not turn an expired private session into public snapshot fallback', async () => {
    const { events, snapshot, recovery, signal } = setup();
    events.mockRejectedValueOnce(new CampaignAPIError('UNAUTHORIZED', 401));
    await expect(recovery.read(signal)).rejects.toMatchObject({ status: 401 });
    expect(snapshot).not.toHaveBeenCalled();
  });

  it('never follows a canceled late anchor with a snapshot, even when its transport ignores abort', async () => {
    const { events, snapshot, recovery } = setup();
    const pending = deferred<EventFeedResponse>();
    events.mockReturnValueOnce(pending.promise);
    const controller = new AbortController();
    const read = recovery.read(controller.signal);
    const assertion = expect(read).rejects.toMatchObject({ code: 'ABORTED' });
    controller.abort();
    pending.resolve(feed());
    await assertion;
    expect(snapshot).not.toHaveBeenCalled();
  });

  it('invalidates late snapshots on reset and starts the next read from an empty cursor', async () => {
    const { events, snapshot, recovery, signal } = setup();
    const pending = deferred<{ count: number }>();
    snapshot.mockReturnValueOnce(pending.promise);
    const read = recovery.read(signal);
    await vi.waitFor(() => expect(snapshot).toHaveBeenCalledTimes(1));
    const assertion = expect(read).rejects.toMatchObject({ code: 'ABORTED' });
    recovery.reset();
    await recovery.read(signal);
    pending.resolve({ count: 999 });
    await assertion;
    expect(events.mock.calls.map((call) => call[1])).toEqual([undefined, undefined, 'cursor1']);
  });
});
