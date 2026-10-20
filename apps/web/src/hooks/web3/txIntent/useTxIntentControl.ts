import { useCallback, useEffect, useRef, useState } from 'react';
import type { Connector, ResolvedRegister, UseChainIdReturnType, UseSendTransactionReturnType } from 'wagmi';
import { decodeTxError } from './errors';
import { allowanceBelowRequired, hasDrifted, IntentDeadlineTooCloseError, isStepExpired, quoteOutOfBand } from './intent';
import { INITIAL_MACHINE_STATE, transition } from './machine';
import type { MachineState, TxEvent } from './machine';
import type { TxErrorCode, TxInvalidationReason, TxStep } from './types';
import type { UseTxIntentOptions } from './useTxIntent.types';

type Identity = { address?: string; connector?: Connector; connectorId?: string; connectorUid?: string; viewerKey: string | null };
type Wallet = { address?: string; connector?: Connector; walletChainId: UseChainIdReturnType<ResolvedRegister['config']> };
type SendRaw = UseSendTransactionReturnType<ResolvedRegister['config']>['mutateAsync'];

export function hasUnsettledSubmission(machine: MachineState): boolean {
  return machine.state === 'AWAITING_WALLET' || machine.state === 'SUBMITTED' || machine.state === 'CONFIRMING'
    // A receipt read failure does not prove that a broadcast transaction failed.
    || (machine.state === 'FAILED' && machine.hash !== null);
}

function invalidationFor(step: TxStep, frozen: Identity, live: UseTxIntentOptions & Wallet): TxInvalidationReason | null {
  if (live.address?.toLowerCase() !== frozen.address || live.address?.toLowerCase() !== step.owner) return 'ACCOUNT_CHANGED';
  if (live.connector?.id !== frozen.connectorId || live.connector?.uid !== frozen.connectorUid) return 'CONNECTOR_CHANGED';
  if ((live.viewerKey ?? null) !== frozen.viewerKey) return 'VIEWER_CHANGED';
  if (live.targetChainId !== step.targetChainId) return 'CHAIN_CHANGED';
  if (step.kind === 'action') {
    const required = step.review.token?.amount;
    if (required !== undefined && allowanceBelowRequired(live.liveAllowance, required)) return 'ALLOWANCE_BELOW_REQUIRED';
    if (quoteOutOfBand(step.review.guard?.minAmountOut, live.liveMinAmountOut)) return 'QUOTE_OUT_OF_BAND';
  }
  return null;
}

/** Owns the synchronous signing boundary; observers cannot replace an unsettled operation. */
export function useTxIntentControl(options: UseTxIntentOptions, wallet: Wallet, sendRaw: SendRaw) {
  const [machine, setMachine] = useState<MachineState>(INITIAL_MACHINE_STATE);
  const [steps, setSteps] = useState<readonly TxStep[]>([]);
  const [rawError, setRawError] = useState<unknown>(null);
  const machineRef = useRef(machine);
  const stepsRef = useRef(steps);
  const operation = useRef(0);
  const mounted = useRef(true);
  const latest = useRef({ ...options, ...wallet, sendRaw });
  latest.current = { ...options, ...wallet, sendRaw };
  const frozenIdentity = useRef<Identity | null>(null);
  const frozenAt = useRef(0);
  const activeStep = steps[0] ?? null;

  useEffect(() => {
    mounted.current = true;
    return () => { mounted.current = false; operation.current += 1; };
  }, []);

  const replaceMachine = useCallback((next: MachineState) => {
    // React state is committed later; the ref closes the same-tick double-sign gap.
    machineRef.current = next;
    setMachine(next);
  }, []);
  const dispatch = useCallback((event: TxEvent) => {
    if (mounted.current) replaceMachine(transition(machineRef.current, event));
  }, [replaceMachine]);
  const isCurrent = useCallback((version: number, hash?: string | null) =>
    mounted.current && operation.current === version && (hash === undefined || machineRef.current.hash === hash), []);

  const review = useCallback((): TxErrorCode | null => {
    if (!mounted.current || hasUnsettledSubmission(machineRef.current)) return 'UNKNOWN';
    const live = latest.current;
    const now = Date.now();
    let built: TxStep[] | null;
    try {
      built = live.build(now);
    } catch (error) {
      if (error instanceof IntentDeadlineTooCloseError) return 'DEADLINE_EXPIRED';
      setRawError(error);
      return decodeTxError(error);
    }
    if (!built?.length || !live.address) return 'UNKNOWN';
    if (built[0].owner !== live.address.toLowerCase()) return 'INTENT_DRIFTED';
    if (built[0].targetChainId !== live.targetChainId) return 'CHAIN_MISMATCH';

    operation.current += 1;
    frozenIdentity.current = {
      address: live.address.toLowerCase(), connector: live.connector, connectorId: live.connector?.id,
      connectorUid: live.connector?.uid, viewerKey: live.viewerKey ?? null,
    };
    // Rebuild with the review time so a relative deadline is not mistaken for input drift.
    frozenAt.current = now;
    stepsRef.current = built;
    setSteps(built);
    setRawError(null);
    replaceMachine(transition(transition(INITIAL_MACHINE_STATE, { type: 'REVIEW' }), { type: 'REVIEW_OK' }));
    return null;
  }, [replaceMachine]);

  const reset = useCallback(() => {
    if (!mounted.current || hasUnsettledSubmission(machineRef.current)) return;
    operation.current += 1;
    frozenIdentity.current = null;
    stepsRef.current = [];
    setSteps([]);
    setRawError(null);
    replaceMachine(INITIAL_MACHINE_STATE);
  }, [replaceMachine]);

  const send = useCallback(async () => {
    const step = stepsRef.current[0];
    const frozen = frozenIdentity.current;
    if (!mounted.current || !step || !frozen || machineRef.current.state !== 'READY_FOR_SIGNATURE') return;
    // A modal may retain this callback across renders, so every gate reads the latest inputs.
    const live = latest.current;
    const reason = invalidationFor(step, frozen, live);
    if (reason) {
      dispatch({ type: 'INVALIDATE', reason });
      return;
    }
    if (isStepExpired(step, Date.now())) {
      dispatch({ type: 'EXPIRE' });
      return;
    }
    let candidate: TxStep[] | null;
    try { candidate = live.build(frozenAt.current); } catch { candidate = null; }
    const frozenSteps = stepsRef.current;
    // An approval also commits the following action's terms, even when its allowance is unchanged.
    if (!candidate || candidate.length !== frozenSteps.length || frozenSteps.some((frozenStep, index) => hasDrifted(frozenStep, candidate[index]))) {
      dispatch({ type: 'INVALIDATE', reason: 'DRIFT_AT_SEND' });
      return;
    }
    if (live.walletChainId !== step.targetChainId) {
      setRawError(null);
      dispatch({ type: 'FAIL', code: 'WRONG_CHAIN' });
      return;
    }

    const version = operation.current;
    dispatch({ type: 'SEND' });
    try {
      // Wagmi can await provider resolution before sending, so bind the reviewed
      // account and connector instead of resolving whichever wallet is selected later.
      const hash = await live.sendRaw({
        to: step.target, data: step.calldata, value: step.value, chainId: live.walletChainId,
        account: step.owner, connector: frozen.connector,
      });
      if (isCurrent(version)) dispatch({ type: 'SUBMITTED', hash });
    } catch (error) {
      if (!isCurrent(version)) return;
      setRawError(error);
      const code = decodeTxError(error);
      dispatch(code === 'REJECTED_BY_WALLET' ? { type: 'WALLET_REJECTED' } : { type: 'FAIL', code });
    }
  }, [dispatch, isCurrent]);

  useEffect(() => {
    const frozen = frozenIdentity.current;
    if (!frozen || !activeStep || (machine.state !== 'REVIEWING' && machine.state !== 'READY_FOR_SIGNATURE')) return;
    const reason = invalidationFor(activeStep, frozen, latest.current);
    if (reason) dispatch({ type: 'INVALIDATE', reason });
  }, [activeStep, machine.state, wallet.address, wallet.connector?.id, wallet.connector?.uid, options.viewerKey, options.targetChainId,
    options.liveAllowance, options.liveMinAmountOut, dispatch]);

  useEffect(() => {
    if (!activeStep || !Number.isFinite(activeStep.expiresAt)) return;
    if (machine.state !== 'REVIEWING' && machine.state !== 'READY_FOR_SIGNATURE') return;
    const version = operation.current;
    const timer = setTimeout(() => { if (isCurrent(version)) dispatch({ type: 'EXPIRE' }); }, Math.max(0, activeStep.expiresAt - Date.now()));
    return () => clearTimeout(timer);
  }, [activeStep, machine.state, dispatch, isCurrent]);

  return { machine, steps, activeStep, rawError, setRawError, review, send, reset, dispatch,
    operationVersion: operation.current, isCurrent };
}
