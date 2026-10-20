'use client';

import { SiweMessage } from 'siwe';
import { buildApiUrl } from '../api/base-url';
import { isVerifyResponse, isWalletProfile } from './auth-session';
import type { AuthenticatedWalletProfile } from './auth.types';
import type {
  AuthErrorPayload,
  AuthNonceChallenge,
  AuthVerifyResponse,
  ServerSignOutResult,
} from './auth.types';

export const TOKEN_KEY = 'web3_auth_token';

export function readStoredToken() {
  return localStorage.getItem(TOKEN_KEY);
}

export function writeStoredToken(token: string) {
  localStorage.setItem(TOKEN_KEY, token);
}

export function clearStoredToken() {
  localStorage.removeItem(TOKEN_KEY);
}

export async function fetchAuthenticatedProfile(storedToken: string, signal?: AbortSignal): Promise<AuthenticatedWalletProfile | null> {
  const response = await fetch(buildApiUrl('/auth/me'), {
    signal,
    cache: 'no-store',
    headers: {
      Authorization: `Bearer ${storedToken}`,
    },
  });

  if (response.status === 401 || response.status === 403) return null;
  // A transport/server failure is not evidence that the wallet session expired.
  if (!response.ok) throw new Error('Session verification is temporarily unavailable');
  const data = await parseApiPayload<unknown>(response);
  if (!isWalletProfile(data)) throw new Error('Invalid session verification response');
  return { address: data.address.toLowerCase(), chainId: data.chainId };
}

export async function fetchAuthNonce(address: string, signal?: AbortSignal): Promise<AuthNonceChallenge> {
  const response = await fetch(buildApiUrl('/auth/nonce'), {
    signal,
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({ address }),
  });

  if (!response.ok) {
    throw new Error('Failed to fetch nonce');
  }

  return parseApiPayload<AuthNonceChallenge>(response);
}

export function buildSiweMessage({
  address,
  chainId,
  challenge,
}: {
  address: string;
  chainId: number;
  challenge: AuthNonceChallenge;
}) {
  const siweMessage = new SiweMessage({
    domain: challenge.domain,
    address,
    statement: challenge.statement,
    uri: challenge.uri,
    version: '1',
    chainId,
    nonce: challenge.nonce,
    issuedAt: challenge.issuedAt,
    expirationTime: challenge.expirationTime,
  });

  return siweMessage.prepareMessage();
}

export async function verifySiweSignature(
  message: string,
  signature: string,
  signal?: AbortSignal,
): Promise<AuthVerifyResponse> {
  const response = await fetch(buildApiUrl('/auth/verify'), {
    signal,
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({ message, signature }),
  });

  if (!response.ok) {
    throw await createApiError(response, 'Verification failed');
  }

  const result = await parseApiPayload<unknown>(response);
  if (!isVerifyResponse(result)) throw new Error('Invalid session verification response');
  return result;
}

export const SERVER_SIGN_OUT_TIMEOUT_MS = 8_000;

/**
 * Ends the token's session on the server (POST /web3-auth/logout). It never
 * throws and never claims more than the server said: only a 2xx with the
 * expected body counts as confirmed, a 401 means the server no longer accepts
 * the token, and anything else (offline, timeout, 5xx, unreadable body) is
 * unconfirmed. keepalive lets the request finish if the page is closing.
 */
export async function revokeServerSession(token: string, signal?: AbortSignal): Promise<ServerSignOutResult> {
  const controller = new AbortController();
  const abort = () => controller.abort();
  const timer = setTimeout(abort, SERVER_SIGN_OUT_TIMEOUT_MS);
  signal?.addEventListener('abort', abort, { once: true });
  try {
    const response = await fetch(buildApiUrl('/web3-auth/logout'), {
      method: 'POST',
      signal: controller.signal,
      cache: 'no-store',
      credentials: 'omit',
      keepalive: true,
      headers: { Authorization: `Bearer ${token}` },
    });
    if (response.status === 401) return 'rejected';
    if (!response.ok) return 'unconfirmed';
    const body: unknown = await response.json();
    if (!body || typeof body !== 'object') return 'unconfirmed';
    const { revoked, alreadyRevoked } = body as Record<string, unknown>;
    if (revoked !== true || typeof alreadyRevoked !== 'boolean') return 'unconfirmed';
    return alreadyRevoked ? 'already-revoked' : 'revoked';
  } catch {
    return 'unconfirmed';
  } finally {
    clearTimeout(timer);
    signal?.removeEventListener('abort', abort);
  }
}

async function parseApiPayload<T>(response: Response): Promise<T> {
  const data = await response.json();
  return (data.data || data) as T;
}

async function createApiError(response: Response, fallbackMessage: string) {
  const errorData = (await response.json().catch(() => ({}))) as AuthErrorPayload;
  const message = Array.isArray(errorData.message)
    ? errorData.message.join(', ')
    : errorData.message || fallbackMessage;
  const error = new Error(message) as Error & { code?: string };
  error.code = errorData.code;
  return error;
}
