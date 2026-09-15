import type { Abi } from 'viem';

/**
 * The shared transaction lifecycle (contract §WP1.1).
 *
 * `DRAFT` .. `AWAITING_WALLET` are client-side and pre-broadcast. `SUBMITTED`
 * onward is observed from the chain and the product's own reads. The terminal
 * branches are deliberately distinct from each other: a wallet rejection, a
 * revert, a replacement and a reorg are different facts and the user is owed
 * the right one.
 *
 * `DROPPED` is representable but nothing in this slice produces it: wagmi/viem
 * report replacements (`onReplaced`) but not drops, and detecting a drop needs
 * the sender nonce plus a poll — a separate RPC path recorded as out of reach.
 */
export type TxLifecycleState =
  | 'DRAFT'
  | 'REVIEWING'
  | 'READY_FOR_SIGNATURE'
  | 'AWAITING_WALLET'
  | 'SUBMITTED'
  | 'CONFIRMING'
  | 'INDEXED'
  | 'FINALIZED'
  | 'REJECTED_BY_WALLET'
  | 'REVERTED'
  | 'REPLACED'
  | 'DROPPED'
  | 'EXPIRED'
  | 'REORGED'
  | 'FAILED';

export const TERMINAL_STATES: ReadonlySet<TxLifecycleState> = new Set<TxLifecycleState>([
  'FINALIZED',
  'REJECTED_BY_WALLET',
  'REVERTED',
  'REPLACED',
  'DROPPED',
  'EXPIRED',
  'REORGED',
  'FAILED',
]);

/** States in which the CTA must be disabled: a second prompt would be a second transaction. */
export const BUSY_STATES: ReadonlySet<TxLifecycleState> = new Set<TxLifecycleState>([
  'REVIEWING',
  'READY_FOR_SIGNATURE',
  'AWAITING_WALLET',
  'SUBMITTED',
  'CONFIRMING',
]);

/**
 * The modal can express six things a human cares about. Fifteen lifecycle
 * states collapse onto them; the collapse is explicit so a new state can never
 * fall through to "still submitting" with no way out.
 */
export type TxPhase = 'review' | 'wallet' | 'pending' | 'indexing' | 'done' | 'failed';

/** Stable, public error codes. The engine emits these; components translate them. */
export type TxErrorCode =
  | 'REJECTED_BY_WALLET'
  | 'INSUFFICIENT_FUNDS'
  | 'REVERTED'
  | 'DEADLINE_EXPIRED'
  | 'SLIPPAGE_EXCEEDED'
  | 'INTENT_DRIFTED'
  | 'INTENT_EXPIRED'
  | 'WRONG_CHAIN'
  | 'CHAIN_MISMATCH'
  | 'PRICE_UNAVAILABLE'
  | 'UNKNOWN';

/** Why a frozen intent was invalidated. Rendered by components, never by the engine. */
export type TxInvalidationReason =
  | 'ACCOUNT_CHANGED'
  | 'CHAIN_CHANGED'
  | 'CONNECTOR_CHANGED'
  | 'VIEWER_CHANGED'
  | 'ALLOWANCE_BELOW_REQUIRED'
  | 'QUOTE_OUT_OF_BAND'
  | 'DEADLINE_TOO_CLOSE'
  | 'DRIFT_AT_SEND';

/**
 * The facts the user is shown before signing (contract §WP1.1.3). Everything
 * here is derived from the frozen step, so it cannot describe a different
 * transaction than the one signed.
 */
export interface TxReviewFacts {
  readonly owner: `0x${string}`;
  readonly targetChainId: number;
  readonly target: `0x${string}`;
  readonly calldataHash: string;
  readonly value: bigint;
  readonly token?: {
    readonly address: `0x${string}`;
    readonly symbol: string;
    readonly decimals: number;
    readonly amount: bigint;
  };
  readonly guard?: {
    readonly slippageBps?: number;
    readonly minAmountOut?: bigint;
    /** Display metadata for minAmountOut; not part of what the chain checks. */
    readonly outToken?: { readonly symbol: string; readonly decimals: number };
    readonly acceptablePrice?: bigint;
    readonly deadline?: bigint;
  };
}

/**
 * One transaction the wallet will be asked to sign. Fully frozen: the calldata
 * is encoded once here and those exact bytes are what gets sent.
 */
export interface TxStep {
  readonly stepId: string;
  readonly kind: 'approve' | 'action';
  /** Route-level label the UI maps to copy, e.g. 'swap' or 'open-position'. */
  readonly actionType: string;
  readonly owner: `0x${string}`;
  readonly targetChainId: number;
  readonly target: `0x${string}`;
  readonly calldata: `0x${string}`;
  readonly value: bigint;
  readonly review: TxReviewFacts;
  /**
   * Whether the product's indexer is expected to project this transaction. An
   * ERC-20 approve is not watched, so for it a confirmed receipt is terminal and
   * no indexing step is ever shown.
   */
  readonly expectsIndexing: boolean;
  /** ms epoch. The engine refuses to open the wallet at or after this. */
  readonly expiresAt: number;
  /** Canonical serialization of every field that matters. Equal ⇔ same request. */
  readonly fingerprint: string;
  /** Metadata persisted with the report; opaque to the engine. */
  readonly metadata: Record<string, unknown> | null;
}

/** What `freezeStep` needs to build a step. `args` must already be fully resolved. */
export interface FreezeStepInput {
  readonly kind: TxStep['kind'];
  readonly actionType: string;
  readonly owner: `0x${string}`;
  readonly targetChainId: number;
  readonly target: `0x${string}`;
  readonly abi: Abi;
  readonly functionName: string;
  readonly args: readonly unknown[];
  readonly value?: bigint;
  readonly token?: TxReviewFacts['token'];
  readonly guard?: TxReviewFacts['guard'];
  readonly expectsIndexing: boolean;
  readonly metadata?: Record<string, unknown> | null;
  /** ms epoch of "now"; injected so tests are deterministic. */
  readonly now: number;
}

/** The engine's observable snapshot. Everything user-facing is a code. */
export interface TxIntentSnapshot {
  readonly state: TxLifecycleState;
  readonly phase: TxPhase;
  readonly steps: readonly TxStep[];
  readonly activeStepIndex: number;
  readonly activeStep: TxStep | null;
  readonly hash: `0x${string}` | null;
  readonly replacedByHash: `0x${string}` | null;
  readonly confirmations: number;
  readonly errorCode: TxErrorCode | null;
  readonly invalidationReason: TxInvalidationReason | null;
  /** True once every step has reached a success state. */
  readonly complete: boolean;
}

export const INTENT_EXPIRY_MARGIN_MS = 60_000;
