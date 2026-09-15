import { encodeFunctionData, keccak256 } from 'viem';
import type { FreezeStepInput, TxReviewFacts, TxStep } from './types';
import { INTENT_EXPIRY_MARGIN_MS } from './types';

/**
 * Canonical serialization of a value: bigints as decimal strings, hex strings
 * lowercased, object keys sorted. Two requests are the same request exactly
 * when their serializations are equal.
 *
 * This is deliberately a string, not a hash. A fingerprint here detects DRIFT
 * between what was reviewed and what is about to be sent; it is not a security
 * boundary against an adversary, so collision resistance buys nothing. What a
 * plain string buys is a synchronous comparison on the same tick as the wallet
 * call — an async digest would open a gap between "checked" and "sent".
 */
export function canonicalize(value: unknown): string {
  if (typeof value === 'bigint') return `${value.toString(10)}n`;
  if (typeof value === 'string') return JSON.stringify(/^0x[0-9a-fA-F]+$/.test(value) ? value.toLowerCase() : value);
  if (typeof value === 'number' || typeof value === 'boolean' || value === null) return JSON.stringify(value);
  if (value === undefined) return 'undefined';
  if (Array.isArray(value)) return `[${value.map(canonicalize).join(',')}]`;
  if (typeof value === 'object') {
    const entries = Object.entries(value as Record<string, unknown>)
      .filter(([, v]) => v !== undefined)
      .sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0));
    return `{${entries.map(([k, v]) => `${JSON.stringify(k)}:${canonicalize(v)}`).join(',')}}`;
  }
  return JSON.stringify(String(value));
}

/**
 * Freeze one transaction. The calldata is encoded HERE, once, and the returned
 * bytes are the bytes the engine sends — see ADR 0008 for why re-encoding at
 * send time and comparing would be a tautology.
 *
 * Throws when the deadline is already inside the safety margin: freezing a
 * transaction that must revert is worse than refusing to freeze it.
 */
export function freezeStep(input: FreezeStepInput): TxStep {
  const calldata = encodeFunctionData({
    abi: input.abi,
    functionName: input.functionName,
    args: input.args as never,
  });
  const value = input.value ?? 0n;
  const owner = input.owner.toLowerCase() as `0x${string}`;
  const target = input.target.toLowerCase() as `0x${string}`;

  const expiresAt = expiryFor(input.guard?.deadline, input.now);
  if (expiresAt !== null && expiresAt <= input.now) {
    throw new IntentDeadlineTooCloseError();
  }

  const review: TxReviewFacts = {
    owner,
    targetChainId: input.targetChainId,
    target,
    calldataHash: keccak256(calldata),
    value,
    token: input.token,
    guard: input.guard,
  };

  const fingerprint = canonicalize({
    kind: input.kind,
    actionType: input.actionType,
    owner,
    targetChainId: input.targetChainId,
    target,
    calldata,
    value,
    token: input.token,
    guard: input.guard,
  });

  return {
    stepId: `${input.kind}:${fingerprint.length}:${review.calldataHash.slice(2, 18)}`,
    kind: input.kind,
    actionType: input.actionType,
    owner,
    targetChainId: input.targetChainId,
    target,
    calldata,
    value,
    review,
    expectsIndexing: input.expectsIndexing,
    expiresAt: expiresAt ?? Number.POSITIVE_INFINITY,
    fingerprint,
    metadata: input.metadata ?? null,
  };
}

export class IntentDeadlineTooCloseError extends Error {
  readonly code = 'DEADLINE_TOO_CLOSE' as const;
  constructor() {
    super('the on-chain deadline is inside the safety margin');
    this.name = 'IntentDeadlineTooCloseError';
  }
}

/**
 * When the engine must stop offering to sign. The on-chain deadline is in
 * seconds; the engine's clock is ms. The margin keeps the wallet from being
 * opened on a transaction that could lapse while the user reads the prompt.
 */
export function expiryFor(deadlineSeconds: bigint | undefined, now: number): number | null {
  if (deadlineSeconds === undefined) return null;
  const deadlineMs = Number(deadlineSeconds) * 1000;
  const expiresAt = deadlineMs - INTENT_EXPIRY_MARGIN_MS;
  // A caller that computed the deadline relative to a stale "now" still gets
  // a sane answer: never in the future of the deadline itself.
  return Math.min(expiresAt, deadlineMs);
}

export function isStepExpired(step: TxStep, now: number): boolean {
  return now >= step.expiresAt;
}

/** True when the reviewed request and a freshly rebuilt one are not the same request. */
export function hasDrifted(frozen: TxStep, candidate: TxStep): boolean {
  return frozen.fingerprint !== candidate.fingerprint;
}

/**
 * Materiality predicates (contract §WP1.1.5). These invalidate on VALUES that
 * would make the frozen transaction unsafe, never on identity or fetch
 * timestamps — a quote that refetches every 12 s must not destroy the intent
 * every 12 s.
 */
export function allowanceBelowRequired(allowance: bigint | undefined, required: bigint): boolean {
  if (allowance === undefined) return false;
  return allowance < required;
}

/**
 * A live quote has left the frozen guard band when the amount it would now
 * deliver is below what the frozen calldata insists on — that transaction
 * would revert with INSUFFICIENT_OUTPUT_AMOUNT. A quote that improved leaves
 * the frozen transaction executable, so it does not invalidate.
 */
export function quoteOutOfBand(frozenMinAmountOut: bigint | undefined, liveMinAmountOut: bigint | undefined): boolean {
  if (frozenMinAmountOut === undefined || liveMinAmountOut === undefined) return false;
  return liveMinAmountOut < frozenMinAmountOut;
}
