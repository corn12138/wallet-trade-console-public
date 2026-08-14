'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import { useTranslations } from 'next-intl';
import { useChainId, useWaitForTransactionReceipt, useWriteContract } from 'wagmi';
import { erc20Abi } from '@/lib/web3/contracts';
import { usePersistTransactionLifecycle } from '@/hooks/web3/usePersistTransactionLifecycle';
import { buildTransactionExplorerUrl } from '@/lib/web3/explorer';
import { useApp } from '../AppContext';
import { shortAddr } from './assetUtils';

export interface RevokeTarget {
  tokenAddress: `0x${string}`;
  spender: `0x${string}`;
  chainId: number;
}

interface UseRevokeApprovalParams {
  fromAddress?: `0x${string}`;
  /** Fired once, after the revoke receipt confirms — refetch server state. */
  onConfirmed?: () => void;
}

interface UseRevokeApprovalResult {
  /** Submit a real wallet-signed `approve(spender, 0)` revoke transaction. */
  revoke: (target: RevokeTarget) => void;
  /** Stable key of the approval whose revoke is in flight (token-spender). */
  pendingKey: string | null;
  /** True while a wallet prompt / mined-tx wait is in progress. */
  isRevoking: boolean;
}

function keyFor(target: { tokenAddress: string; spender: string }) {
  return `${target.tokenAddress}-${target.spender}`.toLowerCase();
}

// wagmi types chainId against the configured chains union; the revoke only
// ever proceeds on a supported, wallet-matched chain (guarded in `revoke`).
type SupportedChainId = 1 | 31337 | 11155111;

/**
 * Wallet-signed ERC20 approval revoke.
 *
 * Revoking is exactly `approve(spender, 0)`, so this submits a real Sepolia
 * (or connected-chain) transaction with `useWriteContract` — it is NOT a
 * passive "build-tx then show summary" flow. The lifecycle is:
 *   1. mismatched wallet chain → prompt a network switch, submit nothing;
 *   2. submit → tx modal `submit`;
 *   3. wallet returns a hash → modal `pending` + hash + explorer link;
 *   4. receipt confirms → modal `confirmed`, persist receipt, fire onConfirmed
 *      so approvals / alerts / activity / product-status refetch from the API;
 *   5. rejection / failure → precise error in the modal, no local mutation.
 */
export function useRevokeApproval({
  fromAddress,
  onConfirmed,
}: UseRevokeApprovalParams): UseRevokeApprovalResult {
  const app = useApp();
  const t = useTranslations('security');
  const walletChainId = useChainId();
  const [active, setActive] = useState<RevokeTarget | null>(null);
  const confirmedHashRef = useRef<string | null>(null);

  const { writeContract, data: hash, isPending, error: writeError, reset } = useWriteContract();

  const {
    isLoading: isConfirming,
    isSuccess,
    data: receipt,
    error: receiptError,
  } = useWaitForTransactionReceipt({
    chainId: active?.chainId as SupportedChainId | undefined,
    hash,
  });

  usePersistTransactionLifecycle({
    chainId: active?.chainId,
    hash,
    fromAddress,
    toAddress: active?.tokenAddress,
    contractAddress: active?.tokenAddress,
    txType: 'revoke-approval',
    metadata: active ? { spender: active.spender, allowance: '0' } : null,
    receipt,
    enabled: Boolean(fromAddress && hash && active),
  });

  const summaryFor = useCallback(
    (target: RevokeTarget) =>
      t('txRevokeSummary', {
        spender: shortAddr(target.spender),
        token: shortAddr(target.tokenAddress),
        chainId: target.chainId,
      }),
    [t],
  );

  const revoke = useCallback(
    (target: RevokeTarget) => {
      if (!fromAddress) {
        app.openConnect();
        return;
      }
      // Unsupported / mismatched chain → surface a switch action instead of
      // silently signing on the wrong network.
      if (walletChainId !== target.chainId) {
        app.toast(t('toastSwitchChain', { chainId: target.chainId }), 'warn');
        app.openChain();
        return;
      }

      reset();
      confirmedHashRef.current = null;
      setActive(target);
      app.setModal({
        kind: 'tx',
        props: { stage: 'submit', title: t('txRevokeTitle'), summary: summaryFor(target) },
      });

      writeContract(
        {
          chainId: target.chainId,
          address: target.tokenAddress,
          abi: erc20Abi,
          functionName: 'approve',
          args: [target.spender, BigInt(0)],
        },
        {
          onError: (e) => {
            app.setModal({
              kind: 'tx',
              props: {
                stage: 'submit',
                title: t('txRevokeTitle'),
                summary: summaryFor(target),
                error: e?.message || t('toastRevokeFailed'),
              },
            });
            app.toast(e?.message || t('toastRevokeFailed'), 'err');
            setActive(null);
          },
        },
      );
    },
    [app, fromAddress, reset, summaryFor, t, walletChainId, writeContract],
  );

  // Advance the tx modal to `pending` once the wallet returns a hash.
  useEffect(() => {
    if (!active || !hash || isSuccess) return;
    app.setModal({
      kind: 'tx',
      props: {
        stage: 'pending',
        title: t('txRevokeTitle'),
        summary: summaryFor(active),
        hash,
        explorerUrl: buildTransactionExplorerUrl(hash, active.chainId) ?? undefined,
      },
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hash, isConfirming]);

  // Receipt confirmed → confirmed modal + refetch. Runs once per hash.
  useEffect(() => {
    if (!active || !hash || !isSuccess) return;
    if (confirmedHashRef.current === hash) return;
    confirmedHashRef.current = hash;

    const succeeded = receipt?.status === 'success';
    app.setModal({
      kind: 'tx',
      props: {
        stage: succeeded ? 'confirmed' : 'pending',
        title: t('txRevokeTitle'),
        summary: succeeded ? t('txRevokeConfirmed') : t('txRevokeReverted'),
        hash,
        explorerUrl: buildTransactionExplorerUrl(hash, active.chainId) ?? undefined,
        error: succeeded ? undefined : t('txRevokeReverted'),
      },
    });
    if (succeeded) {
      app.toast(t('toastRevokeConfirmed'), 'ok');
      onConfirmed?.();
    } else {
      app.toast(t('txRevokeReverted'), 'err');
    }
    setActive(null);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [isSuccess, hash, receipt?.status]);

  // Receipt-wait failure (dropped / replaced) → precise error, no mutation.
  useEffect(() => {
    if (!active || !receiptError) return;
    app.setModal({
      kind: 'tx',
      props: {
        stage: 'pending',
        title: t('txRevokeTitle'),
        summary: summaryFor(active),
        hash,
        explorerUrl: hash ? buildTransactionExplorerUrl(hash, active.chainId) ?? undefined : undefined,
        error: receiptError.message || t('toastRevokeFailed'),
      },
    });
    app.toast(receiptError.message || t('toastRevokeFailed'), 'err');
    setActive(null);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [receiptError]);

  return {
    revoke,
    pendingKey: active ? keyFor(active) : null,
    isRevoking: Boolean(active) && (isPending || isConfirming),
  };
}

export { keyFor as revokeKeyFor };
