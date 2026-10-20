'use client';

import { createContext, type ReactNode, useCallback, useEffect, useState, useContext, useRef } from 'react';
import { useAccount, useSignMessage, useDisconnect } from 'wagmi';
import {
  buildSiweMessage, clearStoredToken, fetchAuthenticatedProfile, fetchAuthNonce,
  readStoredToken, revokeServerSession, verifySiweSignature, writeStoredToken, TOKEN_KEY,
} from './auth-provider.utils';
import {
  emptyAuthState, getConnectorStatus, readSessionIdentity,
  sessionMatchesWallet, validateVerifiedSession,
  type AuthenticatedSession, type ConnectorStatus,
} from './auth-session';
import type { ServerSignOutState, WalletAuthErrorCode, WalletAuthStatus } from './auth.types';
import { supportedChains } from './config';

interface AuthContextType {
  isAuthenticated: boolean;
  isLoading: boolean;
  isRestoring: boolean;
  connectorStatus: ConnectorStatus;
  sessionGeneration: number;
  error: string | null;
  errorCode: WalletAuthErrorCode | null;
  status: WalletAuthStatus;
  token: string | null;
  authenticatedAddress: string | null;
  authenticatedChainId: number | null;
  sessionExpiresAt: string | null;
  signIn: () => Promise<void>;
  /** Signs out locally at once, then asks the server to end the session. */
  signOut: () => void;
  retrySession: () => void;
  /**
   * Server side of the last sign-out on this page: pending, a confirmed
   * result, or unconfirmed (the server may keep the session until it expires).
   * Never a reason to show private data: local sign-out already happened.
   */
  serverSignOut: ServerSignOutState;
  /** Asks the server again after an unconfirmed sign-out; a no-op otherwise. */
  retryServerSignOut: () => void;
}

const AuthContext = createContext<AuthContextType | null>(null);
export function useAuth(): AuthContextType {
  const context = useContext(AuthContext);
  if (!context) throw new Error('useAuth must be used within an AuthProvider');
  return context;
}

export function AuthProvider({ children }: { children: ReactNode }) {
  const { address, chainId, status: accountStatus, connector } = useAccount();
  const { signMessageAsync } = useSignMessage();
  const { disconnect } = useDisconnect();
  const connectorStatus = getConnectorStatus(accountStatus);
  const identityKey = `${connectorStatus}:${address?.toLowerCase()}:${chainId}:${connector?.id}:${connector?.uid}`;
  const [state, setState] = useState(() => emptyAuthState(identityKey));
  const [restoreRevision, setRestoreRevision] = useState(0);
  const [serverSignOut, setServerSignOut] = useState<ServerSignOutState>(null);
  // The signed-out token is kept in memory only, and only while an
  // unconfirmed server sign-out could still be retried. It never authorizes
  // anything here: storage and session state are already cleared.
  const revocation = useRef<{ token: string; attempt: number } | null>(null);
  const operation = useRef(0);
  const pending = useRef<AbortController | null>(null);
  const mounted = useRef(false);
  const liveIdentity = useRef(identityKey);
  liveIdentity.current = identityKey;

  // Reset before children render. An effect-only reset briefly exposes A's
  // private view under B, and can reuse A's cache when the wallet returns to A.
  if (state.identityKey !== identityKey) setState(emptyAuthState(identityKey));

  const cancelPending = useCallback(() => {
    operation.current += 1;
    pending.current?.abort();
    pending.current = null;
  }, []);
  useEffect(() => {
    mounted.current = true;
    return () => { mounted.current = false; cancelPending(); };
  }, [cancelPending]);
  const isCurrent = useCallback((version: number, identity: string) =>
    mounted.current && operation.current === version && liveIdentity.current === identity, []);

  const clearAuth = useCallback((nextStatus: WalletAuthStatus = 'idle', code: WalletAuthErrorCode | null = null) => {
    cancelPending();
    clearStoredToken();
    setState({ ...emptyAuthState(liveIdentity.current), status: nextStatus, errorCode: code,
      error: code === 'wrong_chain' ? 'Please switch to a supported chain before signing in.' : null });
  }, [cancelPending]);
  const applySession = useCallback((session: AuthenticatedSession) => {
    writeStoredToken(session.token);
    setState({ ...emptyAuthState(liveIdentity.current), session, status: 'authenticated' });
  }, []);
  const retrySession = useCallback(() => {
    cancelPending();
    setState(emptyAuthState(liveIdentity.current));
    setRestoreRevision((revision) => revision + 1);
  }, [cancelPending]);
  const revokeOnServer = useCallback((token: string) => {
    const attempt = (revocation.current?.attempt ?? 0) + 1;
    revocation.current = { token, attempt };
    setServerSignOut('pending');
    void revokeServerSession(token).then((result) => {
      // A newer sign-out supersedes this answer.
      if (revocation.current?.attempt !== attempt) return;
      if (result !== 'unconfirmed') revocation.current = null;
      if (mounted.current) setServerSignOut(result);
    });
  }, []);
  const retryServerSignOut = useCallback(() => {
    const pendingSignOut = revocation.current;
    if (pendingSignOut) revokeOnServer(pendingSignOut.token);
  }, [revokeOnServer]);

  useEffect(() => {
    cancelPending();
    if (connectorStatus === 'restoring') {
      setState((current) => ({ ...current, status: 'connecting', isLoading: true }));
      return;
    }
    if (connectorStatus === 'disconnected' || !address || !chainId) { clearAuth(); return; }
    if (!supportedChains.some((chain) => chain.id === chainId)) { clearAuth('wrong-chain', 'wrong_chain'); return; }
    const storedToken = readStoredToken();
    if (!storedToken) return;
    const identity = readSessionIdentity(storedToken);
    if (!identity || !sessionMatchesWallet(identity, address, chainId)) { clearAuth(); return; }

    const controller = new AbortController();
    pending.current = controller;
    const version = operation.current;
    setState((current) => ({ ...current, status: 'connecting', isLoading: true }));
    void fetchAuthenticatedProfile(storedToken, controller.signal).then((profile) => {
      if (!isCurrent(version, identityKey)) return;
      if (!profile || profile.address.toLowerCase() !== identity.address || profile.chainId !== identity.chainId
        || !sessionMatchesWallet(identity, address, chainId)) { clearAuth(); return; }
      applySession({ token: storedToken, ...identity });
    }).catch(() => {
      if (!isCurrent(version, identityKey)) return;
      // Keep the credential for an explicit/focus/online retry, but never expose
      // private data until the server has verified this restored session.
      setState((current) => ({ ...current, status: 'error', isLoading: false,
        error: 'Session verification is temporarily unavailable', errorCode: null }));
    });
    return () => { if (operation.current === version) cancelPending(); };
  }, [address, applySession, cancelPending, chainId, clearAuth, connectorStatus, identityKey, isCurrent, restoreRevision]);

  useEffect(() => {
    const onStorage = (event: StorageEvent) => {
      if (event.key === TOKEN_KEY || event.key === null) retrySession();
    };
    const onResume = () => {
      if (document.visibilityState === 'hidden' || state.isLoading) return;
      if (!state.session && readStoredToken()) retrySession();
      else if (state.session && Date.parse(state.session.sessionExpiresAt) <= Date.now()) clearAuth('idle', 'session_expired');
    };
    // Back online: ask again about a sign-out the server never confirmed.
    const onOnline = () => {
      if (serverSignOut === 'unconfirmed') retryServerSignOut();
      onResume();
    };
    window.addEventListener('storage', onStorage);
    window.addEventListener('online', onOnline);
    window.addEventListener('focus', onResume);
    document.addEventListener('visibilitychange', onResume);
    return () => {
      window.removeEventListener('storage', onStorage);
      window.removeEventListener('online', onOnline);
      window.removeEventListener('focus', onResume);
      document.removeEventListener('visibilitychange', onResume);
    };
  }, [clearAuth, retryServerSignOut, retrySession, serverSignOut, state.isLoading, state.session]);

  useEffect(() => {
    if (!state.session) return;
    const expiresAt = Date.parse(state.session.sessionExpiresAt);
    let timer: number;
    const checkExpiry = () => {
      const remaining = expiresAt - Date.now();
      if (remaining <= 0) { clearAuth('idle', 'session_expired'); return; }
      // Browser timers overflow past signed 32-bit milliseconds.
      timer = window.setTimeout(checkExpiry, Math.min(remaining, 2_147_483_647));
    };
    checkExpiry();
    return () => window.clearTimeout(timer);
  }, [clearAuth, state.session]);

  const signIn = useCallback(async () => {
    if (!mounted.current || liveIdentity.current !== identityKey || connectorStatus === 'restoring') return;
    cancelPending();
    if (connectorStatus !== 'connected' || !address || !chainId) {
      setState((current) => ({ ...current, status: 'error', error: 'Please connect your wallet first', isLoading: false }));
      return;
    }
    if (!supportedChains.some((chain) => chain.id === chainId)) { clearAuth('wrong-chain', 'wrong_chain'); return; }
    const version = operation.current;
    const controller = new AbortController();
    pending.current = controller;
    setState((current) => ({ ...current, status: 'signing', isLoading: true, error: null, errorCode: null }));
    try {
      const challenge = await fetchAuthNonce(address, controller.signal);
      if (!isCurrent(version, identityKey)) return;
      const message = buildSiweMessage({ address, chainId, challenge });
      // Bind the initiating wallet even if the connector awaits provider discovery.
      const signature = await signMessageAsync({ message, account: address, connector });
      if (!isCurrent(version, identityKey)) return;
      setState((current) => ({ ...current, status: 'verifying' }));
      const result = await verifySiweSignature(message, signature, controller.signal);
      if (!isCurrent(version, identityKey)) {
        // Signed out or switched wallet while verifying: this session will
        // never be used here, so it must not stay live on the server either.
        void revokeServerSession(result.token);
        return;
      }
      const session = validateVerifiedSession(result, address, chainId);
      if (!session) {
        void revokeServerSession(result.token);
        throw new Error('The verified session does not match the connected wallet');
      }
      applySession(session);
    } catch (err) {
      if (!isCurrent(version, identityKey)) return;
      const code = err instanceof Error && 'code' in err ? err.code as WalletAuthErrorCode : null;
      setState((current) => ({ ...current, status: code === 'wrong_chain' ? 'wrong-chain' : 'error',
        error: err instanceof Error ? err.message : 'Sign in failed', errorCode: code }));
    } finally {
      if (isCurrent(version, identityKey)) setState((current) => ({ ...current, isLoading: false }));
    }
  }, [address, applySession, cancelPending, chainId, clearAuth, connector, connectorStatus, identityKey, isCurrent, signMessageAsync]);

  const signOut = useCallback(() => {
    // Local first: cancel in-flight work, drop the stored credential and start
    // a new generation before any network round trip, so nothing private stays
    // visible and no late answer can restore the session.
    const token = state.session?.token ?? readStoredToken();
    clearAuth();
    disconnect();
    if (token) revokeOnServer(token);
  }, [clearAuth, disconnect, revokeOnServer, state.session]);
  const session = state.identityKey === identityKey && connectorStatus === 'connected' ? state.session : null;
  const isRestoring = connectorStatus === 'restoring' || state.status === 'connecting';
  return <AuthContext.Provider value={{
    isAuthenticated: session !== null, isLoading: isRestoring || state.isLoading, isRestoring, connectorStatus,
    sessionGeneration: state.sessionGeneration, error: state.error, errorCode: state.errorCode,
    status: isRestoring ? 'connecting' : state.status, token: session?.token ?? null,
    authenticatedAddress: session?.address ?? null, authenticatedChainId: session?.chainId ?? null,
    sessionExpiresAt: session?.sessionExpiresAt ?? null, signIn, signOut, retrySession,
    serverSignOut, retryServerSignOut,
  }}>{children}</AuthContext.Provider>;
}
