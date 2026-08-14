'use client';

/**
 * @file WalletButton
 * @description 钱包连接按钮组件
 *
 * 功能：
 * - 显示连接状态
 * - 连接/断开钱包
 * - 显示地址和余额
 * - 切换网络
 */

import { useState } from 'react';
import { useTranslations } from 'next-intl';
import {
  useAccount,
  useConnect,
  useDisconnect,
  useBalance,
  useChainId,
  useSwitchChain,
} from 'wagmi';
import { useClientMounted } from '@/hooks/web3';
import { useAuth } from '@/lib/web3';
import { supportedChains } from '@/lib/web3/config';
import {
  formatAddress,
  formatBalanceLabel,
  getConnectorOptions,
  getWalletStatusMessageKey,
  resolveWalletConnectionStatus,
} from './connect-button.utils';

/**
 * 钱包连接按钮
 */
export function WalletButton() {
  const t = useTranslations('web3Connect');
  const mounted = useClientMounted();
  const [isOpen, setIsOpen] = useState(false);

  const { address, isConnected, connector } = useAccount();
  const { connectors, connect, isPending } = useConnect();
  const { disconnect } = useDisconnect();
  const { data: balance } = useBalance({ address });
  const chainId = useChainId();
  const { switchChain, isPending: isSwitching } = useSwitchChain();
  const connectorOptions = getConnectorOptions(connectors);
  const { isAuthenticated, status } = useAuth();
  const walletStatus = resolveWalletConnectionStatus({
    authStatus: status,
    isAuthenticated,
    isConnected,
    isConnecting: isPending,
    connectors,
    chainId,
  });

  // SSR 骨架屏
  if (!mounted) {
    return (
      <div className="h-10 w-32 animate-pulse rounded-lg bg-gray-200 dark:bg-gray-700" />
    );
  }

  // 已连接状态
  if (isConnected && address) {
    const currentChain = supportedChains.find((c) => c.id === chainId);

    return (
      <div className="relative">
        <button
          onClick={() => setIsOpen(!isOpen)}
          className="flex items-center gap-2 rounded-lg bg-gray-100 px-4 py-2 text-sm font-medium text-gray-900 hover:bg-gray-200 dark:bg-gray-800 dark:text-white dark:hover:bg-gray-700"
        >
          {/* 网络指示器 */}
          <span
            className={`h-2 w-2 rounded-full ${
              currentChain ? 'bg-green-500' : 'bg-red-500'
            }`}
          />
          {/* 余额 */}
          <span className="font-mono">
            {formatBalanceLabel(balance)}
          </span>
          {/* 地址 */}
          <span className="font-mono text-gray-500">{formatAddress(address)}</span>
        </button>

        {/* 下拉菜单 */}
        {isOpen && (
          <>
            <div
              className="fixed inset-0 z-10"
              onClick={() => setIsOpen(false)}
            />
            <div className="absolute right-0 z-20 mt-2 w-56 origin-top-right rounded-lg bg-white p-2 shadow-lg ring-1 ring-black ring-opacity-5 dark:bg-gray-800">
              {/* 当前连接信息 */}
              <div className="border-b border-gray-100 px-3 py-2 dark:border-gray-700">
                <p className="text-xs text-gray-500">
                  {t('connectedWith', { connector: connector?.name || t('unknownWallet') })}
                </p>
                <p className="mt-1 text-[11px] uppercase tracking-[0.16em] text-gray-500">
                  {t(`status.${getWalletStatusMessageKey(walletStatus)}`)}
                </p>
                <p className="mt-1 font-mono text-sm">{formatAddress(address)}</p>
              </div>

              {/* 网络切换 */}
              <div className="border-b border-gray-100 py-2 dark:border-gray-700">
                <p className="px-3 text-xs text-gray-500">{t('switchNetwork')}</p>
                <div className="mt-1 space-y-1">
                  {supportedChains.map((chain) => (
                    <button
                      key={chain.id}
                      onClick={() => switchChain({ chainId: chain.id })}
                      disabled={chain.id === chainId || isSwitching}
                      className={`flex w-full items-center gap-2 rounded px-3 py-1.5 text-sm ${
                        chain.id === chainId
                          ? 'bg-green-50 text-green-700 dark:bg-green-900/20 dark:text-green-400'
                          : 'hover:bg-gray-100 dark:hover:bg-gray-700'
                      }`}
                    >
                      <span
                        className={`h-2 w-2 rounded-full ${
                          chain.id === chainId ? 'bg-green-500' : 'bg-gray-300'
                        }`}
                      />
                      {chain.name}
                    </button>
                  ))}
                </div>
              </div>

              {/* 断开连接 */}
              <button
                onClick={() => {
                  disconnect();
                  setIsOpen(false);
                }}
                className="mt-2 flex w-full items-center gap-2 rounded px-3 py-2 text-sm text-red-600 hover:bg-red-50 dark:text-red-400 dark:hover:bg-red-900/20"
              >
                <svg className="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                  <path
                    strokeLinecap="round"
                    strokeLinejoin="round"
                    strokeWidth={2}
                    d="M17 16l4-4m0 0l-4-4m4 4H7m6 4v1a3 3 0 01-3 3H6a3 3 0 01-3-3V7a3 3 0 013-3h4a3 3 0 013 3v1"
                  />
                </svg>
                {t('disconnect')}
              </button>
            </div>
          </>
        )}
      </div>
    );
  }

  // 未连接状态
  return (
    <div className="relative">
      <button
        onClick={() => setIsOpen(!isOpen)}
        disabled={isPending}
        className="rounded-lg bg-blue-600 px-4 py-2 text-sm font-medium text-white hover:bg-blue-700 disabled:opacity-50"
      >
        {isPending ? t('connecting') : t('connectWallet')}
      </button>

      {/* 连接器选择 */}
      {isOpen && (
        <>
          <div
            className="fixed inset-0 z-10"
            onClick={() => setIsOpen(false)}
          />
          <div className="absolute right-0 z-20 mt-2 w-56 origin-top-right rounded-lg bg-white p-2 shadow-lg ring-1 ring-black ring-opacity-5 dark:bg-gray-800">
            <p className="px-3 py-2 text-xs text-gray-500">{t('chooseWallet')}</p>
            {connectorOptions.map(({ connector, icon, meta, badge }) => (
              <button
                key={connector.uid}
                onClick={() => {
                  connect({ connector });
                  setIsOpen(false);
                }}
                className="flex w-full items-center gap-3 rounded px-3 py-2 text-sm hover:bg-gray-100 dark:hover:bg-gray-700"
              >
                {/* 钱包图标 */}
                <span className="flex h-8 w-8 items-center justify-center rounded-full bg-gray-100 dark:bg-gray-700">
                  {icon}
                </span>
                <span className="min-w-0 flex-1 text-left">
                  <span className="block truncate">{connector.name}</span>
                  <span className="block truncate text-[11px] text-gray-500">{meta}</span>
                </span>
                <span className="rounded-full border border-gray-200 px-2 py-0.5 text-[10px] uppercase tracking-[0.12em] text-gray-500 dark:border-gray-700">
                  {badge}
                </span>
              </button>
            ))}
          </div>
        </>
      )}
    </div>
  );
}
