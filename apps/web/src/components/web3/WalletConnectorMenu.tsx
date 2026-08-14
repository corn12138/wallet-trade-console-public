'use client';

import { useTranslations } from 'next-intl';
import type { Connector } from 'wagmi';
import {
  getConnectorOptions,
  getPrimaryButtonClasses,
  getWalletStatusMessageKey,
  type ConnectButtonVariant,
  type WalletConnectionStatus,
} from './connect-button.utils';

interface WalletConnectorMenuProps {
  isOpen: boolean;
  isConnecting: boolean;
  connectors: readonly Connector[];
  status: WalletConnectionStatus;
  variant?: ConnectButtonVariant;
  onToggle: () => void;
  onClose: () => void;
  onConnect: (connector: Connector) => void;
}

export function WalletConnectorMenu({
  isOpen,
  isConnecting,
  connectors,
  status,
  variant = 'default',
  onToggle,
  onClose,
  onConnect,
}: WalletConnectorMenuProps) {
  const t = useTranslations('web3Connect');
  return (
    <div className="relative">
      <button
        onClick={onToggle}
        disabled={isConnecting}
        className={getPrimaryButtonClasses(variant)}
      >
        {isConnecting
          ? t('connecting')
          : status === 'discovering'
            ? t('discoveringWallets')
            : t('connectWallet')}
      </button>

      {isOpen ? (
        <>
          <div className="fixed inset-0 z-10" onClick={onClose} />
          <div className="command-menu absolute right-0 z-20 mt-2 w-60 origin-top-right p-2">
            <div className="border-b border-white/8 px-3 py-2">
              <p className="text-xs text-slate-500">{t('chooseWallet')}</p>
              <p className="mt-1 text-[11px] uppercase tracking-[0.18em] text-cyan-100/80">
                {t(`status.${getWalletStatusMessageKey(status)}`)}
              </p>
            </div>
            {getConnectorOptions(connectors).map(({ connector, icon, name, meta, badge }) => (
              <button
                key={connector.uid}
                onClick={() => onConnect(connector)}
                className="flex w-full items-center gap-3 rounded-xl px-3 py-2 text-sm text-slate-200 hover:bg-white/5"
              >
                <span className="flex h-8 w-8 items-center justify-center rounded-full bg-white/6">
                  {icon}
                </span>
                <span className="min-w-0 flex-1 text-left">
                  <span className="block truncate">{name}</span>
                  <span className="block truncate text-[11px] text-slate-500">{meta}</span>
                </span>
                <span className="rounded-full border border-white/8 px-2 py-0.5 text-[10px] uppercase tracking-[0.12em] text-slate-400">
                  {badge}
                </span>
              </button>
            ))}
          </div>
        </>
      ) : null}
    </div>
  );
}
