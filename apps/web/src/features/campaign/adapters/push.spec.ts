import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { CampaignPushListener, CampaignSession } from '../model/ports';
import { createCampaignPushWith, type PushSocket } from './push';

type Handler = (...args: unknown[]) => void;
class FakeSocket {
  connected = false;
  active = true;
  handlers = new Map<string, Handler[]>();
  managerHandlers = new Map<string, Handler[]>();
  emits: { event: string; payload: unknown; ack?: Handler }[] = [];
  disconnected = 0;
  connects = 0;
  constructor(public auth: { token: string } | null) {}
  on(event: string, fn: Handler) { this.handlers.set(event, [...this.handlers.get(event) ?? [], fn]); return this; }
  emit(event: string, payload: unknown, ack?: Handler) { this.emits.push({ event, payload, ack }); return this; }
  timeout() { return { emit: (event: string, payload: unknown, ack: Handler) => this.emit(event, payload, ack) }; }
  connect() { this.connects++; return this; }
  disconnect() { this.disconnected++; this.connected = false; return this; }
  removeAllListeners() { this.handlers.clear(); this.managerHandlers.clear(); return this; }
  io = { on: (event: string, fn: Handler) => { this.managerHandlers.set(event, [...this.managerHandlers.get(event) ?? [], fn]); } };
  fire(event: string, ...args: unknown[]) { for (const fn of this.handlers.get(event) ?? []) fn(...args); }
  open() { this.connected = true; this.fire('connect'); }
  subscribes() { return this.emits.filter((item) => item.event === 'campaign:subscribe'); }
  unsubscribes() { return this.emits.filter((item) => item.event === 'campaign:unsubscribe').map((item) => item.payload); }
  ackAll(result: (payload: { campaignId: string; family: string }) => unknown = (p) => ({ success: true, ...p })) {
    for (const item of this.subscribes().splice(0)) item.ack?.(null, result(item.payload as { campaignId: string; family: string }));
    this.emits = this.emits.filter((item) => item.event !== 'campaign:subscribe');
  }
}

const sessionA: CampaignSession = { owner: `0x${'1'.repeat(40)}`, generation: 1, token: 'token-a' };
const sessionB: CampaignSession = { owner: `0x${'2'.repeat(40)}`, generation: 2, token: 'token-b' };
const pub = { campaignId: 'c1', family: 'publication' } as const;
const mine = { campaignId: 'c1', family: 'personal' } as const;
const listener = () => ({ invalidate: vi.fn<(head: bigint) => void>(), subscribed: vi.fn(), reset: vi.fn() }) satisfies CampaignPushListener;
const invalidation = (head: string, key: { campaignId: string; family: string } = pub) => ({ schemaVersion: 1, ...key, headVersion: head });

let sockets: FakeSocket[];
const harness = (overrides = {}) => createCampaignPushWith({ connect: (auth) => { const socket = new FakeSocket(auth); sockets.push(socket); return socket as unknown as PushSocket; }, ...overrides });

beforeEach(() => { sockets = []; vi.useFakeTimers(); });
afterEach(() => { vi.useRealTimers(); });

describe('campaign push adapter', () => {
  it('subscribes a key once per socket and unsubscribes with the complete key on the last watcher', () => {
    const push = harness();
    const one = listener(), two = listener();
    const stopOne = push.watch(pub, null, one);
    const stopTwo = push.watch(pub, null, two);
    expect(sockets).toHaveLength(1);
    expect(sockets[0].auth).toBeNull();
    sockets[0].open();
    expect(sockets[0].subscribes().map((item) => item.payload)).toEqual([{ campaignId: 'c1', family: 'publication' }]);
    sockets[0].ackAll();
    expect(one.subscribed).toHaveBeenCalledTimes(1);
    expect(two.subscribed).toHaveBeenCalledTimes(1);
    stopOne();
    stopOne();
    expect(sockets[0].unsubscribes()).toEqual([]);
    stopTwo();
    expect(sockets[0].unsubscribes()).toEqual([{ campaignId: 'c1', family: 'publication' }]);
    expect(sockets[0].disconnected).toBe(0);
    vi.advanceTimersByTime(1_000);
    expect(sockets[0].disconnected).toBe(1);
  });

  it('reconnect re-sends only the current key set without changing reference counts', () => {
    const push = harness();
    const a = listener();
    const stopA = push.watch(pub, null, a);
    const stopA2 = push.watch(pub, null, a);
    const stopB = push.watch({ campaignId: 'c1', family: 'interaction' }, null, listener());
    const socket = sockets[0];
    socket.open();
    socket.ackAll();
    stopB();
    socket.fire('disconnect', 'transport close');
    socket.open();
    expect(socket.subscribes().map((item) => item.payload)).toEqual([{ campaignId: 'c1', family: 'publication' }]);
    socket.ackAll();
    expect(a.subscribed).toHaveBeenCalledTimes(4); // two watches × (initial + reconnect)
    stopA();
    expect(socket.unsubscribes()).toEqual([{ campaignId: 'c1', family: 'interaction' }]);
    stopA2();
    expect(socket.unsubscribes()).toEqual([{ campaignId: 'c1', family: 'interaction' }, { campaignId: 'c1', family: 'publication' }]);
  });

  it('delivers increasing heads once, acknowledges frames and drops malformed or foreign ones', () => {
    const push = harness();
    const watcher = listener();
    push.watch(pub, null, watcher);
    const socket = sockets[0];
    socket.open();
    const early = vi.fn();
    socket.fire('campaign:invalidate', invalidation('5'), early);
    expect(early).toHaveBeenCalledTimes(1);
    expect(watcher.invalidate).not.toHaveBeenCalled(); // not subscribed yet
    socket.ackAll();
    const ack = vi.fn();
    socket.fire('campaign:invalidate', invalidation('5'), ack);
    socket.fire('campaign:invalidate', invalidation('5'), ack);
    socket.fire('campaign:invalidate', invalidation('4'), ack);
    for (const bad of [{ ...invalidation('6'), owner: 'x' }, { ...invalidation('6'), schemaVersion: 2 }, { ...invalidation('6'), headVersion: '06' },
      invalidation('6', { campaignId: 'c2', family: 'publication' }), invalidation('6', mine), 'c1', null]) socket.fire('campaign:invalidate', bad, ack);
    socket.fire('campaign:invalidate', invalidation('7'), ack);
    expect(ack).toHaveBeenCalledTimes(11);
    expect(watcher.invalidate.mock.calls.map(([head]) => head)).toEqual([5n, 7n]);
  });

  it('keeps identities on separate sockets, sends credentials only in CONNECT auth and handles A→B→A', () => {
    const push = harness();
    expect(push.watch(mine, null, listener())).toBeTypeOf('function');
    expect(sockets).toHaveLength(0);
    const stopA = push.watch(mine, sessionA, listener());
    const stopPublic = push.watch(pub, sessionA, listener());
    expect(sockets.map((s) => s.auth)).toEqual([{ token: 'token-a' }, null]);
    sockets[0].open();
    expect(JSON.stringify(sockets[0].emits)).not.toContain('token-a');
    stopA();
    const stopB = push.watch(mine, sessionB, listener());
    expect(sockets[2].auth).toEqual({ token: 'token-b' });
    stopB();
    const lateA = push.watch(mine, sessionA, listener());
    expect(sockets).toHaveLength(3); // A's socket was still lingering and is reused
    lateA();
    vi.advanceTimersByTime(1_000);
    expect(sockets[0].disconnected).toBe(1);
    expect(sockets[2].disconnected).toBe(1);
    push.watch(mine, sessionA, listener());
    expect(sockets).toHaveLength(4);
    stopPublic();
  });

  it('never calls a disposed watcher, even for an acknowledgement already in flight', () => {
    const push = harness();
    const watcher = listener();
    const stop = push.watch(pub, null, watcher);
    sockets[0].open();
    const pending = sockets[0].subscribes()[0];
    stop();
    pending.ack?.(null, { success: true, ...pub });
    sockets[0].fire('campaign:invalidate', invalidation('9'));
    sockets[0].fire('campaign:reset', { schemaVersion: 1, ...pub, reason: 'not_found' });
    expect(watcher.subscribed).not.toHaveBeenCalled();
    expect(watcher.invalidate).not.toHaveBeenCalled();
    expect(watcher.reset).not.toHaveBeenCalled();
    // A disposed watcher stays silent while another watcher keeps the key alive.
    const kept = listener(), gone = listener();
    push.watch(pub, null, kept);
    const stopGone = push.watch(pub, null, gone);
    sockets[0].subscribes().at(-1)?.ack?.(null, { success: true, ...pub });
    stopGone();
    sockets[0].fire('campaign:invalidate', invalidation('10'));
    expect(kept.invalidate).toHaveBeenCalledWith(10n);
    expect(gone.invalidate).not.toHaveBeenCalled();
  });

  it('ignores the cancelled acknowledgement of a superseded subscribe and keeps the replacement', () => {
    const push = harness();
    const first = listener();
    const stop = push.watch(pub, null, first);
    sockets[0].open();
    const superseded = sockets[0].subscribes()[0];
    stop();
    expect(sockets[0].unsubscribes()).toEqual([{ campaignId: 'c1', family: 'publication' }]);
    const replacement = listener();
    push.watch(pub, null, replacement);
    const current = sockets[0].subscribes().at(-1);
    expect(current).not.toBe(superseded);
    superseded.ack?.(null, { success: false, code: 'cancelled', message: 'Subscription was cancelled by a later request' });
    expect(replacement.reset).not.toHaveBeenCalled();
    expect(sockets[0].subscribes()).toHaveLength(2);
    current?.ack?.(null, { success: true, ...pub });
    expect(replacement.subscribed).toHaveBeenCalledTimes(1);
    sockets[0].fire('campaign:invalidate', invalidation('3'));
    expect(replacement.invalidate).toHaveBeenCalledWith(3n);
    expect(first.subscribed).not.toHaveBeenCalled();
    expect(first.reset).not.toHaveBeenCalled();
  });

  it('stops an identity on revocation and bounds reconnects with a tombstone', () => {
    const push = harness();
    const watcher = listener();
    push.watch(mine, sessionA, watcher);
    sockets[0].open();
    sockets[0].ackAll();
    sockets[0].fire('campaign:reset', { schemaVersion: 1, ...mine, reason: 'unauthorized' });
    expect(watcher.reset).toHaveBeenCalledWith('unauthorized');
    expect(sockets[0].disconnected).toBe(1);
    expect(push.watch(mine, sessionA, listener())).toBeTypeOf('function');
    expect(sockets).toHaveLength(1);
    vi.advanceTimersByTime(60_000);
    push.watch(mine, sessionA, listener());
    expect(sockets).toHaveLength(2);
    sockets[1].active = false;
    const next = listener();
    push.watch({ campaignId: 'c2', family: 'personal' }, sessionA, next);
    sockets[1].fire('connect_error', { message: 'unauthorized', data: { code: 'unauthorized' } });
    expect(next.reset).toHaveBeenCalledWith('unauthorized');
    sockets[1].fire('disconnect', 'io server disconnect');
    expect(sockets).toHaveLength(2);
  });

  it('retries unavailable session checks with bounded backoff, then falls back to polling', () => {
    const push = harness();
    const watcher = listener();
    push.watch(mine, sessionA, watcher);
    const socket = sockets[0];
    socket.active = false;
    for (let attempt = 0; attempt < 5; attempt++) {
      socket.fire('connect_error', { data: { code: 'session_unavailable' } });
      vi.advanceTimersByTime(2_000 * 2 ** attempt);
    }
    expect(socket.connects).toBe(5);
    socket.fire('connect_error', { data: { code: 'session_unavailable' } });
    expect(watcher.reset).toHaveBeenCalledWith('session_unavailable');
    expect(socket.disconnected).toBe(1);
  });

  it('maps acknowledgement failures to stop, bounded retry or revocation', () => {
    const push = harness();
    const hidden = listener(), busy = listener();
    push.watch({ campaignId: 'gone', family: 'publication' }, null, hidden);
    push.watch(pub, null, busy);
    const socket = sockets[0];
    socket.open();
    socket.ackAll((p) => p.campaignId === 'gone' ? { success: false, code: 'not_found', message: 'x' } : { success: false, code: 'unavailable', message: 'x' });
    expect(hidden.reset).toHaveBeenCalledWith('not_found');
    expect(busy.reset).toHaveBeenCalledWith('unavailable');
    for (let attempt = 0; attempt < 6; attempt++) {
      vi.advanceTimersByTime(2_000 * 2 ** attempt);
      socket.ackAll(() => ({ success: false, code: 'unavailable', message: 'x' }));
    }
    expect(busy.reset).toHaveBeenCalledTimes(6); // first failure + five bounded retries
    expect(vi.getTimerCount()).toBe(0);
    socket.ackAll();
    const timedOut = listener();
    push.watch({ campaignId: 'c3', family: 'publication' }, null, timedOut);
    socket.subscribes()[0].ack?.(new Error('timeout'));
    expect(timedOut.reset).toHaveBeenCalledWith('unavailable');
  });

  it('limits keys per socket and rejects malformed keys before connecting', () => {
    const push = harness();
    expect(push.watch({ campaignId: 'bad id', family: 'publication' }, null, listener())).toBeTypeOf('function');
    expect(push.watch({ campaignId: 'c1', family: 'drafts' as 'publication' }, null, listener())).toBeTypeOf('function');
    expect(sockets).toHaveLength(0);
    for (let i = 0; i < 17; i++) push.watch({ campaignId: `c${i}`, family: 'publication' }, null, listener());
    sockets[0].open();
    expect(sockets[0].subscribes()).toHaveLength(16);
  });

  it('treats an unmounted namespace as polling-only without reconnect loops', () => {
    const push = harness();
    const watcher = listener();
    push.watch(pub, null, watcher);
    sockets[0].active = false;
    sockets[0].fire('connect_error', { message: 'Invalid namespace' });
    expect(watcher.reset).toHaveBeenCalledWith('unavailable');
    push.watch(pub, null, listener());
    expect(sockets).toHaveLength(1);
    expect(vi.getTimerCount()).toBe(0);
  });
});
