import { io } from 'socket.io-client';
import { buildSocketNamespaceUrl, getSocketClientConfig } from '@/lib/api/base-url';
import { decodeCampaignContract } from '../model/decode-revision';
import type { CampaignPush, CampaignPushKey, CampaignPushListener, CampaignPushReset, CampaignSession } from '../model/ports';

/** The subset of a socket.io-client Socket this adapter drives. */
export type PushSocket = {
  readonly connected: boolean;
  readonly active: boolean;
  on(event: string, listener: (...args: never[]) => void): unknown;
  emit(event: string, ...args: unknown[]): unknown;
  timeout(ms: number): { emit(event: string, ...args: unknown[]): unknown };
  connect(): unknown;
  disconnect(): unknown;
  removeAllListeners(): unknown;
  readonly io: { on(event: string, listener: (...args: never[]) => void): unknown };
};
export type CampaignPushOptions = {
  connect(auth: { token: string } | null): PushSocket;
  lingerMs?: number;
  retryMs?: number;
  ackTimeoutMs?: number;
  tombstoneMs?: number;
};

const MAX_KEYS = 16;
const MAX_ATTEMPTS = 5;
type Watch = { listener: CampaignPushListener; live: boolean };
type Entry = { key: CampaignPushKey; watches: Set<Watch>; head: bigint; state: 'pending' | 'subscribed' | 'stopped'; attempts: number; timer: ReturnType<typeof setTimeout> | null; turn: number };
type Channel = { socket: PushSocket | null; entries: Map<string, Entry>; linger: ReturnType<typeof setTimeout> | null; retry: ReturnType<typeof setTimeout> | null; dead: number; attempts: number; personal: boolean };

const keyId = (key: CampaignPushKey) => `${key.family}:${key.campaignId}`;
const each = (entry: Entry, call: (listener: CampaignPushListener) => void) => {
  for (const watch of [...entry.watches]) if (watch.live) call(watch.listener);
};

/** One socket per identity: anonymous for public families, one per session
 * generation for personal keys. Reference counts belong to component watches;
 * a reconnect re-sends only the current key set. Nothing here marks data fresh. */
export function createCampaignPushWith(options: CampaignPushOptions): CampaignPush {
  const linger = options.lingerMs ?? 1_000;
  const retry = options.retryMs ?? 2_000;
  const ackTimeout = options.ackTimeoutMs ?? 5_000;
  const tombstone = options.tombstoneMs ?? 60_000;
  const channels = new Map<string, Channel>();

  const close = (identity: string, channel: Channel) => {
    if (channel.linger) clearTimeout(channel.linger);
    if (channel.retry) clearTimeout(channel.retry);
    for (const entry of channel.entries.values()) if (entry.timer) clearTimeout(entry.timer);
    channel.socket?.removeAllListeners();
    channel.socket?.disconnect();
    channel.socket = null;
    if (!channel.dead && channels.get(identity) === channel) channels.delete(identity);
  };
  // A dead channel stops pushing for this identity; its watchers fall back to
  // polling. The tombstone bounds reconnect attempts across remounts.
  const kill = (identity: string, channel: Channel, reason: CampaignPushReset) => {
    channel.dead = Date.now();
    const entries = [...channel.entries.values()];
    close(identity, channel);
    for (const entry of entries) { entry.state = 'stopped'; each(entry, (listener) => listener.reset(reason)); }
  };

  const subscribe = (identity: string, channel: Channel, entry: Entry) => {
    const socket = channel.socket;
    if (!socket?.connected || channel.entries.get(keyId(entry.key)) !== entry) return;
    entry.state = 'pending';
    const turn = ++entry.turn;
    socket.timeout(ackTimeout).emit('campaign:subscribe', { campaignId: entry.key.campaignId, family: entry.key.family }, (error: unknown, raw: unknown) => {
      if (channel.entries.get(keyId(entry.key)) !== entry || entry.turn !== turn || channel.socket !== socket) return;
      const ack = error ? null : decodeCampaignContract('PushAck', raw);
      if (ack?.supported && ack.value.success && ack.value.campaignId === entry.key.campaignId && ack.value.family === entry.key.family) {
        entry.state = 'subscribed';
        entry.attempts = 0;
        // The server baseline precedes this acknowledgement; the caller's
        // catch-up after it closes the bootstrap window.
        each(entry, (listener) => listener.subscribed());
        return;
      }
      const code = ack?.supported && !ack.value.success ? ack.value.code : undefined;
      if (code === 'unauthorized') return kill(identity, channel, 'unauthorized');
      if (code === 'invalid_request' || code === 'not_found' || code === 'too_many_subscriptions') {
        entry.state = 'stopped';
        each(entry, (listener) => listener.reset(code === 'not_found' ? 'not_found' : 'unavailable'));
        return;
      }
      backoff(identity, channel, entry, code === 'session_unavailable' ? 'session_unavailable' : 'unavailable');
    });
  };

  const backoff = (identity: string, channel: Channel, entry: Entry, reason: CampaignPushReset) => {
    entry.state = 'stopped';
    each(entry, (listener) => listener.reset(reason));
    if (entry.attempts >= MAX_ATTEMPTS || entry.timer) return;
    const delay = retry * 2 ** entry.attempts++;
    entry.timer = setTimeout(() => { entry.timer = null; subscribe(identity, channel, entry); }, delay);
  };

  const open = (identity: string, session: CampaignSession | null): Channel | null => {
    const existing = channels.get(identity);
    if (existing && existing.dead && Date.now() - existing.dead < tombstone) return null;
    if (existing && !existing.dead) return existing;
    const channel: Channel = { socket: null, entries: new Map(), linger: null, retry: null, dead: 0, attempts: 0, personal: session !== null };
    channels.set(identity, channel);
    const socket = options.connect(session ? { token: session.token } : null);
    channel.socket = socket;
    socket.on('connect', () => {
      channel.attempts = 0;
      for (const entry of channel.entries.values()) {
        if (entry.timer) { clearTimeout(entry.timer); entry.timer = null; }
        entry.attempts = 0;
        subscribe(identity, channel, entry);
      }
    });
    socket.on('disconnect', (reason: string) => {
      for (const entry of channel.entries.values()) entry.state = 'pending';
      // Only a revoked credential makes the server end a Campaign connection.
      if (reason === 'io server disconnect') kill(identity, channel, channel.personal ? 'unauthorized' : 'unavailable');
    });
    socket.on('connect_error', (error: { data?: { code?: unknown } }) => {
      if (socket.active) return; // transport failure: the manager retries with its own bound
      const code = error?.data?.code;
      if (code === 'unauthorized' || code === 'invalid_auth') return kill(identity, channel, 'unauthorized');
      if ((code === 'session_unavailable' || code === 'unavailable') && channel.attempts < MAX_ATTEMPTS && !channel.retry) {
        const delay = retry * 2 ** channel.attempts++;
        channel.retry = setTimeout(() => { channel.retry = null; if (channel.socket === socket) socket.connect(); }, delay);
        return;
      }
      if (!channel.retry) kill(identity, channel, code === 'session_unavailable' ? 'session_unavailable' : 'unavailable');
    });
    socket.io.on('reconnect_failed', () => kill(identity, channel, 'unavailable'));
    socket.on('campaign:invalidate', (raw: unknown, ack?: unknown) => {
      // Flow control only: the server may send this key's next hint.
      if (typeof ack === 'function') (ack as () => void)();
      const frame = decodeCampaignContract('PushInvalidation', raw);
      if (!frame.supported) return;
      const entry = channel.entries.get(keyId(frame.value));
      if (!entry || entry.state !== 'subscribed') return;
      const head = BigInt(frame.value.headVersion);
      if (head <= entry.head) return;
      entry.head = head;
      each(entry, (listener) => listener.invalidate(head));
    });
    socket.on('campaign:reset', (raw: unknown) => {
      const frame = decodeCampaignContract('PushReset', raw);
      if (!frame.supported) return;
      const entry = channel.entries.get(keyId(frame.value));
      if (!entry) return;
      if (frame.value.reason === 'unauthorized') return kill(identity, channel, 'unauthorized');
      if (frame.value.reason === 'not_found') {
        entry.state = 'stopped';
        each(entry, (listener) => listener.reset('not_found'));
        return;
      }
      backoff(identity, channel, entry, 'session_unavailable');
    });
    return channel;
  };

  return {
    watch(key, session, listener) {
      const decoded = decodeCampaignContract('PushSubscriptionInput', key);
      if (!decoded.supported) return () => undefined;
      const personal = key.family === 'personal';
      if (personal && (!session?.token || /[\r\n\0]/.test(session.token))) return () => undefined;
      const identity = personal ? `session\0${session!.owner}\0${session!.generation}\0${session!.token}` : 'public';
      const channel = open(identity, personal ? session : null);
      if (!channel) return () => undefined;
      if (channel.linger) { clearTimeout(channel.linger); channel.linger = null; }
      const id = keyId(key);
      const watch: Watch = { listener, live: true };
      let entry = channel.entries.get(id);
      if (!entry) {
        if (channel.entries.size >= MAX_KEYS) return () => undefined;
        entry = { key: { campaignId: key.campaignId, family: key.family }, watches: new Set([watch]), head: -1n, state: 'pending', attempts: 0, timer: null, turn: 0 };
        channel.entries.set(id, entry);
        subscribe(identity, channel, entry);
      } else {
        entry.watches.add(watch);
        // A new watcher still needs its own catch-up after a baseline exists.
        if (entry.state === 'subscribed') queueMicrotask(() => { if (watch.live) listener.subscribed(); });
      }
      const owned = entry;
      return () => {
        if (!watch.live) return;
        watch.live = false;
        owned.watches.delete(watch);
        if (owned.watches.size || channel.entries.get(id) !== owned) return;
        channel.entries.delete(id);
        owned.turn++;
        if (owned.timer) clearTimeout(owned.timer);
        if (channel.socket?.connected) channel.socket.emit('campaign:unsubscribe', { campaignId: owned.key.campaignId, family: owned.key.family });
        if (!channel.entries.size && !channel.dead) {
          channel.linger = setTimeout(() => { channel.linger = null; if (!channel.entries.size) close(identity, channel); }, linger);
        }
      };
    },
  };
}

/** Production adapter on the repository's Socket.IO origin and transport
 * policy; undefined (polling only) when realtime sockets are disabled. */
export function createCampaignPush(): CampaignPush | undefined {
  if (typeof window === 'undefined') return undefined;
  const config = getSocketClientConfig();
  if (!config.enabled) return undefined;
  return createCampaignPushWith({
    connect: (auth) => io(buildSocketNamespaceUrl('/campaign-events'), {
      path: '/socket.io', transports: config.transports, addTrailingSlash: config.addTrailingSlash, upgrade: config.upgrade,
      // Each identity needs its own connection so a token never rides on a
      // multiplexed anonymous one; credentials only travel in the CONNECT auth.
      forceNew: true, ...(auth ? { auth } : {}),
      reconnection: true, reconnectionAttempts: MAX_ATTEMPTS, reconnectionDelay: 500, reconnectionDelayMax: 4_000, timeout: 5_000,
    }) as unknown as PushSocket,
  });
}

/** Watches several keys as one unit for a read scope. */
export function watchCampaignKeys(push: CampaignPush, keys: readonly CampaignPushKey[], session: CampaignSession | null, listener: CampaignPushListener): () => void {
  const stops = keys.map((key) => push.watch(key, session, listener));
  return () => { for (const stop of stops) stop(); };
}
