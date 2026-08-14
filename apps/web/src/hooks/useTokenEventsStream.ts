'use client';

import { useEffect, useRef, useState } from 'react';
import { io, Socket } from 'socket.io-client';
import { buildSocketNamespaceUrl, getSocketClientConfig } from '@/lib/api/base-url';

/**
 * Live subscription to the Go `/token-events` Socket.IO namespace for one
 * token room. The production producer is the indexer's DB sink (committed
 * Buy/Sell/Graduated projections → Postgres NOTIFY → API broadcast), so this
 * hook only claims "live" when a socket is actually connected — REST polling
 * remains the callers' fallback and must not be removed.
 *
 * Lifecycle mirrors useMarketStream: transport selection (polling-only on
 * same-origin/Vercel) comes from getSocketClientConfig(); exhausted
 * reconnects settle on 'offline' instead of spinning forever.
 */

export type TokenEventsStatus = 'idle' | 'connecting' | 'live' | 'offline';

export interface TokenEventHandlers {
  onTrade?: (trade: Record<string, unknown>) => void;
  onPriceUpdate?: (update: Record<string, unknown>) => void;
  onGraduation?: (payload: Record<string, unknown>) => void;
}

const MAX_WS_RECONNECT_ATTEMPTS = 5;

export function useTokenEventsStream(
  tokenAddress: string | undefined,
  handlers: TokenEventHandlers,
) {
  const [status, setStatus] = useState<TokenEventsStatus>('idle');
  const [lastEventAt, setLastEventAt] = useState<string | null>(null);

  // Handlers live in a ref so consumers can pass inline closures without
  // resubscribing the socket on every render.
  const handlersRef = useRef(handlers);
  handlersRef.current = handlers;
  const socketRef = useRef<Socket | null>(null);

  useEffect(() => {
    const addr = tokenAddress?.trim().toLowerCase();
    if (!addr || !/^0x[0-9a-f]{40}$/.test(addr)) {
      setStatus('idle');
      return;
    }

    const socketConfig = getSocketClientConfig();
    if (!socketConfig.enabled) {
      setStatus('offline');
      return;
    }

    let cancelled = false;
    const socket = io(buildSocketNamespaceUrl('/token-events'), {
      autoConnect: true,
      path: '/socket.io',
      transports: socketConfig.transports,
      addTrailingSlash: socketConfig.addTrailingSlash,
      upgrade: socketConfig.upgrade,
      reconnection: true,
      reconnectionAttempts: MAX_WS_RECONNECT_ATTEMPTS,
      reconnectionDelay: 500,
      reconnectionDelayMax: 4_000,
      timeout: 5_000,
    });
    socketRef.current = socket;
    setStatus('connecting');

    const markEvent = () => setLastEventAt(new Date().toISOString());

    socket.on('connect', () => {
      if (cancelled) return;
      setStatus('live');
      socket.emit('subscribe:token', addr);
    });
    socket.on('disconnect', () => {
      if (cancelled) return;
      setStatus('connecting');
    });
    socket.io.on('reconnect_failed', () => {
      if (cancelled) return;
      setStatus('offline');
    });

    socket.on('trade', (payload: Record<string, unknown>) => {
      if (cancelled) return;
      markEvent();
      handlersRef.current.onTrade?.(payload);
    });
    socket.on('price-update', (payload: Record<string, unknown>) => {
      if (cancelled) return;
      markEvent();
      handlersRef.current.onPriceUpdate?.(payload);
    });
    socket.on('graduation', (payload: Record<string, unknown>) => {
      if (cancelled) return;
      markEvent();
      handlersRef.current.onGraduation?.(payload);
    });

    return () => {
      cancelled = true;
      if (socket.connected) {
        socket.emit('unsubscribe:token', addr);
      }
      socket.removeAllListeners();
      socket.disconnect();
      socketRef.current = null;
    };
  }, [tokenAddress]);

  return {
    status,
    isLive: status === 'live',
    lastEventAt,
  };
}
