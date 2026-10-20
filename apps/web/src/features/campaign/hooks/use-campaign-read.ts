'use client';

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { CampaignAPIError, type CampaignPushListener } from '../model/ports';
import { timestamp, type ReadMode, type ReadRecovery, type ResponseClock, type SnapshotRead } from '../model/read-state';

type Options<Wire extends ResponseClock, View> = {
  key: string | null;
  identity: object;
  token?: string;
  privateData?: boolean;
  read: SnapshotRead<Wire>;
  recover?: (read: SnapshotRead<Wire>) => ReadRecovery<Wire>;
  // Invalidation hints for this scope. They only request the same bounded
  // read as polling; freshness still comes from the REST snapshot.
  push?: (listener: CampaignPushListener) => () => void;
  merge: (previous: View | null, next: Wire, mode: ReadMode) => View;
  cursor?: (view: View) => string | null;
  transition?: (wire: Wire, view: View) => string | null;
  covered?: (wire: Wire, view: View) => string[];
};
type State<View> = {
  scope: object;
  data: View | null;
  status: 'idle' | 'loading' | 'ready' | 'error';
  error: CampaignAPIError | null;
  isRefreshing: boolean;
  isLoadingMore: boolean;
  serverNow: string | null;
  clockOffsetMs: number | null;
  active: boolean;
  freshIds: string[];
};
const activePage = () => document.visibilityState !== 'hidden' && navigator.onLine !== false;
const empty = <View,>(scope: object, enabled: boolean): State<View> => ({ scope, data: null, status: enabled ? 'loading' : 'idle', error: null, isRefreshing: false, isLoadingMore: false, serverNow: null, clockOffsetMs: null, active: false, freshIds: [] });

export function useCampaignRead<Wire extends ResponseClock, View>(options: Options<Wire, View>) {
  const scope = useMemo(() => ({ key: options.key, source: options.identity, credential: options.token }), [options.key, options.identity, options.token]);
  const [state, setState] = useState(() => empty<View>(scope, options.key !== null));
  const control = useRef<{ scope: object; run: (mode: ReadMode) => void } | null>(null);

  useEffect(() => {
    const timing = new WeakMap<AbortSignal, { elapsed: number; received: number }>();
    const snapshot: SnapshotRead<Wire> = async (signal, cursor) => {
      const started = performance.now();
      const wire = await options.read(signal, cursor);
      timing.set(signal, { elapsed: performance.now() - started, received: Date.now() });
      return wire;
    };
    const recovery = options.recover?.(snapshot);
    const read = recovery?.read ?? snapshot;
    let current = empty<View>(scope, options.key !== null);
    let stopped = false;
    let flight: AbortController | null = null;
    let queued: ReadMode | null = null;
    let timer: ReturnType<typeof setTimeout> | null = null;
    let transitionAt: number | null = null;
    let repairedTransition: number | null = null;
    const live = () => !stopped;
    const commit = (next: State<View>) => { current = next; if (live()) setState(next); };
    const clearTimer = () => { if (timer !== null) clearTimeout(timer); timer = null; };
    const schedule = () => {
      clearTimer();
      if (!live() || !activePage() || options.key === null || current.error?.status === 401) return;
      const untilTransition = transitionAt === null ? 15_000 : transitionAt - Date.now();
      const delay = untilTransition > 0 ? Math.min(15_000, untilTransition) : 15_000;
      timer = setTimeout(() => run('poll'), Math.max(50, delay));
    };
    const run = (mode: ReadMode) => {
      if (!live() || options.key === null || !activePage()) return;
      if (mode === 'more' && current.status !== 'ready') return;
      if (flight) {
        if (mode !== 'more' && queued !== 'replace') queued = mode;
        return;
      }
      const cursor = mode === 'more' && current.data ? options.cursor?.(current.data) : undefined;
      if (mode === 'more' && !cursor) return;
      clearTimer();
      const request = new AbortController();
      flight = request;
      commit({ ...current, active: true, freshIds: [], status: current.data ? current.status : 'loading', isRefreshing: mode !== 'more', isLoadingMore: mode === 'more' });
      void Promise.resolve().then(() => {
        if (!live() || request.signal.aborted) throw new CampaignAPIError('ABORTED', 0);
        return read(request.signal, cursor ?? undefined);
      }).then((wire) => {
        if (!live() || flight !== request || request.signal.aborted) return;
        const server = timestamp(wire.serverNow);
        timestamp(wire.asOf);
        if (current.serverNow !== null && server < timestamp(current.serverNow)) return;
        // Feed catch-up can surround a snapshot with unrelated network delay.
        // Only the final REST round trip calibrates its server clock sample.
        const sample = timing.get(request.signal)!;
        const offset = server + sample.elapsed / 2 - sample.received;
        const data = options.merge(current.data, wire, mode);
        const transition = options.transition?.(wire, data);
        const boundary = transition ? timestamp(transition) : null;
        transitionAt = boundary !== null && boundary > server ? boundary - offset : null;
        const crossed = transitionAt !== null && transitionAt <= Date.now();
        // Time-only phase changes need no event. Slow catch-up must not reopen
        // admission using a snapshot whose boundary passed while it was waiting.
        // Repair each boundary once so a stale server cannot create a tight loop.
        if (crossed && repairedTransition !== boundary) {
          repairedTransition = boundary;
          if (queued !== 'replace') queued = 'poll';
        }
        commit({ ...current, data, freshIds: crossed ? [] : options.covered?.(wire, data) ?? [], status: 'ready', error: null, serverNow: wire.serverNow, clockOffsetMs: offset });
      }).catch((error: unknown) => {
        if (!live() || flight !== request || request.signal.aborted) return;
        commit({ ...current, freshIds: [], status: 'error', error: error instanceof CampaignAPIError ? error : new CampaignAPIError('NETWORK_ERROR', 0) });
      }).finally(() => {
        if (!live() || flight !== request) return;
        flight = null;
        commit({ ...current, isRefreshing: false, isLoadingMore: false });
        if (queued) { const next = queued; queued = null; run(next); }
        else schedule();
      });
    };
    let unwatch: (() => void) | null = null;
    const repair = () => { if (live()) run('poll'); };
    const watch = () => {
      if (unwatch || !options.push || !live() || options.key === null || !activePage()) return;
      unwatch = options.push({ invalidate: repair, subscribed: repair, reset: repair });
    };
    const unwatchNow = () => { const stop = unwatch; unwatch = null; stop?.(); };
    const suspend = () => {
      unwatchNow();
      clearTimer();
      queued = null;
      recovery?.reset();
      flight?.abort();
      flight = null;
      commit({ ...current, active: false, freshIds: [], status: current.status === 'loading' ? 'idle' : current.status, isRefreshing: false, isLoadingMore: false });
    };
    const sync = () => { if (activePage()) { watch(); run('poll'); } else suspend(); };
    control.current = { scope, run };
    commit(current);
    if (options.key !== null) {
      window.addEventListener('focus', sync);
      window.addEventListener('online', sync);
      window.addEventListener('offline', sync);
      document.addEventListener('visibilitychange', sync);
      sync();
    }
    return () => {
      stopped = true;
      unwatchNow();
      recovery?.reset();
      clearTimer();
      flight?.abort();
      window.removeEventListener('focus', sync);
      window.removeEventListener('online', sync);
      window.removeEventListener('offline', sync);
      document.removeEventListener('visibilitychange', sync);
      if (control.current?.scope === scope) control.current = null;
    };
    // The scope encodes every result-changing input. Keeping request callbacks
    // inside that scope prevents a late A request from observing B credentials.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [scope]);

  // A mutation may retain this callback after its owner leaves. Capturing the
  // scope keeps that old completion from refreshing the new owner's resource.
  const refresh = useCallback(() => { if (control.current?.scope === scope) control.current.run('replace'); }, [scope]);
  const loadMore = useCallback(() => { if (control.current?.scope === scope) control.current.run('more'); }, [scope]);
  // Mask synchronously in render; effect cleanup alone leaves one render where
  // a new wallet can see the previous owner's cached personal state.
  const visible = state.scope === scope ? state : empty<View>(scope, options.key !== null);
  const active = visible.active && typeof document !== 'undefined' && activePage();
  return { ...visible, active, writesReady: active && visible.status === 'ready' && !visible.isRefreshing && !visible.isLoadingMore && visible.freshIds.length > 0,
    data: options.privateData && visible.status === 'error' ? null : visible.data, refresh, loadMore };
}
