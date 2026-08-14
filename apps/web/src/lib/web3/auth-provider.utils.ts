'use client';

import { SiweMessage } from 'siwe';
import { buildApiUrl } from '../api/base-url';
import type {
  AuthErrorPayload,
  AuthNonceChallenge,
  AuthVerifyResponse,
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

export async function fetchAuthenticatedProfile(storedToken: string) {
  const response = await fetch(buildApiUrl('/auth/me'), {
    headers: {
      Authorization: `Bearer ${storedToken}`,
    },
  });

  if (!response.ok) {
    return null;
  }

  const data = await response.json();
  return data.data || data;
}

export async function fetchAuthNonce(address: string): Promise<AuthNonceChallenge> {
  const response = await fetch(buildApiUrl('/auth/nonce'), {
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
): Promise<AuthVerifyResponse> {
  const response = await fetch(buildApiUrl('/auth/verify'), {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({ message, signature }),
  });

  if (!response.ok) {
    throw await createApiError(response, 'Verification failed');
  }

  return parseApiPayload<AuthVerifyResponse>(response);
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
