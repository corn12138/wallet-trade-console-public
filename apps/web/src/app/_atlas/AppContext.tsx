'use client';
import React, { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslations } from 'next-intl';
import { useAccount, useChainId, useConnect, useDisconnect, useSwitchChain } from 'wagmi';
import { useAuth, supportedChains as web3SupportedChains } from '@/lib/web3';
import { getSiwePromptKey, shouldAutoOpenSiweModal } from './siwe-modal-state';

import type { WalletState, WalletInfo, ToastKind, ToastItem, ModalState } from '@/lib/ui/shell-contract';
export type { WalletState, WalletInfo, ToastKind, ToastItem, ModalState, ModalKind } from '@/lib/ui/shell-contract';

export type AppCtx = {
  // Wallet — bridged from real wagmi + SIWE
  walletState: WalletState;
  wallet: WalletInfo;
  chainId: number;
  openConnect: () => void;
  openChain: () => void;
  closeModal: () => void;
  pickWallet: (opt: { id?: string }) => void;
  signSiwe: () => void;
  disconnect: () => void;
  switchChain: (c: { id: number; name: string }) => void;
  // UI
  drawerOpen: boolean;
  setDrawerOpen: (v: boolean) => void;
  railOpen: boolean;
  setRailOpen: (v: boolean) => void;
  modal: ModalState;
  setModal: (m: ModalState) => void;
  toast: (msg: string, kind?: ToastKind) => void;
  toasts: ToastItem[];
};

const Ctx = createContext<AppCtx | null>(null);

export function useApp() {
  const v = useContext(Ctx);
  if (!v) throw new Error('AppCtx missing');
  return v;
}

export function AppProvider({ children }: { children: React.ReactNode }) {
  const tToasts = useTranslations('atlasShell.toasts');
  /* ─────────── Real wallet bridge ─────────── */
  const { address, isConnected, status: accountStatus } = useAccount();
  const realChainId = useChainId();
  const auth = useAuth();
  const { connect, connectors, isPending: isConnectPending } = useConnect();
  const { disconnect: wagmiDisconnect } = useDisconnect();
  const { switchChain: wagmiSwitchChain } = useSwitchChain();

  /* ─────────── UI state ─────────── */
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [railOpen, setRailOpen] = useState(false);
  const [modal, setModal] = useState<ModalState>(null);
  const [toasts, setToasts] = useState<ToastItem[]>([]);
  const [dismissedSiwePromptKey, setDismissedSiwePromptKey] = useState<string | null>(null);

  const toastSeqRef = useRef(0);
  const toast = useCallback((msg: string, kind: ToastKind = 'ok') => {
    const id = `toast-${++toastSeqRef.current}`;
    setToasts((t) => [...t, { id, msg, kind }]);
    setTimeout(() => setToasts((t) => t.filter((x) => x.id !== id)), 2800);
  }, []);

  /* ─────────── Derive design walletState from real wagmi + auth ─────────── */
  const supportedIds = useMemo(() => new Set(web3SupportedChains.map((c) => c.id)), []);
  const isWrongNetwork = isConnected && !supportedIds.has(realChainId);

  const walletState: WalletState = useMemo(() => {
    if (auth.isRestoring) return 'connecting';
    if (isWrongNetwork) return 'wrong-network';
    if (accountStatus === 'connecting' || accountStatus === 'reconnecting' || isConnectPending) return 'connecting';
    if (!isConnected) return 'disconnected';
    if (auth.status === 'signing' || auth.isLoading) return 'siwe';
    if (auth.isAuthenticated) return 'connected';
    return 'siwe';
  }, [auth.isRestoring, accountStatus, isConnectPending, isConnected, isWrongNetwork, auth.status, auth.isLoading, auth.isAuthenticated]);

  const wallet: WalletInfo = useMemo(() => {
    if (!isConnected || !address) return null;
    return { address, chainId: realChainId, balance: 0 };
  }, [isConnected, address, realChainId]);

  /* ─────────── Wallet actions exposed to design pages ─────────── */
  const siwePromptKey = useMemo(() => getSiwePromptKey(address, realChainId), [address, realChainId]);
  const dismissSiwePrompt = useCallback(() => {
    if (siwePromptKey) setDismissedSiwePromptKey(siwePromptKey);
  }, [siwePromptKey]);

  const openConnect = useCallback(() => {
    if (auth.isRestoring) return;
    if (isConnected && isWrongNetwork) {
      setModal({ kind: 'chain' });
      return;
    }
    if (isConnected && !auth.isAuthenticated) {
      setDismissedSiwePromptKey(null);
      setModal({ kind: 'siwe' });
      return;
    }
    setModal({ kind: 'wallet' });
  }, [auth.isRestoring, auth.isAuthenticated, isConnected, isWrongNetwork]);
  const openChain = useCallback(() => setModal({ kind: 'chain' }), []);
  const closeModal = useCallback(() => {
    if (modal?.kind === 'siwe') dismissSiwePrompt();
    setModal(null);
  }, [dismissSiwePrompt, modal?.kind]);

  const pickWallet = useCallback(
    (opt: { id?: string }) => {
      // Real connect via the wagmi connectors. We accept any connector or fall back to the first injected one.
      const connector =
        connectors.find((c) => (opt?.id ? c.id === opt.id : false)) ||
        connectors.find((c) => c.id === 'injected') ||
        connectors[0];
      if (!connector) {
        toast(tToasts('noConnector'), 'err');
        setModal(null);
        return;
      }
      setDismissedSiwePromptKey(null);
      setModal(null);
      connect(
        { connector },
        {
          onError: (e) => toast(e.message || tToasts('connectionFailed'), 'err'),
        },
      );
    },
    [connect, connectors, toast, tToasts],
  );

  const signSiwe = useCallback(async () => {
    dismissSiwePrompt();
    setModal(null);
    try {
      await auth.signIn();
    } catch (e: any) {
      toast(e?.message || tToasts('siweFailed'), 'err');
    }
  }, [auth, dismissSiwePrompt, toast, tToasts]);

  // Once connected but not yet authenticated, open the SIWE modal automatically
  // so the design wallet flow continues to drive the user to sign.
  useEffect(() => {
    if (shouldAutoOpenSiweModal({
      isConnected,
      isAuthenticated: auth.isAuthenticated,
      isLoading: auth.isLoading,
      isRestoring: auth.isRestoring,
      walletState,
      modal,
      promptKey: siwePromptKey,
      dismissedPromptKey: dismissedSiwePromptKey,
    })) {
      setModal({ kind: 'siwe' });
    }
  }, [isConnected, auth.isAuthenticated, auth.isLoading, auth.isRestoring, walletState, modal, siwePromptKey, dismissedSiwePromptKey]);

  useEffect(() => {
    const authenticatedPromptKey = getSiwePromptKey(auth.authenticatedAddress ?? undefined, auth.authenticatedChainId ?? undefined);
    if (walletState !== 'connected' || !auth.isAuthenticated || auth.isLoading || auth.isRestoring
      || !siwePromptKey || authenticatedPromptKey !== siwePromptKey) return;
    // Session restoration can finish without signSiwe. Only its current-identity
    // prompt is obsolete; authentication must never dismiss another workflow.
    setModal((current) => current?.kind === 'siwe' ? null : current);
  }, [walletState, auth.isAuthenticated, auth.isLoading, auth.isRestoring, auth.authenticatedAddress, auth.authenticatedChainId, siwePromptKey, modal?.kind]);

  useEffect(() => {
    if (!isConnected) {
      setDismissedSiwePromptKey(null);
    }
  }, [isConnected]);

  // Toast on successful SIWE so the design's wallet-connected UX still fires.
  const lastAuthRef = React.useRef<boolean>(false);
  useEffect(() => {
    if (auth.isAuthenticated && !lastAuthRef.current && address) {
      toast(tToasts('walletConnected', { address: `${address.slice(0, 6)}…${address.slice(-4)}` }), 'ok');
    }
    lastAuthRef.current = auth.isAuthenticated;
  }, [auth.isAuthenticated, address, toast, tToasts]);

  const disconnect = useCallback(() => {
    setDismissedSiwePromptKey(null);
    auth.signOut();
    wagmiDisconnect();
    toast(tToasts('walletDisconnected'), 'warn');
  }, [auth, wagmiDisconnect, toast, tToasts]);

  // Report what the server said about the last sign-out. Local sign-out has
  // already happened; an unconfirmed answer is never shown as a server logout.
  const lastServerSignOut = useRef(auth.serverSignOut);
  useEffect(() => {
    const result = auth.serverSignOut;
    if (result === lastServerSignOut.current) return;
    lastServerSignOut.current = result;
    if (result === 'revoked' || result === 'already-revoked') toast(tToasts('serverSignOutConfirmed'), 'ok');
    else if (result === 'rejected') toast(tToasts('serverSignOutRejected'), 'ok');
    else if (result === 'unconfirmed') toast(tToasts('serverSignOutUnconfirmed'), 'warn');
  }, [auth.serverSignOut, toast, tToasts]);

  const switchChain = useCallback(
    (c: { id: number; name: string }) => {
      setModal(null);
      wagmiSwitchChain(
        { chainId: c.id as any },
        {
          onSuccess: () => toast(tToasts('switchedTo', { chain: c.name }), 'ok'),
          onError: (e: any) => toast(e?.message || tToasts('chainSwitchFailed'), 'err'),
        },
      );
    },
    [wagmiSwitchChain, toast, tToasts],
  );

  /* ─────────── ESC closes modals ─────────── */
  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      if (e.key === 'Escape') closeModal();
    };
    window.addEventListener('keydown', h);
    return () => window.removeEventListener('keydown', h);
  }, [closeModal]);

  const value: AppCtx = useMemo(
    () => ({
      walletState,
      wallet,
      chainId: realChainId,
      openConnect,
      openChain,
      closeModal,
      pickWallet,
      signSiwe,
      disconnect,
      switchChain,
      drawerOpen,
      setDrawerOpen,
      railOpen,
      setRailOpen,
      modal,
      setModal,
      toast,
      toasts,
    }),
    [walletState, wallet, realChainId, openConnect, openChain, closeModal, pickWallet, signSiwe, disconnect, switchChain, drawerOpen, railOpen, modal, toast, toasts],
  );

  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}
