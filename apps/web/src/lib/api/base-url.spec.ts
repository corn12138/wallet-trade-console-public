import { afterEach, describe, expect, it } from 'vitest';
import {
  buildApiUrl,
  buildSocketNamespaceUrl,
  getApiBaseUrl,
  getApiOrigin,
  getSocketClientConfig,
} from './base-url';

const originalEnv = { ...process.env };

describe('API base URL helpers', () => {
  afterEach(() => {
    process.env = { ...originalEnv };
  });

  it('keeps the local API default when no public API URL is configured', () => {
    delete process.env.NEXT_PUBLIC_API_URL;
    delete process.env.NEXT_PUBLIC_API_ORIGIN;
    delete process.env.NEXT_PUBLIC_API_FORCE_SAME_ORIGIN;
    delete process.env.NEXT_PUBLIC_API_SAME_ORIGIN_HOSTS;

    expect(getApiOrigin()).toBe('http://127.0.0.1:8090');
    expect(getApiBaseUrl()).toBe('http://127.0.0.1:8090/api');
  });

  it('uses same-origin API paths when the configured host requires a proxy', () => {
    process.env.NEXT_PUBLIC_API_URL = 'https://legacy-api.example.com/api';
    process.env.NEXT_PUBLIC_API_SAME_ORIGIN_HOSTS = 'legacy-api.example.com';
    delete process.env.NEXT_PUBLIC_API_FORCE_SAME_ORIGIN;

    expect(getApiOrigin()).toBe('');
    expect(getApiBaseUrl()).toBe('/api');
    expect(buildApiUrl('/trading/markets')).toBe('/api/trading/markets');
    expect(buildSocketNamespaceUrl('/markets')).toBe('/markets');
  });

  it('allows explicitly disabling the same-origin proxy fallback', () => {
    process.env.NEXT_PUBLIC_API_URL = 'https://legacy-api.example.com/api';
    process.env.NEXT_PUBLIC_API_SAME_ORIGIN_HOSTS = 'legacy-api.example.com';
    process.env.NEXT_PUBLIC_API_FORCE_SAME_ORIGIN = 'false';

    expect(getApiOrigin()).toBe('https://legacy-api.example.com');
    expect(buildApiUrl('/trading/markets')).toBe(
      'https://legacy-api.example.com/api/trading/markets',
    );
  });
});

describe('Socket client config', () => {
  afterEach(() => {
    process.env = { ...originalEnv };
  });

  it('uses polling-only without trailing slash for same-origin deployments', () => {
    process.env.NEXT_PUBLIC_API_URL = '/api';
    delete process.env.NEXT_PUBLIC_SOCKET_URL;

    const config = getSocketClientConfig();
    expect(config.enabled).toBe(true);
    expect(config.origin).toBe('');
    expect(config.transports).toEqual(['polling']);
    expect(config.addTrailingSlash).toBe(false);
    expect(config.upgrade).toBe(false);
    expect(buildSocketNamespaceUrl('/markets')).toBe('/markets');
  });

  it('treats a configured proxy-only API host as same-origin for sockets too', () => {
    process.env.NEXT_PUBLIC_API_URL = 'https://legacy-api.example.com/api';
    process.env.NEXT_PUBLIC_API_SAME_ORIGIN_HOSTS = 'legacy-api.example.com';
    delete process.env.NEXT_PUBLIC_SOCKET_URL;

    const config = getSocketClientConfig();
    expect(config.enabled).toBe(true);
    expect(config.origin).toBe('');
    expect(config.transports).toEqual(['polling']);
  });

  it('keeps websocket-first transports for direct API origins', () => {
    delete process.env.NEXT_PUBLIC_API_URL;
    delete process.env.NEXT_PUBLIC_API_ORIGIN;
    delete process.env.NEXT_PUBLIC_SOCKET_URL;

    const config = getSocketClientConfig();
    expect(config.enabled).toBe(true);
    expect(config.origin).toBe('http://127.0.0.1:8090');
    expect(config.transports).toEqual(['websocket', 'polling']);
    expect(config.addTrailingSlash).toBe(true);
    expect(config.upgrade).toBe(true);
    expect(buildSocketNamespaceUrl('markets')).toBe('http://127.0.0.1:8090/markets');
  });

  it('honors a dedicated socket endpoint via NEXT_PUBLIC_SOCKET_URL', () => {
    process.env.NEXT_PUBLIC_API_URL = '/api';
    process.env.NEXT_PUBLIC_SOCKET_URL = 'https://stream.example.com/';

    const config = getSocketClientConfig();
    expect(config.enabled).toBe(true);
    expect(config.origin).toBe('https://stream.example.com');
    expect(config.transports).toEqual(['websocket', 'polling']);
    expect(buildSocketNamespaceUrl('/markets')).toBe('https://stream.example.com/markets');
  });

  it.each(['off', 'disabled', 'none', 'false', 'OFF'])(
    'explicitly blocks realtime when NEXT_PUBLIC_SOCKET_URL=%s',
    (value) => {
      process.env.NEXT_PUBLIC_API_URL = '/api';
      process.env.NEXT_PUBLIC_SOCKET_URL = value;

      const config = getSocketClientConfig();
      expect(config.enabled).toBe(false);
      expect(config.transports).toEqual([]);
    },
  );
});
