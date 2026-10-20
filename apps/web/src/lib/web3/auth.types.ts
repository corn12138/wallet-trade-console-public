export type WalletAuthStatus =
  | 'idle'
  | 'discovering'
  | 'connecting'
  | 'wrong-chain'
  | 'signing'
  | 'verifying'
  | 'authenticated'
  | 'error';

export type WalletAuthErrorCode =
  | 'wrong_chain'
  | 'invalid_domain'
  | 'expired_nonce'
  | 'reused_nonce'
  | 'invalid_signature'
  | 'session_expired';

export interface AuthNonceChallenge {
  nonce: string;
  domain: string;
  uri: string;
  statement: string;
  issuedAt: string;
  expirationTime: string;
  allowedChainIds: number[];
}

export interface AuthenticatedWalletProfile {
  address: string;
  chainId: number;
}

export interface AuthVerifyResponse {
  token: string;
  address: string;
  chainId: number;
  sessionExpiresAt: string;
}

/**
 * Server side of a sign-out. Local sign-out is always immediate; this reports
 * what the server said about the token afterwards:
 * - revoked / already-revoked: the server confirmed the session has ended;
 * - rejected: the server no longer accepts the token (expired or invalid);
 * - unconfirmed: no usable answer (offline, timeout, 5xx) — the session may
 *   stay valid on the server until it expires.
 */
export type ServerSignOutResult = 'revoked' | 'already-revoked' | 'rejected' | 'unconfirmed';
export type ServerSignOutState = ServerSignOutResult | 'pending' | null;

export interface AuthErrorPayload {
  code?: WalletAuthErrorCode;
  message?: string | string[];
}
