'use client';

import { useTranslations } from 'next-intl';
import {
  formatAddress,
  getAddressPillClasses,
  getNeutralButtonClasses,
  getPrimaryButtonClasses,
  getWalletStatusMessageKey,
  type ConnectButtonVariant,
  type WalletConnectionStatus,
} from './connect-button.utils';
import type { WalletAuthErrorCode } from '@/lib/web3/auth.types';

interface ConnectButtonPendingAuthProps {
  address: string;
  isAuthLoading: boolean;
  error: string | null;
  errorCode: WalletAuthErrorCode | null;
  status: WalletConnectionStatus;
  variant?: ConnectButtonVariant;
  onSignIn: () => void;
  onDisconnect: () => void;
}

export function ConnectButtonPendingAuth({
  address,
  isAuthLoading,
  error,
  errorCode,
  status,
  variant = 'default',
  onSignIn,
  onDisconnect,
}: ConnectButtonPendingAuthProps) {
  const t = useTranslations('web3Connect');
  const isActionDisabled = isAuthLoading || status === 'wrong-chain';
  const actionLabel = status === 'signing'
    ? t('awaitingSignature')
    : status === 'verifying'
      ? t('verifyingSession')
      : status === 'wrong-chain'
        ? t('wrongNetwork')
        : t('signIn');

  return (
    <div className={`flex ${variant === 'hero' ? 'flex-col items-stretch' : 'items-center'} gap-2`}>
      <span className={getAddressPillClasses(variant)}>
        {formatAddress(address)}
      </span>

      <span className="text-[11px] uppercase tracking-[0.18em] text-slate-500">
        {t(`status.${getWalletStatusMessageKey(status)}`)}
      </span>

      <button
        onClick={onSignIn}
        disabled={isActionDisabled}
        className={getPrimaryButtonClasses(variant)}
      >
        {actionLabel}
      </button>

      <button
        onClick={onDisconnect}
        className={`${getNeutralButtonClasses(variant)} ${variant === 'hero' ? 'justify-center' : 'px-3'}`}
      >
        ✕
      </button>

      {error ? (
        <span className="text-xs text-rose-300">
          {errorCode ? `[${errorCode}] ` : ''}
          {error}
        </span>
      ) : null}
    </div>
  );
}
