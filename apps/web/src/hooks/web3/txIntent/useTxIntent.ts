'use client';

import { useEffect, useMemo, useRef } from 'react';
import { useQuery } from '@tanstack/react-query';
import { useAccount, useChainId, useSendTransaction, useWaitForTransactionReceipt } from 'wagmi';
import { getEventsByTxHash } from '@/lib/api/events';
import { usePersistTransactionLifecycle } from '../usePersistTransactionLifecycle';
import { decodeTxError } from './errors';
import { phaseOf } from './machine';
import type { TxIntentSnapshot } from './types';
import type { UseTxIntentOptions, UseTxIntentResult } from './useTxIntent.types';
export type { UseTxIntentOptions, UseTxIntentResult } from './useTxIntent.types';
import { BUSY_STATES } from './types';
import { hasUnsettledSubmission, useTxIntentControl } from './useTxIntentControl';

const DEFAULT_INDEXING_TIMEOUT_MS = 90_000;
const DEFAULT_INDEXING_POLL_MS = 3_000;

/**
 * The shared transaction lifecycle for a route (ADR 0008).
 *
 * The step is frozen at `review()`; `send()` rebuilds a candidate from the
 * live inputs and refuses on any drift, then sends the frozen calldata with
 * `sendTransaction` — one encoding, the reviewed one. Everything user-facing
 * is a code; the page translates it.
 */
export function useTxIntent(options: UseTxIntentOptions): UseTxIntentResult {
  const {
    indexingTimeoutMs = DEFAULT_INDEXING_TIMEOUT_MS,
    indexingPollMs = DEFAULT_INDEXING_POLL_MS,
    onFinalized,
  } = options;

  const { address, connector } = useAccount();
  const walletChainId = useChainId();
  const { mutateAsync: sendRaw } = useSendTransaction();

  const { machine, steps, activeStep, rawError, setRawError, review, send, reset, dispatch, operationVersion, isCurrent } =
    useTxIntentControl(options, { address, connector, walletChainId }, sendRaw);
  const observedHash = machine.hash;

  // --- Chain observation.
  const {
    data: observedReceipt,
    isError: receiptFailed,
    error: receiptError,
  } = useWaitForTransactionReceipt({
    hash: machine.hash ?? undefined,
    // wagmi types this to the configured chain union; the step's chain was
    // validated against the wallet at send() and is one of those chains.
    chainId: activeStep?.targetChainId as never,
    onReplaced: (replacement) => {
      if (isCurrent(operationVersion, observedHash)) {
        setRawError(null);
        dispatch({ type: 'REPLACED', hash: replacement.transaction.hash });
      }
    },
  });

  // Cached receipt data may briefly belong to the prior query key. Do not report
  // or persist it under a newer intent's owner, transaction hash or action type.
  const receipt = observedReceipt?.transactionHash === observedHash ? observedReceipt : undefined;
  useEffect(() => {
    if (!receipt || receiptFailed || !activeStep || !isCurrent(operationVersion, observedHash)) return;
    setRawError(null);
    if (receipt.status === 'reverted') {
      dispatch({ type: 'RECEIPT_REVERTED' });
      return;
    }
    dispatch({ type: 'RECEIPT_SUCCESS', confirmations: 1, expectsIndexing: activeStep.expectsIndexing });
  }, [receipt, receiptFailed, activeStep, dispatch, isCurrent, operationVersion, observedHash, setRawError]);

  useEffect(() => {
    if (!receiptFailed || !receiptError || !isCurrent(operationVersion, observedHash)) return;
    setRawError(receiptError);
    dispatch({ type: 'FAIL', code: decodeTxError(receiptError) });
  }, [receiptFailed, receiptError, dispatch, isCurrent, operationVersion, observedHash, setRawError]);

  // --- Confirmed is not indexed: the product's own read has to show it.
  const waitingForIndex = machine.state === 'CONFIRMING' && machine.indexing === 'pending' && !!machine.hash;
  const indexedQuery = useQuery({
    queryKey: ['tx-intent-indexed', activeStep?.targetChainId, machine.hash],
    queryFn: () => getEventsByTxHash(machine.hash as string, activeStep?.targetChainId),
    enabled: waitingForIndex,
    refetchInterval: waitingForIndex ? indexingPollMs : false,
    retry: false,
  });
  useEffect(() => {
    if (!waitingForIndex || !isCurrent(operationVersion, observedHash)) return;
    if ((indexedQuery.data?.length ?? 0) > 0) dispatch({ type: 'INDEXED' });
  }, [waitingForIndex, indexedQuery.data, dispatch, isCurrent, operationVersion, observedHash]);
  useEffect(() => {
    if (!waitingForIndex) return;
    const timer = setTimeout(() => {
      if (isCurrent(operationVersion, observedHash)) dispatch({ type: 'INDEX_TIMEOUT' });
    }, indexingTimeoutMs);
    return () => clearTimeout(timer);
  }, [waitingForIndex, indexingTimeoutMs, dispatch, isCurrent, operationVersion, observedHash]);

  // --- Report through the existing, unchanged persistence path.
  usePersistTransactionLifecycle({
    chainId: activeStep?.targetChainId,
    hash: machine.hash ?? undefined,
    fromAddress: activeStep?.owner,
    toAddress: activeStep?.target,
    contractAddress: activeStep?.target,
    value: activeStep ? activeStep.value.toString() : '0',
    txType: activeStep?.actionType,
    metadata: activeStep?.metadata ?? null,
    receipt: receipt ?? null,
    enabled: Boolean(activeStep && machine.hash),
  });

  const finalizedFor = useRef<string | null>(null);
  useEffect(() => {
    if (machine.state !== 'FINALIZED' || !activeStep || !machine.hash || !receipt || !isCurrent(operationVersion, observedHash)) return;
    if (finalizedFor.current === machine.hash) return;
    finalizedFor.current = machine.hash;
    onFinalized?.(activeStep, machine.hash, receipt);
  }, [machine.state, machine.hash, activeStep, receipt, onFinalized, isCurrent, operationVersion, observedHash]);

  const snapshot = useMemo<TxIntentSnapshot>(() => ({
    state: machine.state,
    phase: phaseOf(machine),
    steps,
    activeStepIndex: 0,
    activeStep,
    hash: machine.hash,
    replacedByHash: machine.replacedByHash,
    confirmations: machine.confirmations,
    errorCode: machine.errorCode,
    invalidationReason: machine.invalidationReason,
    complete: machine.state === 'FINALIZED',
  }), [machine, steps, activeStep]);

  return {
    ...snapshot,
    review,
    send,
    reset,
    busy: BUSY_STATES.has(machine.state) || hasUnsettledSubmission(machine),
    wrongChain: Boolean(activeStep && walletChainId !== activeStep.targetChainId),
    receipt,
    rawError,
    indexing: machine.indexing,
    now: Date.now,
  };
}
