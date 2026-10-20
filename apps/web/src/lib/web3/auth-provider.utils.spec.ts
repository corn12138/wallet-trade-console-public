import { afterEach, describe, expect, it, vi } from 'vitest';
import { revokeServerSession, SERVER_SIGN_OUT_TIMEOUT_MS } from './auth-provider.utils';

function respond(status: number, body?: unknown) {
  return vi.fn(async () => new Response(body === undefined ? null : JSON.stringify(body), { status }));
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

describe('revokeServerSession', () => {
  it('posts the bearer to the web3 logout route without cookies or caching', async () => {
    const fetcher = respond(200, { revoked: true, alreadyRevoked: false });
    vi.stubGlobal('fetch', fetcher);
    await expect(revokeServerSession('token-a')).resolves.toBe('revoked');
    const [url, init] = fetcher.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toMatch(/\/api\/web3-auth\/logout$/);
    expect(init.method).toBe('POST');
    expect(init.credentials).toBe('omit');
    expect(init.cache).toBe('no-store');
    expect(init.keepalive).toBe(true);
    expect(new Headers(init.headers).get('Authorization')).toBe('Bearer token-a');
  });

  it.each([
    ['already revoked', 200, { revoked: true, alreadyRevoked: true }, 'already-revoked'],
    ['token no longer accepted', 401, { statusCode: 401 }, 'rejected'],
    ['store unavailable', 503, { statusCode: 503 }, 'unconfirmed'],
    ['gateway page', 502, undefined, 'unconfirmed'],
    ['unexpected body', 200, { ok: true }, 'unconfirmed'],
    ['not revoked', 200, { revoked: false, alreadyRevoked: false }, 'unconfirmed'],
  ])('%s → %s', async (_name, status, body, expected) => {
    vi.stubGlobal('fetch', respond(status as number, body));
    await expect(revokeServerSession('token-a')).resolves.toBe(expected);
  });

  it('treats a network failure as unconfirmed, never as signed out on the server', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => { throw new TypeError('offline'); }));
    await expect(revokeServerSession('token-a')).resolves.toBe('unconfirmed');
  });

  it('gives up after its timeout as unconfirmed', async () => {
    vi.useFakeTimers();
    vi.stubGlobal('fetch', vi.fn((_url: string, init: RequestInit) => new Promise((_resolve, reject) => {
      init.signal?.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')));
    })));
    const outcome = revokeServerSession('token-a');
    await vi.advanceTimersByTimeAsync(SERVER_SIGN_OUT_TIMEOUT_MS);
    await expect(outcome).resolves.toBe('unconfirmed');
  });
});
