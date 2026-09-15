'use client';

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { useAccount, useChainId, useSendTransaction, useWaitForTransactionReceipt } from 'wagmi';
import type { TransactionReceipt } from 'viem';
import { getEventsByTxHash } from '@/lib/api/events';
import { usePersistTransactionLifecycle } from '../usePersistTransactionLifecycle';
import { decodeTxError } from './errors';
import { allowanceBelowRequired, hasDrifted, IntentDeadlineTooCloseError, isStepExpired, quoteOutOfBand } from './intent';
import { INITIAL_MACHINE_STATE, phaseOf, transition } from './machine';
import type { MachineState, TxEvent } from './machine';
import type { TxErrorCode, TxIntentSnapshot, TxStep } from './types';
import { BUSY_STATES, TERMINAL_STATES } from './types';

export interface UseTxIntentOptions {
  /**
   * Freeze the ordered steps from CURRENT inputs. Called at review and again at
   * send, so a drift between the two is observable. Return null while the
   * inputs are not ready. May throw IntentDeadlineTooCloseError.
   */
  build: (now: number) => TxStep[] | null;
  /** Where the transaction must execute. Frozen into every step. */
  targetChainId: number;
  /** Identity of the authenticated viewer; a change invalidates a draft. */
  viewerKey?: string | null;
  /** Live allowance for the action step's spender; invalidates only when it falls below the frozen amount. */
  liveAllowance?: bigint;
  /** Live minimum-received for a quote-guarded action; invalidates only when it falls below the frozen guard. */
  liveMinAmountOut?: bigint;
  /** How long to wait for the product's own read to show the transaction. */
  indexingTimeoutMs?: number;
  /** Polling cadence for the indexed check. */
  indexingPollMs?: number;
  onFinalized?: (step: TxStep, hash: `0x${string}`, receipt: TransactionReceipt) => void;
}

export interface UseTxIntentResult extends TxIntentSnapshot {
  /** Freeze the current inputs. Returns the error code that blocked it, or null. */
  review: () => TxErrorCode | null;
  /** Re-check for drift and expiry, then hand the FROZEN bytes to the wallet. */
  send: () => Promise<void>;
  reset: () => void;
  /** True while a second prompt would be a second transaction. */
  busy: boolean;
  /** The live wallet chain differs from the frozen target; the page decides how to switch. */
  wrongChain: boolean;
  receipt: TransactionReceipt | undefined;
  /** The raw error for a collapsed technical line; never rendered directly. */
  rawError: unknown;
  indexing: MachineState['indexing'];
  now: () => number;
}

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
    build,
    targetChainId,
    viewerKey = null,
    liveAllowance,
    liveMinAmountOut,
    indexingTimeoutMs = DEFAULT_INDEXING_TIMEOUT_MS,
    indexingPollMs = DEFAULT_INDEXING_POLL_MS,
    onFinalized,
  } = options;

  const { address, connector } = useAccount();
  const walletChainId = useChainId();
  const { mutateAsync: sendRaw } = useSendTransaction();

  const [machine, setMachine] = useState<MachineState>(INITIAL_MACHINE_STATE);
  const [steps, setSteps] = useState<readonly TxStep[]>([]);
  const [rawError, setRawError] = useState<unknown>(null);
  const activeStep = steps[0] ?? null;

  // Refs so callbacks fired by wagmi (which may be stale closures) always act
  // on the current machine and never resurrect a settled intent.
  const machineRef = useRef(machine);
  machineRef.current = machine;
  const dispatch = useCallback((event: TxEvent) => {
    setMachine((current) => transition(current, event));
  }, []);

  // The identity the step was frozen under. A change while still a draft is
  // an invalidation, never a silent mutation.
  const frozenIdentity = useRef<{ address?: string; connectorId?: string; viewerKey: string | null } | null>(null);
  // The timestamp the step was frozen at. The send-time drift candidate is
  // rebuilt at THIS time, not at "now": a deadline derived from the clock would
  // otherwise differ on every send and read as drift. Time itself is judged by
  // expiry, not by the fingerprint.
  const frozenAt = useRef<number>(0);

  const review = useCallback((): TxErrorCode | null => {
    const now = Date.now();
    let built: TxStep[] | null;
    try {
      built = build(now);
    } catch (error) {
      if (error instanceof IntentDeadlineTooCloseError) return 'DEADLINE_EXPIRED';
      setRawError(error);
      return decodeTxError(error);
    }
    if (!built || built.length === 0) return 'UNKNOWN';
    if (!address) return 'UNKNOWN';
    if (built[0].owner !== address.toLowerCase()) return 'INTENT_DRIFTED';

    frozenIdentity.current = { address: address.toLowerCase(), connectorId: connector?.id, viewerKey };
    frozenAt.current = now;
    setRawError(null);
    setSteps(built);
    setMachine(transition(transition(INITIAL_MACHINE_STATE, { type: 'REVIEW' }), { type: 'REVIEW_OK' }));
    return null;
  }, [address, build, connector?.id, viewerKey]);

  const reset = useCallback(() => {
    frozenIdentity.current = null;
    setSteps([]);
    setRawError(null);
    setMachine(INITIAL_MACHINE_STATE);
  }, []);

  const send = useCallback(async () => {
    const step = steps[0];
    const current = machineRef.current;
    if (!step || current.state !== 'READY_FOR_SIGNATURE') return;

    const now = Date.now();
    if (isStepExpired(step, now)) {
      dispatch({ type: 'EXPIRE' });
      return;
    }
    // The drift check with teeth: the candidate is rebuilt from LIVE inputs
    // and must be the same request. Comparing the frozen calldata to a
    // re-encoding of the frozen args would be a tautology (ADR 0008).
    let candidate: TxStep[] | null = null;
    try {
      candidate = build(frozenAt.current);
    } catch {
      candidate = null;
    }
    if (!candidate || !candidate[0] || hasDrifted(step, candidate[0])) {
      dispatch({ type: 'INVALIDATE', reason: 'DRIFT_AT_SEND' });
      return;
    }
    if (!address || address.toLowerCase() !== step.owner) {
      dispatch({ type: 'INVALIDATE', reason: 'ACCOUNT_CHANGED' });
      return;
    }
    if (walletChainId !== step.targetChainId) {
      // Not a discard: the page offers the switch and the user retries.
      setRawError(null);
      dispatch({ type: 'FAIL', code: 'WRONG_CHAIN' });
      return;
    }

    dispatch({ type: 'SEND' });
    try {
      const hash = await sendRaw({
        to: step.target,
        data: step.calldata,
        value: step.value,
        chainId: step.targetChainId,
      });
      dispatch({ type: 'SUBMITTED', hash });
    } catch (error) {
      setRawError(error);
      const code = decodeTxError(error);
      dispatch(code === 'REJECTED_BY_WALLET' ? { type: 'WALLET_REJECTED' } : { type: 'FAIL', code });
    }
  }, [address, build, dispatch, sendRaw, steps, walletChainId]);

  // --- Invalidation (contract §WP1.1.5) — drafts only; the machine ignores it after SEND.
  useEffect(() => {
    const frozen = frozenIdentity.current;
    if (!frozen || !activeStep) return;
    if (TERMINAL_STATES.has(machine.state) || machine.state === 'DRAFT') return;
    if ((address?.toLowerCase() ?? undefined) !== frozen.address) {
      dispatch({ type: 'INVALIDATE', reason: 'ACCOUNT_CHANGED' });
    } else if (connector?.id !== frozen.connectorId) {
      dispatch({ type: 'INVALIDATE', reason: 'CONNECTOR_CHANGED' });
    } else if (viewerKey !== frozen.viewerKey) {
      dispatch({ type: 'INVALIDATE', reason: 'VIEWER_CHANGED' });
    }
  }, [address, connector?.id, viewerKey, activeStep, machine.state, dispatch]);

  useEffect(() => {
    if (!activeStep || activeStep.kind !== 'action') return;
    if (TERMINAL_STATES.has(machine.state) || machine.state === 'DRAFT') return;
    const required = activeStep.review.token?.amount;
    if (required !== undefined && allowanceBelowRequired(liveAllowance, required)) {
      dispatch({ type: 'INVALIDATE', reason: 'ALLOWANCE_BELOW_REQUIRED' });
      return;
    }
    if (quoteOutOfBand(activeStep.review.guard?.minAmountOut, liveMinAmountOut)) {
      dispatch({ type: 'INVALIDATE', reason: 'QUOTE_OUT_OF_BAND' });
    }
  }, [activeStep, liveAllowance, liveMinAmountOut, machine.state, dispatch]);

  // Expiry: fire strictly before the on-chain deadline can lapse.
  useEffect(() => {
    if (!activeStep || !Number.isFinite(activeStep.expiresAt)) return;
    if (machine.state !== 'REVIEWING' && machine.state !== 'READY_FOR_SIGNATURE') return;
    const delay = Math.max(0, activeStep.expiresAt - Date.now());
    const timer = setTimeout(() => dispatch({ type: 'EXPIRE' }), delay);
    return () => clearTimeout(timer);
  }, [activeStep, machine.state, dispatch]);

  // --- Chain observation.
  const {
    data: receipt,
    isError: receiptFailed,
    error: receiptError,
  } = useWaitForTransactionReceipt({
    hash: machine.hash ?? undefined,
    // wagmi types this to the configured chain union; the step's chain was
    // validated against the wallet at send() and is one of those chains.
    chainId: activeStep?.targetChainId as never,
    onReplaced: (replacement) => {
      dispatch({ type: 'REPLACED', hash: replacement.transaction.hash });
    },
  });

  useEffect(() => {
    if (!receipt || !activeStep) return;
    if (receipt.status === 'reverted') {
      dispatch({ type: 'RECEIPT_REVERTED' });
      return;
    }
    dispatch({ type: 'RECEIPT_SUCCESS', confirmations: 1, expectsIndexing: activeStep.expectsIndexing });
  }, [receipt, activeStep, dispatch]);

  useEffect(() => {
    if (!receiptFailed || !receiptError) return;
    setRawError(receiptError);
    dispatch({ type: 'FAIL', code: decodeTxError(receiptError) });
  }, [receiptFailed, receiptError, dispatch]);

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
    if (!waitingForIndex) return;
    if ((indexedQuery.data?.length ?? 0) > 0) dispatch({ type: 'INDEXED' });
  }, [waitingForIndex, indexedQuery.data, dispatch]);
  useEffect(() => {
    if (!waitingForIndex) return;
    const timer = setTimeout(() => dispatch({ type: 'INDEX_TIMEOUT' }), indexingTimeoutMs);
    return () => clearTimeout(timer);
  }, [waitingForIndex, indexingTimeoutMs, dispatch]);

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
    if (machine.state !== 'FINALIZED' || !activeStep || !machine.hash || !receipt) return;
    if (finalizedFor.current === machine.hash) return;
    finalizedFor.current = machine.hash;
    onFinalized?.(activeStep, machine.hash, receipt);
  }, [machine.state, machine.hash, activeStep, receipt, onFinalized]);

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
    busy: BUSY_STATES.has(machine.state),
    wrongChain: Boolean(activeStep && walletChainId !== activeStep.targetChainId),
    receipt,
    rawError,
    indexing: machine.indexing,
    now: Date.now,
  };
}
