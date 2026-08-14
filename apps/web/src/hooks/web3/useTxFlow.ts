'use client';

import { useCallback, useEffect, useRef, useState } from 'react';
import {
  useAccount,
  useChainId,
  useWaitForTransactionReceipt,
  useWriteContract,
} from 'wagmi';
import type { Abi, Address, TransactionReceipt } from 'viem';
import { useApp } from '@/app/_atlas/AppContext';
import { usePersistTransactionLifecycle } from './usePersistTransactionLifecycle';

export type TxStage = 'idle' | 'submitting' | 'pending' | 'confirmed' | 'failed';

export interface UseTxFlowOptions {
  txType: string;
  title: string;
  buildSummary?: (stage: TxStage, ctx: { hash?: `0x${string}` }) => string;
  buildMetadata?: () => Record<string, unknown> | null;
  onConfirmed?: (receipt: TransactionReceipt) => void;
  toastOnSuccess?: string;
}

export interface TxFlowExecuteParams<TAbi extends Abi> {
  address: Address;
  abi: TAbi;
  functionName: string;
  args: readonly unknown[];
  value?: bigint;
  toAddress?: Address | null;
  contractAddress?: Address | null;
}

export interface UseTxFlowResult {
  execute: <TAbi extends Abi>(params: TxFlowExecuteParams<TAbi>) => void;
  reset: () => void;
  stage: TxStage;
  hash: `0x${string}` | undefined;
  receipt: TransactionReceipt | undefined;
  isWorking: boolean;
  error: Error | null;
}

export function useTxFlow({
  txType,
  title,
  buildSummary,
  buildMetadata,
  onConfirmed,
  toastOnSuccess,
}: UseTxFlowOptions): UseTxFlowResult {
  const app = useApp();
  const { address: fromAddress } = useAccount();
  const chainId = useChainId();

  const [stage, setStage] = useState<TxStage>('idle');
  const handledTerminalStageRef = useRef<string | null>(null);
  const lastWriteCtxRef = useRef<{
    toAddress?: Address | null;
    contractAddress?: Address | null;
    value?: bigint;
  }>({});

  const {
    writeContract,
    data: hash,
    isPending: isWritePending,
    error: writeError,
    reset: resetWrite,
  } = useWriteContract();

  const {
    isLoading: isReceiptLoading,
    isSuccess,
    isError: isReceiptError,
    data: receipt,
    error: receiptError,
  } = useWaitForTransactionReceipt({ hash });

  const error = writeError || receiptError || null;

  usePersistTransactionLifecycle({
    chainId,
    hash,
    fromAddress,
    toAddress: lastWriteCtxRef.current.toAddress,
    contractAddress: lastWriteCtxRef.current.contractAddress,
    value: lastWriteCtxRef.current.value?.toString() ?? '0',
    txType,
    metadata: buildMetadata?.() ?? null,
    receipt,
    enabled: Boolean(fromAddress && hash),
  });

  useEffect(() => {
    if (isWritePending) setStage('submitting');
    else if (hash && isReceiptLoading) setStage('pending');
    else if (isSuccess) setStage('confirmed');
    else if (writeError || isReceiptError) setStage('failed');
  }, [isWritePending, hash, isReceiptLoading, isSuccess, writeError, isReceiptError]);

  useEffect(() => {
    if (stage === 'idle') return;
    const summary = buildSummary?.(stage, { hash }) ?? '';
    if (stage === 'submitting') {
      app.setModal({ kind: 'tx', props: { stage: 'submit', title, summary } });
    } else if (stage === 'pending') {
      app.setModal({ kind: 'tx', props: { stage: 'pending', title, summary, hash } });
    } else if (stage === 'confirmed') {
      const handledKey = `confirmed:${receipt?.transactionHash ?? hash ?? 'unknown'}:${receipt?.blockNumber?.toString() ?? 'pending'}`;
      if (handledTerminalStageRef.current === handledKey) return;
      handledTerminalStageRef.current = handledKey;
      app.setModal({ kind: 'tx', props: { stage: 'confirmed', title, summary, hash } });
      app.toast(toastOnSuccess ?? `${title} confirmed`, 'ok');
      if (receipt) onConfirmed?.(receipt);
    } else if (stage === 'failed') {
      const message = (error as { shortMessage?: string } | null)?.shortMessage || error?.message || 'unknown error';
      const handledKey = `failed:${hash ?? 'write'}:${message}`;
      if (handledTerminalStageRef.current === handledKey) return;
      handledTerminalStageRef.current = handledKey;
      app.setModal({ kind: 'tx', props: { stage: 'failed', title, summary, hash, error: message } });
      app.toast(`${title} failed: ${message}`, 'err');
    }
  }, [stage, hash, receipt, error, app, title, buildSummary, onConfirmed, toastOnSuccess]);

  const execute = useCallback(
    <TAbi extends Abi>(params: TxFlowExecuteParams<TAbi>) => {
      lastWriteCtxRef.current = {
        toAddress: params.toAddress ?? params.address,
        contractAddress: params.contractAddress ?? params.address,
        value: params.value,
      };
      handledTerminalStageRef.current = null;
      writeContract({
        address: params.address,
        abi: params.abi,
        functionName: params.functionName,
        args: params.args as readonly unknown[],
        value: params.value,
      } as Parameters<typeof writeContract>[0]);
    },
    [writeContract],
  );

  const reset = useCallback(() => {
    resetWrite();
    setStage('idle');
    handledTerminalStageRef.current = null;
    lastWriteCtxRef.current = {};
  }, [resetWrite]);

  return {
    execute,
    reset,
    stage,
    hash,
    receipt,
    isWorking: stage === 'submitting' || stage === 'pending',
    error,
  };
}
