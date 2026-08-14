'use client';

import {
  createContext,
  type ReactNode,
  useCallback,
  useEffect,
  useState,
  useContext,
} from 'react';
import { useAccount, useSignMessage, useDisconnect } from 'wagmi';
import {
  buildSiweMessage,
  clearStoredToken,
  fetchAuthenticatedProfile,
  fetchAuthNonce,
  readStoredToken,
  verifySiweSignature,
  writeStoredToken,
} from './auth-provider.utils';
import type { WalletAuthErrorCode, WalletAuthStatus } from './auth.types';
import { supportedChains } from './config';

interface AuthContextType {
  isAuthenticated: boolean;
  isLoading: boolean;
  error: string | null;
  errorCode: WalletAuthErrorCode | null;
  status: WalletAuthStatus;
  token: string | null;
  authenticatedAddress: string | null;
  sessionExpiresAt: string | null;
  signIn: () => Promise<void>;
  signOut: () => void;
}

const AuthContext = createContext<AuthContextType | null>(null);

export function useAuth(): AuthContextType {
  const context = useContext(AuthContext);
  if (!context) {
    throw new Error('useAuth must be used within an AuthProvider');
  }
  return context;
}

interface AuthProviderProps {
  children: ReactNode;
}

export function AuthProvider({ children }: AuthProviderProps) {
  const { address, chainId, isConnected } = useAccount();
  const { signMessageAsync } = useSignMessage();
  const { disconnect } = useDisconnect();
  const isSupportedChain = (nextChainId?: number): nextChainId is (typeof supportedChains)[number]['id'] =>
    nextChainId !== undefined && supportedChains.some((chain) => chain.id === nextChainId);

  const [isAuthenticated, setIsAuthenticated] = useState(false);
  const [isLoading, setIsLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [errorCode, setErrorCode] = useState<WalletAuthErrorCode | null>(null);
  const [status, setStatus] = useState<WalletAuthStatus>('idle');
  const [token, setToken] = useState<string | null>(null);
  const [authenticatedAddress, setAuthenticatedAddress] = useState<string | null>(null);
  const [sessionExpiresAt, setSessionExpiresAt] = useState<string | null>(null);

  const clearAuth = useCallback((nextStatus: WalletAuthStatus = 'idle') => {
    clearStoredToken();
    setToken(null);
    setIsAuthenticated(false);
    setAuthenticatedAddress(null);
    setSessionExpiresAt(null);
    setError(null);
    setErrorCode(null);
    setStatus(nextStatus);
  }, []);

  const applyAuthenticatedSession = useCallback((
    nextToken: string,
    nextAddress: string,
    nextSessionExpiresAt: string,
  ) => {
    writeStoredToken(nextToken);
    setToken(nextToken);
    setIsAuthenticated(true);
    setAuthenticatedAddress(nextAddress);
    setSessionExpiresAt(nextSessionExpiresAt);
    setError(null);
    setErrorCode(null);
    setStatus('authenticated');
  }, []);

  const applyErrorState = useCallback((
    nextStatus: WalletAuthStatus,
    nextError: string,
    nextErrorCode: WalletAuthErrorCode | null = null,
  ) => {
    setError(nextError);
    setErrorCode(nextErrorCode);
    setStatus(nextStatus);
  }, []);

  const restoreStoredSession = useCallback(async (storedToken: string, walletAddress: string) => {
    const userData = await fetchAuthenticatedProfile(storedToken);

    if (!userData || userData.address !== walletAddress.toLowerCase()) {
      clearAuth();
      return;
    }

    setToken(storedToken);
    setIsAuthenticated(true);
    setAuthenticatedAddress(userData.address);
    setStatus('authenticated');
  }, [clearAuth]);

  useEffect(() => {
    if (!isConnected || !address) {
      return;
    }

    const storedToken = readStoredToken();

    if (!storedToken) {
      return;
    }

    let isCancelled = false;

    restoreStoredSession(storedToken, address).catch(() => {
      if (!isCancelled) {
        clearAuth();
      }
    });

    return () => {
      isCancelled = true;
    };
  }, [address, clearAuth, isConnected, restoreStoredSession]);

  useEffect(() => {
    if (!isConnected) {
      clearAuth();
    }
  }, [clearAuth, isConnected]);

  useEffect(() => {
    if (!isConnected || !address) {
      return;
    }

    if (!isSupportedChain(chainId)) {
      if (!isAuthenticated) {
        applyErrorState('wrong-chain', 'Please switch to a supported chain before signing in.', 'wrong_chain');
      }
      return;
    }

    if (!isAuthenticated && status === 'wrong-chain') {
      setError(null);
      setErrorCode(null);
      setStatus('idle');
    }
  }, [address, applyErrorState, chainId, isAuthenticated, isConnected, status]);

  useEffect(() => {
    if (authenticatedAddress && address && authenticatedAddress !== address.toLowerCase()) {
      clearAuth();
    }
  }, [address, authenticatedAddress, clearAuth]);

  const signIn = useCallback(async () => {
    if (!isConnected || !address || !chainId) {
      applyErrorState('error', 'Please connect your wallet first');
      return;
    }

    if (!isSupportedChain(chainId)) {
      applyErrorState('wrong-chain', 'Please switch to a supported chain before signing in.', 'wrong_chain');
      return;
    }

    setIsLoading(true);
    setError(null);
    setErrorCode(null);

    try {
      setStatus('signing');
      const challenge = await fetchAuthNonce(address);
      const message = buildSiweMessage({ address, chainId, challenge });
      const signature = await signMessageAsync({ message });
      setStatus('verifying');
      const result = await verifySiweSignature(message, signature);
      applyAuthenticatedSession(result.token, result.address, result.sessionExpiresAt);
    } catch (err) {
      const errorMessage = err instanceof Error ? err.message : 'Sign in failed';
      const errorCodeValue = err instanceof Error && 'code' in err
        ? (err.code as WalletAuthErrorCode | undefined)
        : undefined;
      applyErrorState(
        errorCodeValue === 'wrong_chain' ? 'wrong-chain' : 'error',
        errorMessage,
        errorCodeValue ?? null,
      );
    } finally {
      setIsLoading(false);
    }
  }, [address, applyAuthenticatedSession, applyErrorState, chainId, isConnected, signMessageAsync]);

  const signOut = useCallback(() => {
    clearAuth();
    disconnect();
  }, [clearAuth, disconnect]);

  return (
    <AuthContext.Provider
      value={{
        isAuthenticated,
        isLoading,
        error,
        errorCode,
        status,
        token,
        authenticatedAddress,
        sessionExpiresAt,
        signIn,
        signOut,
      }}
    >
      {children}
    </AuthContext.Provider>
  );
}
