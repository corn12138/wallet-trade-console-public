'use client';

/**
 * @file useSiweAuth
 * @description SIWE (Sign-In with Ethereum) 认证 Hook
 *
 * 实现流程：
 * 1. 从后端获取 nonce
 * 2. 构建 SIWE 消息
 * 3. 请求钱包签名
 * 4. 发送到后端验证
 * 5. 获取 JWT token
 */

import { useState, useCallback } from 'react';
import { buildApiUrl } from '@/lib/api/base-url';
import { useAccount, useSignMessage } from 'wagmi';
import { SiweMessage } from 'siwe';

// 认证状态
export interface AuthState {
  isAuthenticated: boolean;
  token: string | null;
  address: string | null;
  chainId: number | null;
  isLoading: boolean;
  error: string | null;
}

// Hook 返回值
export interface UseSiweAuthReturn extends AuthState {
  signIn: () => Promise<void>;
  signOut: () => void;
  getToken: () => string | null;
}

/**
 * SIWE 认证 Hook
 *
 * 使用示例：
 * ```tsx
 * const { isAuthenticated, signIn, signOut, isLoading, error } = useSiweAuth();
 *
 * if (isAuthenticated) {
 *   return <p>已登录</p>;
 * }
 *
 * return <button onClick={signIn} disabled={isLoading}>登录</button>;
 * ```
 */
export function useSiweAuth(): UseSiweAuthReturn {
  const { address, chainId, isConnected } = useAccount();
  const { signMessageAsync } = useSignMessage();

  const [authState, setAuthState] = useState<AuthState>({
    isAuthenticated: false,
    token: null,
    address: null,
    chainId: null,
    isLoading: false,
    error: null,
  });

  /**
   * 获取 nonce
   */
  const fetchNonce = useCallback(async (walletAddress: string): Promise<string> => {
    const response = await fetch(buildApiUrl('/auth/nonce'), {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
      },
      body: JSON.stringify({ address: walletAddress }),
    });
    if (!response.ok) {
      throw new Error('Failed to fetch nonce');
    }
    const data = await response.json();
    return data.data?.nonce || data.nonce;
  }, []);

  /**
   * 验证签名
   */
  const verifySignature = useCallback(
    async (message: string, signature: string): Promise<{ token: string; address: string; chainId: number }> => {
      const response = await fetch(buildApiUrl('/auth/verify'), {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
        },
        body: JSON.stringify({ message, signature }),
      });

      if (!response.ok) {
        const errorData = await response.json().catch(() => ({}));
        throw new Error(errorData.message || 'Failed to verify signature');
      }

      const data = await response.json();
      return data.data || data;
    },
    []
  );

  /**
   * 执行 SIWE 登录
   */
  const signIn = useCallback(async () => {
    if (!isConnected || !address || !chainId) {
      setAuthState((prev) => ({
        ...prev,
        error: 'Please connect your wallet first',
      }));
      return;
    }

    setAuthState((prev) => ({
      ...prev,
      isLoading: true,
      error: null,
    }));

    try {
      // 1. 获取 nonce
      const nonce = await fetchNonce(address);

      // 2. 构建 SIWE 消息
      const siweMessage = new SiweMessage({
        domain: window.location.host,
        address,
        statement: 'Sign in with Ethereum to Web3 Trading Platform',
        uri: window.location.origin,
        version: '1',
        chainId,
        nonce,
      });

      const message = siweMessage.prepareMessage();

      // 3. 请求钱包签名
      const signature = await signMessageAsync({ message });

      // 4. 发送到后端验证
      const result = await verifySignature(message, signature);

      // 5. 保存 token
      localStorage.setItem('web3_token', result.token);

      setAuthState({
        isAuthenticated: true,
        token: result.token,
        address: result.address,
        chainId: result.chainId,
        isLoading: false,
        error: null,
      });
    } catch (error) {
      const errorMessage = error instanceof Error ? error.message : 'Sign in failed';
      setAuthState((prev) => ({
        ...prev,
        isLoading: false,
        error: errorMessage,
      }));
    }
  }, [isConnected, address, chainId, fetchNonce, signMessageAsync, verifySignature]);

  /**
   * 登出
   */
  const signOut = useCallback(() => {
    localStorage.removeItem('web3_token');
    setAuthState({
      isAuthenticated: false,
      token: null,
      address: null,
      chainId: null,
      isLoading: false,
      error: null,
    });
  }, []);

  /**
   * 获取 token
   */
  const getToken = useCallback(() => {
    return authState.token || localStorage.getItem('web3_token');
  }, [authState.token]);

  return {
    ...authState,
    signIn,
    signOut,
    getToken,
  };
}
