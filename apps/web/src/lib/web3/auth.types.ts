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

export interface AuthVerifyResponse {
  token: string;
  address: string;
  chainId: number;
  sessionExpiresAt: string;
}

export interface AuthErrorPayload {
  code?: WalletAuthErrorCode;
  message?: string | string[];
}
