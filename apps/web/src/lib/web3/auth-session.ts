import type { AuthenticatedWalletProfile, AuthVerifyResponse, WalletAuthErrorCode, WalletAuthStatus } from './auth.types';

export type ConnectorStatus = 'restoring' | 'connected' | 'disconnected';
export type SessionIdentity = { address: string; chainId: number; sessionExpiresAt: string };
export type AuthenticatedSession = SessionIdentity & { token: string };
export interface AuthState {
  identityKey: string;
  sessionGeneration: number;
  session: AuthenticatedSession | null;
  status: WalletAuthStatus;
  isLoading: boolean;
  error: string | null;
  errorCode: WalletAuthErrorCode | null;
}

// Generations also survive provider remounts within this browser runtime. A
// same-wallet remount must not reuse an old private query or mutation scope.
let generation = 0;
export function nextSessionGeneration() { return ++generation; }

export function emptyAuthState(identityKey: string): AuthState {
  return { identityKey, sessionGeneration: nextSessionGeneration(), session: null,
    status: 'idle', isLoading: false, error: null, errorCode: null };
}

export function getConnectorStatus(status: string): ConnectorStatus {
  return status === 'connecting' || status === 'reconnecting' ? 'restoring'
    : status === 'connected' ? 'connected' : 'disconnected';
}

const addressPattern = /^0x[0-9a-fA-F]{40}$/;
export function isWalletProfile(value: unknown): value is AuthenticatedWalletProfile {
  if (!value || typeof value !== 'object') return false;
  const profile = value as Record<string, unknown>;
  return typeof profile.address === 'string' && addressPattern.test(profile.address)
    && typeof profile.chainId === 'number' && Number.isSafeInteger(profile.chainId) && profile.chainId > 0;
}

export function isVerifyResponse(value: unknown): value is AuthVerifyResponse {
  if (!isWalletProfile(value)) return false;
  const session = value as unknown as Record<string, unknown>;
  return typeof session.token === 'string' && session.token.length > 0
    && typeof session.sessionExpiresAt === 'string' && Number.isFinite(Date.parse(session.sessionExpiresAt));
}

/** Decode only to reject a mismatched/expired UI scope; /auth/me still verifies the JWT. */
export function readSessionIdentity(token: string): SessionIdentity | null {
  try {
    const parts = token.split('.');
    if (parts.length !== 3 || parts.some((part) => !part)) return null;
    const payload: unknown = JSON.parse(atob(parts[1].replace(/-/g, '+').replace(/_/g, '/')));
    if (!payload || typeof payload !== 'object') return null;
    const claims = payload as Record<string, unknown>;
    if (claims.type !== 'web3' || typeof claims.sub !== 'string' || !addressPattern.test(claims.sub)
      || typeof claims.chainId !== 'number' || !Number.isSafeInteger(claims.chainId) || claims.chainId <= 0
      || typeof claims.exp !== 'number' || !Number.isSafeInteger(claims.exp) || claims.exp <= 0) return null;
    const expires = new Date(claims.exp * 1000);
    if (!Number.isFinite(expires.getTime())) return null;
    return { address: claims.sub.toLowerCase(), chainId: claims.chainId, sessionExpiresAt: expires.toISOString() };
  } catch { return null; }
}

export function sessionMatchesWallet(session: SessionIdentity, address: string, chainId: number, now = Date.now()) {
  return session.address.toLowerCase() === address.toLowerCase() && session.chainId === chainId
    && Date.parse(session.sessionExpiresAt) > now;
}

export function validateVerifiedSession(response: AuthVerifyResponse, address: string, chainId: number): AuthenticatedSession | null {
  if (!isVerifyResponse(response)) return null;
  const identity = readSessionIdentity(response.token);
  if (!identity || !sessionMatchesWallet(identity, address, chainId)
    || response.address.toLowerCase() !== identity.address || response.chainId !== identity.chainId
    || Date.parse(response.sessionExpiresAt) !== Date.parse(identity.sessionExpiresAt)) return null;
  return { token: response.token, ...identity };
}
