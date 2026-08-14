import { describe, expect, it } from 'vitest';
import type { Connector } from 'wagmi';
import en from '../../../../../services/api-go/internal/i18n/baseline/en.json';
import zh from '../../../../../services/api-go/internal/i18n/baseline/zh.json';
import {
  getConnectorOptions,
  getWalletStatusMessageKey,
  resolveWalletConnectionStatus,
  type WalletConnectionStatus,
} from './connect-button.utils';

describe('connect-button utils', () => {
  it('maps discovered injected wallets into normalized connector metadata', () => {
    const connectors = [
      {
        uid: 'metamask-1',
        id: 'injected',
        name: 'MetaMask',
        rdns: 'io.metamask',
      },
    ] as unknown as readonly Connector[];

    expect(getConnectorOptions(connectors)).toEqual([
      expect.objectContaining({
        name: 'MetaMask',
        icon: '🦊',
        meta: 'io.metamask',
        badge: 'EIP-6963',
      }),
    ]);
  });

  it('resolves the unified wallet status for discovery, connecting, wrong chain, and authenticated states', () => {
    expect(resolveWalletConnectionStatus({
      authStatus: 'idle',
      isAuthenticated: false,
      isConnected: false,
      isConnecting: false,
      connectors: [],
      chainId: undefined,
    })).toBe('discovering');

    expect(resolveWalletConnectionStatus({
      authStatus: 'idle',
      isAuthenticated: false,
      isConnected: false,
      isConnecting: true,
      connectors: [],
      chainId: undefined,
    })).toBe('connecting');

    expect(resolveWalletConnectionStatus({
      authStatus: 'idle',
      isAuthenticated: false,
      isConnected: true,
      isConnecting: false,
      connectors: [],
      chainId: 8453,
    })).toBe('wrong-chain');

    expect(resolveWalletConnectionStatus({
      authStatus: 'authenticated',
      isAuthenticated: true,
      isConnected: true,
      isConnecting: false,
      connectors: [],
      chainId: 11155111,
    })).toBe('authenticated');
  });

  it('exposes semantic status keys that resolve in both locale files', () => {
    expect(getWalletStatusMessageKey('verifying')).toBe('verifying');
    expect(getWalletStatusMessageKey('wrong-chain')).toBe('wrongChain');
    expect(getWalletStatusMessageKey('idle')).toBe('ready');

    // Every status the resolver can produce must have copy in en AND zh —
    // a missing key would leak a raw message id into the wallet menu.
    const statuses: WalletConnectionStatus[] = [
      'idle',
      'discovering',
      'connecting',
      'wrong-chain',
      'signing',
      'verifying',
      'authenticated',
      'error',
    ];
    for (const status of statuses) {
      const key = getWalletStatusMessageKey(status);
      expect(en.web3Connect.status[key], `en web3Connect.status.${key}`).toBeTruthy();
      expect(zh.web3Connect.status[key], `zh web3Connect.status.${key}`).toBeTruthy();
    }
  });
});
