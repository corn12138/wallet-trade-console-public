import { createElement, useLayoutEffect } from 'react';
import { act, render, screen } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { AppProvider, useApp, type AppCtx, type ModalState } from './AppContext';
import { getSiwePromptKey, shouldAutoOpenSiweModal } from './siwe-modal-state';

const bridge = vi.hoisted(() => ({
  account: { address: '0xAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA', isConnected: true, status: 'connected' },
  chainId: 11155111,
  auth: { isAuthenticated: false, isLoading: false, isRestoring: false, status: 'idle',
    authenticatedAddress: null as string | null, authenticatedChainId: null as number | null,
    signIn: vi.fn(), signOut: vi.fn() },
  connect: vi.fn(), disconnect: vi.fn(), switchChain: vi.fn(), translate: (key: string) => key,
}));
vi.mock('next-intl', () => ({ useTranslations: () => bridge.translate }));
vi.mock('wagmi', () => ({
  useAccount: () => bridge.account, useChainId: () => bridge.chainId,
  useConnect: () => ({ connect: bridge.connect, connectors: [], isPending: false }),
  useDisconnect: () => ({ disconnect: bridge.disconnect }),
  useSwitchChain: () => ({ switchChain: bridge.switchChain }),
}));
vi.mock('@/lib/web3', () => ({ useAuth: () => bridge.auth, supportedChains: [{ id: 11155111 }, { id: 1 }] }));

describe('siwe modal state', () => {
  it('builds a stable prompt key from wallet address and chain', () => {
    expect(getSiwePromptKey('0xABCdef0000000000000000000000000000000001', 11155111)).toBe(
      '0xabcdef0000000000000000000000000000000001:11155111',
    );
  });

  it('auto-opens SIWE for a connected unauthenticated wallet', () => {
    expect(shouldAutoOpenSiweModal({
      isConnected: true,
      isAuthenticated: false,
      isLoading: false,
      walletState: 'siwe',
      modal: null,
      promptKey: '0x1:11155111',
      dismissedPromptKey: null,
    })).toBe(true);
  });

  it('does not immediately reopen after the user dismisses the same SIWE prompt', () => {
    expect(shouldAutoOpenSiweModal({
      isConnected: true,
      isAuthenticated: false,
      isLoading: false,
      walletState: 'siwe',
      modal: null,
      promptKey: '0x1:11155111',
      dismissedPromptKey: '0x1:11155111',
    })).toBe(false);
  });

  it('can auto-open again when the wallet or chain changes', () => {
    expect(shouldAutoOpenSiweModal({
      isConnected: true,
      isAuthenticated: false,
      isLoading: false,
      walletState: 'siwe',
      modal: null,
      promptKey: '0x2:11155111',
      dismissedPromptKey: '0x1:11155111',
    })).toBe(true);
  });
});


it('does not request another signature while the stored session is being restored', () => {
  expect(shouldAutoOpenSiweModal({
    isConnected: true, isAuthenticated: false, isLoading: false, isRestoring: true,
    walletState: 'siwe', modal: null, promptKey: '0x1:11155111', dismissedPromptKey: null,
  })).toBe(false);
});

describe('AppProvider SIWE prompt lifecycle', () => {
  let app: AppCtx;
  function Probe() {
    const value = useApp();
    useLayoutEffect(() => { app = value; }, [value]);
    return createElement('span', { 'data-testid': 'modal' }, value.modal?.kind ?? 'none');
  }
  const tree = () => createElement(AppProvider, null, createElement(Probe));
  function authenticate() {
    Object.assign(bridge.auth, { isAuthenticated: true, isLoading: false, isRestoring: false, status: 'authenticated',
      authenticatedAddress: bridge.account.address.toLowerCase(), authenticatedChainId: bridge.chainId });
  }
  beforeEach(() => {
    bridge.account.address = '0xAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA';
    bridge.account.isConnected = true; bridge.account.status = 'connected'; bridge.chainId = 11155111;
    Object.assign(bridge.auth, { isAuthenticated: false, isLoading: false, isRestoring: false, status: 'idle',
      authenticatedAddress: null, authenticatedChainId: null });
    vi.clearAllMocks();
  });

  it('closes the already-open prompt after the matching stored session finishes restoring', () => {
    const view = render(tree());
    expect(screen.getByTestId('modal').textContent).toBe('siwe');
    Object.assign(bridge.auth, { isLoading: true, isRestoring: true, status: 'connecting' });
    view.rerender(tree());
    expect(screen.getByTestId('modal').textContent).toBe('siwe');
    authenticate(); view.rerender(tree());
    expect(screen.getByTestId('modal').textContent).toBe('none');
    expect(bridge.auth.signIn).not.toHaveBeenCalled();
    // Automatic success must not suppress a future prompt for this same wallet.
    Object.assign(bridge.auth, { isAuthenticated: false, authenticatedAddress: null, authenticatedChainId: null, status: 'idle' });
    view.rerender(tree());
    expect(screen.getByTestId('modal').textContent).toBe('siwe');
  });

  it.each<ModalState>([{ kind: 'chain' }, { kind: 'wallet' }, { kind: 'tx', props: { stage: 'pending' } },
    { kind: 'tokenPicker', props: { tokens: [], onPick: () => undefined } }])('preserves $kind when authentication succeeds', (modal) => {
    const view = render(tree());
    act(() => app.setModal(modal));
    authenticate(); view.rerender(tree());
    expect(app.modal).toBe(modal);
  });

  it('does not dismiss the new wallet prompt for another account or chain session', () => {
    const view = render(tree()); authenticate();
    bridge.account.address = '0xBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB';
    view.rerender(tree());
    expect(screen.getByTestId('modal').textContent).toBe('siwe');
    bridge.auth.authenticatedAddress = bridge.account.address;
    bridge.chainId = 1; view.rerender(tree());
    expect(screen.getByTestId('modal').textContent).toBe('siwe');
    authenticate(); view.rerender(tree());
    expect(screen.getByTestId('modal').textContent).toBe('none');
  });

  it.each(['isLoading', 'isRestoring'] as const)('waits while %s even when a prior verified session exists', (flag) => {
    const view = render(tree()); authenticate(); bridge.auth[flag] = true;
    view.rerender(tree());
    expect(screen.getByTestId('modal').textContent).toBe('siwe');
    bridge.auth[flag] = false; view.rerender(tree());
    expect(screen.getByTestId('modal').textContent).toBe('none');
  });

  it('requires a current connected wallet and verified identity before dismissing', () => {
    const view = render(tree()); bridge.auth.isAuthenticated = true;
    view.rerender(tree());
    expect(screen.getByTestId('modal').textContent).toBe('siwe');
    authenticate(); bridge.account.isConnected = false; bridge.account.status = 'disconnected';
    view.rerender(tree());
    expect(screen.getByTestId('modal').textContent).toBe('siwe');
  });
});
