import { act, renderHook } from '@testing-library/react';
import type { ReactNode } from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { AuthProvider, useAuth } from './auth-provider';
import type { AuthVerifyResponse } from './auth.types';

const OWNER = '0x000000000000000000000000000000000000dead';
const OTHER = '0x000000000000000000000000000000000000beef';
const TOKEN_KEY = 'web3_auth_token';
const wallet = {
  address: OWNER as string | undefined,
  chainId: 11155111,
  isConnected: true,
  status: 'connected',
  connector: { id: 'injected', uid: 'wallet-1' },
};
const profile = vi.fn();
const nonce = vi.fn();
const sign = vi.fn();
const verify = vi.fn();
const revoke = vi.fn();
const disconnect = vi.fn();

vi.mock('wagmi', () => ({
  useAccount: () => wallet,
  useSignMessage: () => ({ signMessageAsync: sign }),
  useDisconnect: () => ({ disconnect }),
}));
vi.mock('./config', () => ({ supportedChains: [{ id: 11155111 }, { id: 31337 }] }));
vi.mock('./auth-provider.utils', () => ({
  readStoredToken: () => localStorage.getItem(TOKEN_KEY),
  writeStoredToken: (token: string) => localStorage.setItem(TOKEN_KEY, token),
  clearStoredToken: () => localStorage.removeItem(TOKEN_KEY),
  TOKEN_KEY: 'web3_auth_token',
  fetchAuthenticatedProfile: (...args: unknown[]) => profile(...args),
  fetchAuthNonce: (...args: unknown[]) => nonce(...args),
  buildSiweMessage: ({ address, chainId }: { address: string; chainId: number }) => `${address}:${chainId}`,
  verifySiweSignature: (...args: unknown[]) => verify(...args),
  revokeServerSession: (...args: unknown[]) => revoke(...args),
}));

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}

const EXPIRY = '2099-01-01T00:00:00.000Z';
function makeToken(label: string, address = OWNER, chainId = 11155111, expires = EXPIRY) {
  return `${btoa(JSON.stringify({ alg: 'HS256' }))}.${btoa(JSON.stringify({ type: 'web3', sub: address, chainId, exp: Date.parse(expires) / 1000 }))}.${label}`;
}
function session(label = 'current-session'): AuthVerifyResponse {
  return { token: makeToken(label), address: OWNER, chainId: 11155111, sessionExpiresAt: EXPIRY };
}

function renderAuth() {
  return renderHook(useAuth, {
    wrapper: ({ children }: { children: ReactNode }) => <AuthProvider>{children}</AuthProvider>,
  });
}

beforeEach(() => {
  vi.resetAllMocks();
  localStorage.clear();
  wallet.address = OWNER;
  wallet.chainId = 11155111;
  wallet.isConnected = true;
  wallet.status = 'connected';
  wallet.connector = { id: 'injected', uid: 'wallet-1' };
  profile.mockResolvedValue({ address: OWNER, chainId: 11155111 });
  nonce.mockResolvedValue({ nonce: 'challenge' });
  sign.mockResolvedValue('signature');
  verify.mockResolvedValue(session());
  revoke.mockResolvedValue('revoked');
});

describe('SIWE session ownership across asynchronous work', () => {
  it('restores a stored session for the connected account', async () => {
    localStorage.setItem(TOKEN_KEY, makeToken('stored-session'));
    const { result } = renderAuth();
    await act(async () => {});
    expect(result.current.isAuthenticated).toBe(true);
    expect(result.current.token).toBe(makeToken('stored-session'));
  });

  it('signs with the initiating account and connector and persists its verified session', async () => {
    const { result } = renderAuth();
    await act(async () => { await result.current.signIn(); });
    expect(sign).toHaveBeenCalledWith({ message: `${OWNER}:11155111`, account: OWNER, connector: wallet.connector });
    expect(result.current.status).toBe('authenticated');
    expect(result.current.sessionExpiresAt).toBe(session().sessionExpiresAt);
    expect(localStorage.getItem(TOKEN_KEY)).toBe(makeToken('current-session'));
  });

  it.each(['disconnect', 'sign-out', 'account', 'chain'] as const)(
    'does not restore an old session after %s', async (change) => {
      localStorage.setItem(TOKEN_KEY, makeToken('stored-session'));
      const pending = deferred<{ address: string; chainId: number }>();
      profile.mockReturnValueOnce(pending.promise).mockReturnValue(new Promise(() => {}));
      const { result, rerender } = renderAuth();
      act(() => {
        if (change === 'sign-out') result.current.signOut();
        if (change === 'disconnect') { wallet.isConnected = false; wallet.status = 'disconnected'; wallet.address = undefined; }
        if (change === 'account') wallet.address = OTHER;
        if (change === 'chain') wallet.chainId = 31337;
        rerender();
      });
      await act(async () => { pending.resolve({ address: OWNER, chainId: 11155111 }); });
      expect(result.current.isAuthenticated).toBe(false);
      expect(result.current.token).toBeNull();
    },
  );

  it.each(['resolve', 'reject'] as const)('a late restore %s cannot replace or clear a newer sign-in', async (outcome) => {
    localStorage.setItem(TOKEN_KEY, makeToken('stored-session'));
    const pending = deferred<{ address: string; chainId: number }>();
    profile.mockReturnValue(pending.promise);
    const { result } = renderAuth();
    await act(async () => { await result.current.signIn(); });
    await act(async () => {
      if (outcome === 'resolve') pending.resolve({ address: OWNER, chainId: 11155111 });
      else pending.reject(new Error('old restore failed'));
    });
    expect(result.current.token).toBe(makeToken('current-session'));
    expect(result.current.status).toBe('authenticated');
    expect(localStorage.getItem(TOKEN_KEY)).toBe(makeToken('current-session'));
  });

  it.each(['disconnect', 'sign-out', 'account', 'chain', 'connector'] as const)(
    'ignores a verification response after %s', async (change) => {
      const pending = deferred<AuthVerifyResponse>();
      verify.mockReturnValue(pending.promise);
      const { result, rerender } = renderAuth();
      let signing!: Promise<void>;
      await act(async () => { signing = result.current.signIn(); });
      expect(result.current.status).toBe('verifying');
      act(() => {
        if (change === 'sign-out') result.current.signOut();
        if (change === 'disconnect') { wallet.isConnected = false; wallet.status = 'disconnected'; wallet.address = undefined; }
        if (change === 'account') wallet.address = OTHER;
        if (change === 'chain') wallet.chainId = 31337;
        if (change === 'connector') wallet.connector = { id: 'injected', uid: 'wallet-2' };
        rerender();
      });
      await act(async () => { pending.resolve(session('obsolete-session')); await signing; });
      expect(result.current.isAuthenticated).toBe(false);
      expect(result.current.isLoading).toBe(false);
      expect(result.current.status).not.toBe('verifying');
      expect(localStorage.getItem(TOKEN_KEY)).toBeNull();
    },
  );

  it('does not open a wallet prompt for a nonce obtained before an account change', async () => {
    const pending = deferred<{ nonce: string }>();
    nonce.mockReturnValue(pending.promise);
    const { result, rerender } = renderAuth();
    let signing!: Promise<void>;
    act(() => { signing = result.current.signIn(); });
    act(() => { wallet.address = OTHER; rerender(); });
    await act(async () => { pending.resolve({ nonce: 'old-challenge' }); await signing; });
    expect(sign).not.toHaveBeenCalled();
    expect(verify).not.toHaveBeenCalled();
  });

  it('does not submit a signed challenge after the wallet chain changes', async () => {
    const pending = deferred<string>();
    sign.mockReturnValue(pending.promise);
    const { result, rerender } = renderAuth();
    let signing!: Promise<void>;
    await act(async () => { signing = result.current.signIn(); });
    act(() => { wallet.chainId = 31337; rerender(); });
    await act(async () => { pending.resolve('obsolete-signature'); await signing; });
    expect(verify).not.toHaveBeenCalled();
  });

  it.each(['resolve', 'reject'] as const)('an older sign-in %s cannot clear a newer login spinner or session', async (outcome) => {
    const oldVerify = deferred<AuthVerifyResponse>();
    const nextNonce = deferred<{ nonce: string }>();
    verify.mockReturnValueOnce(oldVerify.promise);
    const { result } = renderAuth();
    let oldSigning!: Promise<void>;
    await act(async () => { oldSigning = result.current.signIn(); });
    nonce.mockReturnValueOnce(nextNonce.promise);
    let newSigning!: Promise<void>;
    act(() => { newSigning = result.current.signIn(); });
    await act(async () => {
      if (outcome === 'resolve') oldVerify.resolve(session('obsolete-session'));
      else oldVerify.reject(new Error('obsolete rejection'));
      await oldSigning;
    });
    expect(result.current.isLoading).toBe(true);
    expect(result.current.status).toBe('signing');
    expect(result.current.error).toBeNull();
    expect(localStorage.getItem(TOKEN_KEY)).toBeNull();
    await act(async () => { nextNonce.resolve({ nonce: 'new-challenge' }); await newSigning; });
    expect(result.current.token).toBe(makeToken('current-session'));
  });

  it('does not persist verification after provider unmount', async () => {
    const pending = deferred<AuthVerifyResponse>();
    verify.mockReturnValue(pending.promise);
    const { result, unmount } = renderAuth();
    let signing!: Promise<void>;
    await act(async () => { signing = result.current.signIn(); });
    unmount();
    await act(async () => { pending.resolve(session()); await signing; });
    expect(localStorage.getItem(TOKEN_KEY)).toBeNull();
  });

  it('a restore failure after unmount cannot delete a session created elsewhere', async () => {
    localStorage.setItem(TOKEN_KEY, makeToken('stored-session'));
    const pending = deferred<{ address: string; chainId: number }>();
    profile.mockReturnValue(pending.promise);
    const { unmount } = renderAuth();
    unmount();
    localStorage.setItem(TOKEN_KEY, makeToken('new-provider-session'));
    await act(async () => { pending.reject(new Error('obsolete restore failed')); });
    expect(localStorage.getItem(TOKEN_KEY)).toBe(makeToken('new-provider-session'));
  });

  it.each(['resolve', 'reject'] as const)('an older sign-in %s cannot overwrite an already completed login', async (outcome) => {
    const pending = deferred<AuthVerifyResponse>();
    verify.mockReturnValueOnce(pending.promise);
    const { result } = renderAuth();
    let older!: Promise<void>;
    await act(async () => { older = result.current.signIn(); });
    await act(async () => { await result.current.signIn(); });
    await act(async () => {
      if (outcome === 'resolve') pending.resolve(session('obsolete-session'));
      else pending.reject(new Error('obsolete rejection'));
      await older;
    });
    expect(result.current.status).toBe('authenticated');
    expect(result.current.token).toBe(makeToken('current-session'));
    expect(result.current.error).toBeNull();
    expect(localStorage.getItem(TOKEN_KEY)).toBe(makeToken('current-session'));
  });
});

describe('connector restoration and private session boundaries', () => {
  it('retains a credential during connector restoration and restores without another SIWE prompt', async () => {
    wallet.status = 'reconnecting';
    wallet.isConnected = false;
    wallet.address = undefined;
    localStorage.setItem(TOKEN_KEY, makeToken('stored-session'));
    const { result, rerender } = renderAuth();
    expect(result.current.isRestoring).toBe(true);
    expect(result.current.connectorStatus).toBe('restoring');
    expect(result.current.token).toBeNull();
    expect(localStorage.getItem(TOKEN_KEY)).toBe(makeToken('stored-session'));
    expect(profile).not.toHaveBeenCalled();
    await act(async () => { await result.current.signIn(); });
    expect(nonce).not.toHaveBeenCalled();
    await act(async () => {
      wallet.status = 'connected'; wallet.isConnected = true; wallet.address = OWNER; rerender();
    });
    expect(result.current.isAuthenticated).toBe(true);
    expect(result.current.isLoading).toBe(false);
    expect(result.current.sessionExpiresAt).toBe(EXPIRY);
    expect(nonce).not.toHaveBeenCalled();
  });

  it('clears a credential after restoration concludes disconnected', () => {
    wallet.status = 'reconnecting'; wallet.isConnected = false; wallet.address = undefined;
    localStorage.setItem(TOKEN_KEY, makeToken('stored-session'));
    const { result, rerender } = renderAuth();
    const before = result.current.sessionGeneration;
    act(() => { wallet.status = 'disconnected'; rerender(); });
    expect(localStorage.getItem(TOKEN_KEY)).toBeNull();
    expect(result.current.isRestoring).toBe(false);
    expect(result.current.sessionGeneration).toBeGreaterThan(before);
  });

  it.each([['address', OTHER, 11155111], ['chain', OWNER, 31337]] as const)(
    'rejects a stored JWT with a different %s before requesting a profile', async (_, address, chainId) => {
      localStorage.setItem(TOKEN_KEY, makeToken('mismatch', address, chainId));
      const { result } = renderAuth();
      await act(async () => {});
      expect(result.current.isAuthenticated).toBe(false);
      expect(profile).not.toHaveBeenCalled();
      expect(localStorage.getItem(TOKEN_KEY)).toBeNull();
    },
  );

  it('rejects a restored profile whose chain disagrees with the verified session scope', async () => {
    localStorage.setItem(TOKEN_KEY, makeToken('stored-session'));
    profile.mockResolvedValue({ address: OWNER, chainId: 31337 });
    const { result } = renderAuth();
    await act(async () => {});
    expect(result.current.isAuthenticated).toBe(false);
    expect(localStorage.getItem(TOKEN_KEY)).toBeNull();
  });

  it('keeps an unverified credential after transport failure and retries it without signing', async () => {
    localStorage.setItem(TOKEN_KEY, makeToken('stored-session'));
    profile.mockRejectedValueOnce(new Error('upstream unavailable'));
    const { result } = renderAuth();
    await act(async () => {});
    expect(result.current.status).toBe('error');
    expect(result.current.token).toBeNull();
    expect(localStorage.getItem(TOKEN_KEY)).toBe(makeToken('stored-session'));
    await act(async () => { result.current.retrySession(); });
    expect(result.current.isAuthenticated).toBe(true);
    expect(nonce).not.toHaveBeenCalled();
  });

  it('rejects successful verification with a different chain without persisting it', async () => {
    verify.mockResolvedValue({ ...session(), chainId: 31337 });
    const { result } = renderAuth();
    await act(async () => { await result.current.signIn(); });
    expect(result.current.status).toBe('error');
    expect(result.current.token).toBeNull();
    expect(localStorage.getItem(TOKEN_KEY)).toBeNull();
  });

  it('advances the session generation across A to B to A and rejects the first A response', async () => {
    const old = deferred<AuthVerifyResponse>();
    verify.mockReturnValueOnce(old.promise);
    const { result, rerender } = renderAuth();
    const firstA = result.current.sessionGeneration;
    let signing!: Promise<void>;
    await act(async () => { signing = result.current.signIn(); });
    act(() => { wallet.address = OTHER; rerender(); });
    const b = result.current.sessionGeneration;
    act(() => { wallet.address = OWNER; rerender(); });
    expect(b).toBeGreaterThan(firstA);
    expect(result.current.sessionGeneration).toBeGreaterThan(b);
    await act(async () => { await result.current.signIn(); });
    await act(async () => { old.resolve(session('obsolete-session')); await signing; });
    expect(result.current.token).toBe(makeToken('current-session'));
  });

  it('does not reauthenticate or change session generation for a locale-only rerender', async () => {
    const { result, rerender } = renderAuth();
    await act(async () => { await result.current.signIn(); });
    const generation = result.current.sessionGeneration;
    document.documentElement.lang = 'zh';
    rerender();
    expect(result.current.sessionGeneration).toBe(generation);
    expect(result.current.isAuthenticated).toBe(true);
    expect(verify).toHaveBeenCalledTimes(1);
    document.documentElement.lang = 'en';
  });

  it('uses a fresh generation on provider remount while restoring the existing credential', async () => {
    const first = renderAuth();
    await act(async () => { await first.result.current.signIn(); });
    const generation = first.result.current.sessionGeneration;
    first.unmount();
    const second = renderAuth();
    await act(async () => {});
    expect(second.result.current.sessionGeneration).toBeGreaterThan(generation);
    expect(second.result.current.isAuthenticated).toBe(true);
    expect(sign).toHaveBeenCalledTimes(1);
  });

  it('aborts a pending profile request when its account changes', () => {
    localStorage.setItem(TOKEN_KEY, makeToken('stored-session'));
    profile.mockReturnValue(new Promise(() => {}));
    const { rerender } = renderAuth();
    const signal = profile.mock.calls[0][1] as AbortSignal;
    expect(signal.aborted).toBe(false);
    act(() => { wallet.address = OTHER; rerender(); });
    expect(signal.aborted).toBe(true);
  });

  it('expires an active session without requiring navigation or another request', async () => {
    vi.useFakeTimers();
    try {
      const expires = new Date(Math.floor(Date.now() / 1000) * 1000 + 5000).toISOString();
      verify.mockResolvedValue({ ...session(), token: makeToken('short-session', OWNER, 11155111, expires), sessionExpiresAt: expires });
      const { result } = renderAuth();
      await act(async () => { await result.current.signIn(); });
      const generation = result.current.sessionGeneration;
      act(() => { vi.advanceTimersByTime(5001); });
      expect(result.current.isAuthenticated).toBe(false);
      expect(result.current.errorCode).toBe('session_expired');
      expect(result.current.sessionGeneration).toBeGreaterThan(generation);
      expect(localStorage.getItem(TOKEN_KEY)).toBeNull();
    } finally { vi.useRealTimers(); }
  });

  it('invalidates a pending sign-in after a logout in another browser tab', async () => {
    const old = deferred<AuthVerifyResponse>();
    verify.mockReturnValueOnce(old.promise);
    const { result } = renderAuth();
    let signing!: Promise<void>;
    await act(async () => { signing = result.current.signIn(); });
    const generation = result.current.sessionGeneration;
    act(() => { window.dispatchEvent(new StorageEvent('storage', { key: TOKEN_KEY, newValue: null })); });
    expect(result.current.sessionGeneration).toBeGreaterThan(generation);
    await act(async () => { old.resolve(session('obsolete-session')); await signing; });
    expect(result.current.token).toBeNull();
    expect(localStorage.getItem(TOKEN_KEY)).toBeNull();
  });
});

describe('server sign-out (FQ-01)', () => {
  async function restored() {
    localStorage.setItem(TOKEN_KEY, makeToken('stored-session'));
    const rendered = renderAuth();
    await act(async () => {});
    expect(rendered.result.current.isAuthenticated).toBe(true);
    return rendered;
  }

  it('signs out locally before the server answers, then reports the confirmed result', async () => {
    const answer = deferred<string>();
    revoke.mockReturnValue(answer.promise);
    const { result } = await restored();
    const before = result.current.sessionGeneration;
    act(() => { result.current.signOut(); });
    expect(result.current.isAuthenticated).toBe(false);
    expect(result.current.token).toBeNull();
    expect(result.current.sessionGeneration).toBeGreaterThan(before);
    expect(localStorage.getItem(TOKEN_KEY)).toBeNull();
    expect(disconnect).toHaveBeenCalled();
    expect(revoke).toHaveBeenCalledWith(makeToken('stored-session'));
    expect(result.current.serverSignOut).toBe('pending');
    await act(async () => { answer.resolve('revoked'); });
    expect(result.current.serverSignOut).toBe('revoked');
    expect(result.current.isAuthenticated).toBe(false);
  });

  it.each(['already-revoked', 'rejected'] as const)('reports a %s answer as it is', async (answer) => {
    revoke.mockResolvedValue(answer);
    const { result } = await restored();
    await act(async () => { result.current.signOut(); });
    expect(result.current.serverSignOut).toBe(answer);
  });

  it('keeps an unconfirmed sign-out unconfirmed and asks again when back online', async () => {
    revoke.mockResolvedValueOnce('unconfirmed').mockResolvedValueOnce('revoked');
    const { result } = await restored();
    await act(async () => { result.current.signOut(); });
    expect(result.current.serverSignOut).toBe('unconfirmed');
    expect(result.current.isAuthenticated).toBe(false);
    await act(async () => { window.dispatchEvent(new Event('online')); });
    expect(revoke).toHaveBeenCalledTimes(2);
    expect(revoke).toHaveBeenLastCalledWith(makeToken('stored-session'));
    expect(result.current.serverSignOut).toBe('revoked');
    // Nothing is left to retry once the server confirmed.
    await act(async () => { result.current.retryServerSignOut(); });
    expect(revoke).toHaveBeenCalledTimes(2);
  });

  it('a superseded server answer cannot overwrite a newer sign-out result', async () => {
    const first = deferred<string>();
    revoke.mockReturnValueOnce(first.promise).mockResolvedValueOnce('unconfirmed').mockResolvedValueOnce('revoked');
    const { result } = await restored();
    act(() => { result.current.signOut(); });
    await act(async () => { result.current.retryServerSignOut(); });
    expect(result.current.serverSignOut).toBe('unconfirmed');
    await act(async () => { first.resolve('revoked'); });
    expect(result.current.serverSignOut).toBe('unconfirmed');
  });

  it('ends on the server a session whose verification arrives after sign-out', async () => {
    const pending = deferred<AuthVerifyResponse>();
    verify.mockReturnValue(pending.promise);
    const { result } = renderAuth();
    let signing!: Promise<void>;
    await act(async () => { signing = result.current.signIn(); });
    act(() => { result.current.signOut(); });
    await act(async () => { pending.resolve(session('late-session')); await signing; });
    expect(result.current.isAuthenticated).toBe(false);
    expect(localStorage.getItem(TOKEN_KEY)).toBeNull();
    expect(revoke).toHaveBeenCalledWith(makeToken('late-session'));
  });

  it('ends on the server a verified session that does not match the wallet', async () => {
    verify.mockResolvedValue({ ...session('foreign-session'), token: makeToken('foreign-session', OTHER), address: OTHER });
    const { result } = renderAuth();
    await act(async () => { await result.current.signIn(); });
    expect(result.current.isAuthenticated).toBe(false);
    expect(revoke).toHaveBeenCalledWith(makeToken('foreign-session', OTHER));
  });

  it('a sign-out with no credential asks the server nothing', async () => {
    const { result } = renderAuth();
    act(() => { result.current.signOut(); });
    expect(revoke).not.toHaveBeenCalled();
    expect(result.current.serverSignOut).toBeNull();
  });
});
