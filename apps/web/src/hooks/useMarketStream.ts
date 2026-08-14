'use client';

import { useEffect, useMemo, useRef, useState } from 'react';
import { io, Socket } from 'socket.io-client';
import type {
  TradingHistoryApi,
  TradingMarketTickerApi,
  TradingOrderbookApi,
} from '@/lib/api';
import { buildSocketNamespaceUrl, getSocketClientConfig } from '@/lib/api/base-url';
import { getMarketSnapshot, type MarketSnapshotApi } from '@/lib/api/markets';

/**
 * Lifecycle:
 *   idle       — no symbol selected.
 *   connecting — initial WebSocket handshake in flight.
 *   live       — receiving WS frames.
 *   degraded   — WS disconnected, attempting silent reconnect (UI may show
 *                "reconnecting" but data may still be fresh from cache).
 *   polling    — gave up on WS, falling back to REST polling. The hook
 *                continues to deliver data to the UI; consumers should
 *                surface a stale-but-functional indicator.
 */
type MarketStreamStatus = 'idle' | 'connecting' | 'live' | 'degraded' | 'polling';

interface UseMarketStreamOptions {
  symbol?: string;
  chainId?: number;
}

interface MarketBookPayload extends TradingOrderbookApi {
  symbol: string;
  chainId?: number;
  updatedAt: string;
}

interface MarketTradesPayload {
  symbol: string;
  chainId?: number;
  trades: TradingHistoryApi[];
  updatedAt: string;
}

const MAX_WS_RECONNECT_ATTEMPTS = 5;
const POLLING_INTERVAL_MS = 5_000;
const POLLING_RETRY_BACKOFF_MS = 2_000;

function normalizeMarketSymbol(symbol?: string) {
  return symbol?.trim().toUpperCase() ?? '';
}

export function useMarketStream({ symbol, chainId }: UseMarketStreamOptions) {
  const normalizedSymbol = useMemo(() => normalizeMarketSymbol(symbol), [symbol]);
  const [status, setStatus] = useState<MarketStreamStatus>('idle');
  const [ticker, setTicker] = useState<TradingMarketTickerApi | null>(null);
  const [orderbook, setOrderbook] = useState<TradingOrderbookApi | null>(null);
  const [trades, setTrades] = useState<TradingHistoryApi[]>([]);
  const [hasLiveTicker, setHasLiveTicker] = useState(false);
  const [hasLiveOrderbook, setHasLiveOrderbook] = useState(false);
  const [hasLiveTrades, setHasLiveTrades] = useState(false);
  const [lastMessageAt, setLastMessageAt] = useState<string | null>(null);

  // Refs avoid effect re-runs when only the WS-vs-polling mode flips.
  const socketRef = useRef<Socket | null>(null);
  const pollingTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const pollingAbortRef = useRef<AbortController | null>(null);

  useEffect(() => {
    if (!normalizedSymbol) {
      setStatus('idle');
      setTicker(null);
      setOrderbook(null);
      setTrades([]);
      setHasLiveTicker(false);
      setHasLiveOrderbook(false);
      setHasLiveTrades(false);
      setLastMessageAt(null);
      return;
    }

    let cancelled = false;
    let wsAttempts = 0;

    const applySnapshot = (snapshot: MarketSnapshotApi) => {
      if (cancelled) return;
      if (snapshot.ticker) {
        setTicker(snapshot.ticker);
      }
      if (snapshot.orderbook) {
        setOrderbook(snapshot.orderbook);
      }
      if (snapshot.trades) {
        setTrades(snapshot.trades);
      }
      setLastMessageAt(snapshot.updatedAt);
    };

    const stopPolling = () => {
      if (pollingTimerRef.current) {
        clearTimeout(pollingTimerRef.current);
        pollingTimerRef.current = null;
      }
      if (pollingAbortRef.current) {
        pollingAbortRef.current.abort();
        pollingAbortRef.current = null;
      }
    };

    const startPolling = () => {
      if (cancelled) return;
      stopPolling();
      setStatus('polling');

      const tick = async () => {
        if (cancelled) return;
        const controller = new AbortController();
        pollingAbortRef.current = controller;
        try {
          const snapshot = await getMarketSnapshot(normalizedSymbol, chainId, controller.signal);
          applySnapshot(snapshot);
          if (!cancelled) {
            pollingTimerRef.current = setTimeout(tick, POLLING_INTERVAL_MS);
          }
        } catch (error) {
          // Network blip — back off and retry. Aborts during teardown are
          // expected and handled by the cancellation flag.
          if (!cancelled && (error as Error).name !== 'AbortError') {
            pollingTimerRef.current = setTimeout(tick, POLLING_RETRY_BACKOFF_MS);
          }
        }
      };

      void tick();
    };

    const teardownSocket = () => {
      const socket = socketRef.current;
      if (!socket) return;
      socket.removeAllListeners();
      socket.disconnect();
      socketRef.current = null;
    };

    const startSocket = () => {
      const socketConfig = getSocketClientConfig();
      if (!socketConfig.enabled) {
        // Realtime is explicitly blocked (NEXT_PUBLIC_SOCKET_URL=off): go
        // straight to REST polling so the UI shows an honest non-live status
        // instead of hanging in "connecting".
        startPolling();
        return;
      }

      const streamUrl = buildSocketNamespaceUrl('/markets');
      const tickerEvent = `market:ticker:${normalizedSymbol}`;
      const bookEvent = `market:book:${normalizedSymbol}`;
      const tradesEvent = `market:trades:${normalizedSymbol}`;

      // Transport selection lives in getSocketClientConfig(): same-origin
      // deployments must use polling-only (the Vercel rewrite rejects WS
      // upgrades with 400 and socket.io-client does not fall back across
      // transports), while direct/dedicated endpoints keep websocket-first.
      // We cap reconnection attempts so that a permanently broken socket path
      // gracefully drops to the REST polling fallback rather than churning.
      const socket = io(streamUrl, {
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

      const subscription = { symbol: normalizedSymbol, chainId };

      socket.on('connect', () => {
        if (cancelled) return;
        wsAttempts = 0;
        setStatus('live');
        socket.emit('subscribe:market:ticker', subscription);
        socket.emit('subscribe:market:book', subscription);
        socket.emit('subscribe:market:trades', subscription);
      });

      socket.on('disconnect', () => {
        if (cancelled) return;
        setStatus('degraded');
      });

      socket.on('connect_error', () => {
        if (cancelled) return;
        wsAttempts += 1;
      });

      socket.io.on('reconnect_failed', () => {
        if (cancelled) return;
        // socket.io exhausted reconnectionAttempts. Hand over to polling.
        teardownSocket();
        startPolling();
      });

      socket.on(tickerEvent, (payload: TradingMarketTickerApi) => {
        if (cancelled) return;
        setTicker(payload);
        setHasLiveTicker(true);
        setLastMessageAt(payload.updatedAt);
      });

      socket.on(bookEvent, (payload: MarketBookPayload) => {
        if (cancelled) return;
        setOrderbook({ bids: payload.bids, asks: payload.asks });
        setHasLiveOrderbook(true);
        setLastMessageAt(payload.updatedAt);
      });

      socket.on(tradesEvent, (payload: MarketTradesPayload) => {
        if (cancelled) return;
        setTrades(payload.trades);
        setHasLiveTrades(true);
        setLastMessageAt(payload.updatedAt);
      });
    };

    setTicker(null);
    setOrderbook(null);
    setTrades([]);
    setHasLiveTicker(false);
    setHasLiveOrderbook(false);
    setHasLiveTrades(false);
    setLastMessageAt(null);

    startSocket();

    return () => {
      cancelled = true;
      const socket = socketRef.current;
      if (socket) {
        if (socket.connected) {
          const subscription = { symbol: normalizedSymbol, chainId };
          socket.emit('unsubscribe:market:ticker', subscription);
          socket.emit('unsubscribe:market:book', subscription);
          socket.emit('unsubscribe:market:trades', subscription);
        }
        teardownSocket();
      }
      stopPolling();
    };
  }, [chainId, normalizedSymbol]);

  return {
    status,
    ticker,
    orderbook,
    trades,
    hasLiveTicker,
    hasLiveOrderbook,
    hasLiveTrades,
    lastMessageAt,
    isConnected: status === 'live',
    isDegraded: status === 'degraded' || status === 'polling',
  };
}

export type { MarketStreamStatus };
