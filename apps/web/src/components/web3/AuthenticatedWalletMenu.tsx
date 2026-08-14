'use client';

import { useTranslations } from 'next-intl';
import { supportedChains } from '@/lib/web3/config';
import {
  formatAddress,
  getNeutralButtonClasses,
  getWalletStatusMessageKey,
  type ConnectButtonVariant,
  type WalletConnectionStatus,
} from './connect-button.utils';

interface AuthenticatedWalletMenuProps {
  isOpen: boolean;
  address: string;
  connectorName?: string;
  balanceLabel: string;
  chainId: (typeof supportedChains)[number]['id'];
  status: WalletConnectionStatus;
  isSwitching: boolean;
  variant?: ConnectButtonVariant;
  onToggle: () => void;
  onClose: () => void;
  onCopyAddress: () => void;
  onSwitchChain: (chainId: (typeof supportedChains)[number]['id']) => void;
  onSignOut: () => void;
}

export function AuthenticatedWalletMenu({
  isOpen,
  address,
  connectorName,
  balanceLabel,
  chainId,
  status,
  isSwitching,
  variant = 'default',
  onToggle,
  onClose,
  onCopyAddress,
  onSwitchChain,
  onSignOut,
}: AuthenticatedWalletMenuProps) {
  const t = useTranslations('web3Connect');
  return (
    <div className="relative">
      <button
        onClick={onToggle}
        className={getNeutralButtonClasses(variant)}
      >
        <span className="h-2.5 w-2.5 rounded-full bg-cyan-300 shadow-[0_0_10px_rgba(0,229,255,0.65)]" />
        <span className="font-mono">{balanceLabel}</span>
        <span className="font-mono text-slate-400">{formatAddress(address)}</span>
      </button>

      {isOpen ? (
        <>
          <div className="fixed inset-0 z-10" onClick={onClose} />
          <div className="command-menu absolute right-0 z-20 mt-2 w-72 origin-top-right p-2">
            <div className="border-b border-white/8 px-3 py-2">
              <div className="flex items-center gap-2">
                <span className="h-2 w-2 rounded-full bg-cyan-300" />
                <span className="text-xs text-cyan-100">{t(`status.${getWalletStatusMessageKey(status)}`)}</span>
              </div>
              <p className="mt-1 text-xs text-slate-400">
                {t('connectedWith', { connector: connectorName || t('unknownWallet') })}
              </p>
              <button
                onClick={onCopyAddress}
                className="mt-1 flex items-center gap-1 font-mono text-sm text-slate-100 hover:text-cyan-100"
                title={t('clickToCopy')}
              >
                {formatAddress(address)}
                <svg className="h-3 w-3" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                  <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M8 16H6a2 2 0 01-2-2V6a2 2 0 012-2h8a2 2 0 012 2v2m-6 12h8a2 2 0 002-2v-8a2 2 0 00-2-2h-8a2 2 0 00-2 2v8a2 2 0 002 2z" />
                </svg>
              </button>
            </div>

            <div className="border-b border-white/8 py-2">
              <p className="px-3 text-xs text-slate-500">{t('network')}</p>
              <div className="mt-1 space-y-1">
                {supportedChains.map((chain) => (
                  <button
                    key={chain.id}
                    onClick={() => onSwitchChain(chain.id)}
                    disabled={chain.id === chainId || isSwitching}
                    className={`flex w-full items-center gap-2 rounded-xl px-3 py-2 text-sm ${chain.id === chainId
                      ? 'bg-cyan-400/10 text-cyan-100'
                      : 'text-slate-300 hover:bg-white/5'
                      }`}
                  >
                    <span className={`h-2 w-2 rounded-full ${chain.id === chainId ? 'bg-cyan-300' : 'bg-slate-600'}`} />
                    {chain.name}
                  </button>
                ))}
              </div>
            </div>

            <button
              onClick={onSignOut}
              className="mt-2 flex w-full items-center gap-2 rounded-xl px-3 py-2 text-sm text-rose-300 hover:bg-rose-400/10"
            >
              <svg className="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                <path
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  strokeWidth={2}
                  d="M17 16l4-4m0 0l-4-4m4 4H7m6 4v1a3 3 0 01-3 3H6a3 3 0 01-3-3V7a3 3 0 013-3h4a3 3 0 013 3v1"
                />
              </svg>
              {t('signOut')}
            </button>
          </div>
        </>
      ) : null}
    </div>
  );
}
