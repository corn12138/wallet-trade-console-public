'use client';

/**
 * @file Web3 Provider
 * @description wagmi、React Query 和 Auth Provider 封装
 *
 * 使用方式：
 * 在 layout.tsx 中包裹应用
 * <Web3Provider>{children}</Web3Provider>
 */

import { useEffect, useState, type ReactNode } from 'react';
import { WagmiProvider } from 'wagmi';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { wagmiConfig } from './config';
import { AuthProvider } from './auth-provider';
import { syncContractAddresses } from './contracts';

interface Web3ProviderProps {
  children: ReactNode;
}

/**
 * Web3Provider
 *
 * 提供：
 * - wagmi 钱包连接功能
 * - React Query 数据缓存
 * - SIWE 认证状态管理
 */
export function Web3Provider({ children }: Web3ProviderProps) {
  // React Query 客户端（每个请求创建新实例，避免服务端状态泄露）
  const [queryClient] = useState(
    () =>
      new QueryClient({
        defaultOptions: {
          queries: {
            // 链上数据刷新配置
            staleTime: 1000 * 60, // 1 分钟后过期
            gcTime: 1000 * 60 * 5, // 5 分钟后垃圾回收
            refetchOnWindowFocus: false, // 窗口聚焦不刷新
            retry: 2, // 失败重试 2 次
          },
        },
      })
  );

  useEffect(() => {
    void syncContractAddresses();
  }, []);

  return (
    <WagmiProvider config={wagmiConfig}>
      <QueryClientProvider client={queryClient}>
        <AuthProvider>{children}</AuthProvider>
      </QueryClientProvider>
    </WagmiProvider>
  );
}
