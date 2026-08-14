import type { Connector } from 'wagmi';
import { formatEther } from 'viem';
import type { WalletAuthStatus } from '@/lib/web3/auth.types';
import { supportedChains } from '@/lib/web3/config';

export type ConnectButtonVariant = 'default' | 'hero' | 'compact';
export type WalletConnectionStatus = WalletAuthStatus;

export interface WalletConnectorOption {
  connector: Connector;
  icon: string;
  name: string;
  meta: string;
  badge: string;
}

export function formatAddress(address: string) {
  return `${address.slice(0, 6)}...${address.slice(-4)}`;
}

type ConnectorMetadata = Connector & {
  icon?: string;
  rdns?: string;
};

export function getConnectorIcon(connectorName: string) {
  if (connectorName === 'MetaMask') return '🦊';
  if (connectorName === 'Rabby Wallet') return '🐰';
  if (connectorName === 'OKX Wallet') return '🟦';
  if (connectorName === 'WalletConnect') return '🔗';
  if (connectorName === 'Injected') return '💉';
  if (connectorName === 'Coinbase Wallet') return '💰';
  return '👛';
}

export function formatBalanceLabel(balance?: { value: bigint; symbol: string }) {
  if (!balance) {
    return '...';
  }

  return `${parseFloat(formatEther(balance.value)).toFixed(4)} ${balance.symbol}`;
}

export function getConnectorOptions(connectors: readonly Connector[]) {
  return connectors.map((connector) => {
    const metadata = connector as ConnectorMetadata;
    const discoveryMethod = metadata.rdns ? 'EIP-6963' : 'Injected';

    return {
      connector,
      icon: getConnectorIcon(connector.name),
      name: connector.name,
      meta: metadata.rdns || connector.id,
      badge: discoveryMethod,
    } satisfies WalletConnectorOption;
  });
}

export function resolveWalletConnectionStatus(input: {
  authStatus: WalletAuthStatus;
  isAuthenticated: boolean;
  isConnected: boolean;
  isConnecting: boolean;
  connectors: readonly Connector[];
  chainId?: number;
}) {
  if (input.isConnecting) {
    return 'connecting' as const;
  }

  if (!input.isConnected) {
    return input.connectors.length === 0 ? 'discovering' : 'idle';
  }

  const isSupportedChain = input.chainId
    ? supportedChains.some((chain) => chain.id === input.chainId)
    : false;

  if (input.chainId && !isSupportedChain && !input.isAuthenticated) {
    return 'wrong-chain' as const;
  }

  return input.authStatus;
}

export type WalletStatusMessageKey =
  | 'discovering'
  | 'connecting'
  | 'wrongChain'
  | 'signing'
  | 'verifying'
  | 'authenticated'
  | 'error'
  | 'ready';

/**
 * Semantic status key for the `web3Connect.status.*` messages. This util is
 * locale-agnostic on purpose: components translate the key via next-intl so
 * wallet-menu copy follows the active locale instead of hardcoded English.
 */
export function getWalletStatusMessageKey(status: WalletConnectionStatus): WalletStatusMessageKey {
  switch (status) {
    case 'discovering':
      return 'discovering';
    case 'connecting':
      return 'connecting';
    case 'wrong-chain':
      return 'wrongChain';
    case 'signing':
      return 'signing';
    case 'verifying':
      return 'verifying';
    case 'authenticated':
      return 'authenticated';
    case 'error':
      return 'error';
    default:
      return 'ready';
  }
}

export function getPrimaryButtonClasses(variant: ConnectButtonVariant = 'default') {
  if (variant === 'hero') {
    return 'flex min-h-14 w-full items-center justify-center gap-3 rounded-xl bg-[linear-gradient(135deg,#c3f5ff_0%,#00daf3_100%)] px-5 py-4 text-base font-bold text-[#00363d] shadow-[0_14px_44px_rgba(0,229,255,0.16)] transition-all hover:brightness-105 disabled:opacity-50';
  }

  if (variant === 'compact') {
    return 'inline-flex min-h-10 items-center justify-center gap-2 rounded-md bg-[linear-gradient(135deg,#c3f5ff_0%,#00daf3_100%)] px-3 py-2 text-xs font-bold uppercase tracking-[0.18em] text-[#00363d] transition-all hover:brightness-105 disabled:opacity-50';
  }

  return 'command-button-primary disabled:opacity-50';
}

export function getNeutralButtonClasses(variant: ConnectButtonVariant = 'default') {
  if (variant === 'hero') {
    return 'flex min-h-14 w-full items-center justify-between gap-3 rounded-xl border border-[rgba(59,73,76,0.18)] bg-[rgba(30,32,35,0.96)] px-4 py-4 text-sm text-[#e2e2e6] transition-colors hover:border-[rgba(0,229,255,0.22)] hover:bg-[rgba(40,42,45,0.92)]';
  }

  if (variant === 'compact') {
    return 'inline-flex min-h-10 items-center justify-between gap-2 rounded-md border border-[rgba(59,73,76,0.16)] bg-[rgba(30,32,35,0.92)] px-3 py-2 text-xs text-[#e2e2e6] transition-colors hover:border-[rgba(0,229,255,0.22)] hover:bg-[rgba(40,42,45,0.88)]';
  }

  return 'command-button';
}

export function getAddressPillClasses(variant: ConnectButtonVariant = 'default') {
  if (variant === 'hero') {
    return 'rounded-xl border border-white/8 bg-white/[0.03] px-4 py-3 font-mono text-sm text-slate-300';
  }

  if (variant === 'compact') {
    return 'rounded-md border border-white/8 bg-white/[0.03] px-2 py-1 font-mono text-xs text-slate-300';
  }

  return 'rounded-xl border border-white/8 bg-white/[0.03] px-3 py-2 font-mono text-sm text-slate-300';
}
