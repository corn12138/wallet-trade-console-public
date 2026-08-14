// Local default targets the Go API (`pnpm dev:api` serves :8090), which is the
// sole backend (the NestJS service was retired 2026-07-08 —
// docs/migration/backend-go/NESTJS_RETIRED_2026-07-08.md). No :3001 default.
const DEFAULT_API_ORIGIN = 'http://127.0.0.1:8090';
const SAME_ORIGIN_API_ORIGIN = '';

function getSameOriginApiHosts(): Set<string> {
  return new Set(
    (process.env.NEXT_PUBLIC_API_SAME_ORIGIN_HOSTS ?? '')
      .split(',')
      .map((host) => host.trim().toLowerCase())
      .filter(Boolean),
  );
}

function stripTrailingSlash(value: string): string {
  return value.endsWith('/') ? value.slice(0, -1) : value;
}

function shouldUseSameOriginApiProxy(configured: string): boolean {
  if (process.env.NEXT_PUBLIC_API_FORCE_SAME_ORIGIN === 'true') {
    return true;
  }

  if (process.env.NEXT_PUBLIC_API_FORCE_SAME_ORIGIN === 'false') {
    return false;
  }

  try {
    return getSameOriginApiHosts().has(new URL(configured).hostname.toLowerCase());
  } catch {
    return false;
  }
}

export function getApiOrigin(): string {
  const configured = stripTrailingSlash(
    process.env.NEXT_PUBLIC_API_URL
      || process.env.NEXT_PUBLIC_API_ORIGIN
      || DEFAULT_API_ORIGIN
  );

  if (shouldUseSameOriginApiProxy(configured)) {
    return SAME_ORIGIN_API_ORIGIN;
  }

  return configured.endsWith('/api')
    ? configured.slice(0, -'/api'.length)
    : configured;
}

export function getApiBaseUrl(): string {
  const origin = getApiOrigin();
  return `${origin}/api`;
}

export function buildApiUrl(pathname: string): string {
  const normalizedPath = pathname.startsWith('/') ? pathname : `/${pathname}`;
  return `${getApiBaseUrl()}${normalizedPath}`;
}

const SOCKET_DISABLED_VALUES = new Set(['off', 'disabled', 'none', 'false']);

export type SocketTransport = 'websocket' | 'polling';

export interface SocketClientConfig {
  /** false = realtime explicitly blocked; consumers must fall back to REST polling. */
  enabled: boolean;
  /** Socket base origin ('' = same-origin through the Vercel/nginx rewrite). */
  origin: string;
  transports: SocketTransport[];
  /**
   * Same-origin sockets can go through a hosting-platform rewrite, where two
   * common limits apply:
   *  - WebSocket upgrades are rejected with HTTP 400 (rewrites never proxy WS),
   *    and socket.io-client does not fall back to the next transport by
   *    default, so websocket-first hangs the stream in "connecting".
   *  - Engine.IO's default trailing-slash request path (`/socket.io/?EIO=…`)
   *    trips Next.js's 308 trailing-slash redirect before the rewrite runs.
   * Polling-only without the trailing slash connects, ACKs subscriptions, and
   * receives live frames end to end.
   */
  addTrailingSlash: boolean;
  upgrade: boolean;
}

function getConfiguredSocketUrl(): string | undefined {
  const raw = process.env.NEXT_PUBLIC_SOCKET_URL?.trim();
  return raw ? raw : undefined;
}

export function getSocketClientConfig(): SocketClientConfig {
  const configured = getConfiguredSocketUrl();

  if (configured && SOCKET_DISABLED_VALUES.has(configured.toLowerCase())) {
    return {
      enabled: false,
      origin: '',
      transports: [],
      addTrailingSlash: true,
      upgrade: false,
    };
  }

  if (configured) {
    // Dedicated socket endpoint (e.g. a future wss:// gateway): independent
    // from the REST /api origin, full transport set.
    return {
      enabled: true,
      origin: stripTrailingSlash(configured),
      transports: ['websocket', 'polling'],
      addTrailingSlash: true,
      upgrade: true,
    };
  }

  const origin = getApiOrigin();
  if (origin === SAME_ORIGIN_API_ORIGIN) {
    return {
      enabled: true,
      origin,
      transports: ['polling'],
      addTrailingSlash: false,
      upgrade: false,
    };
  }

  // Direct API origin (local dev / non-proxied deployments): the backend
  // terminates the socket itself, so WebSocket works.
  return {
    enabled: true,
    origin,
    transports: ['websocket', 'polling'],
    addTrailingSlash: true,
    upgrade: true,
  };
}

export function buildSocketNamespaceUrl(namespace: string): string {
  const normalizedNamespace = namespace.startsWith('/') ? namespace : `/${namespace}`;
  return `${getSocketClientConfig().origin}${normalizedNamespace}`;
}
