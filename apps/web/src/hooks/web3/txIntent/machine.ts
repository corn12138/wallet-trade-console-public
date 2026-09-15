import type { TxErrorCode, TxInvalidationReason, TxLifecycleState, TxPhase } from './types';
import { TERMINAL_STATES } from './types';

/** Everything that can happen to an intent. */
export type TxEvent =
  | { type: 'REVIEW' }
  | { type: 'REVIEW_OK' }
  | { type: 'SEND' }
  | { type: 'WALLET_REJECTED' }
  | { type: 'SUBMITTED'; hash: `0x${string}` }
  | { type: 'RECEIPT_SUCCESS'; confirmations: number; expectsIndexing: boolean }
  | { type: 'RECEIPT_REVERTED' }
  | { type: 'REPLACED'; hash: `0x${string}` }
  | { type: 'INDEXED' }
  | { type: 'INDEX_TIMEOUT' }
  | { type: 'INVALIDATE'; reason: TxInvalidationReason }
  | { type: 'EXPIRE' }
  | { type: 'FAIL'; code: TxErrorCode }
  | { type: 'REORG' }
  | { type: 'RESET' };

export type IndexingOutcome = 'not-expected' | 'pending' | 'indexed' | 'timed-out';

export interface MachineState {
  readonly state: TxLifecycleState;
  readonly hash: `0x${string}` | null;
  readonly replacedByHash: `0x${string}` | null;
  readonly confirmations: number;
  readonly errorCode: TxErrorCode | null;
  readonly invalidationReason: TxInvalidationReason | null;
  readonly indexing: IndexingOutcome;
}

export const INITIAL_MACHINE_STATE: MachineState = {
  state: 'DRAFT',
  hash: null,
  replacedByHash: null,
  confirmations: 0,
  errorCode: null,
  invalidationReason: null,
  indexing: 'not-expected',
};

const PRE_BROADCAST: ReadonlySet<TxLifecycleState> = new Set(['DRAFT', 'REVIEWING', 'READY_FOR_SIGNATURE', 'AWAITING_WALLET']);
const ON_CHAIN: ReadonlySet<TxLifecycleState> = new Set(['SUBMITTED', 'CONFIRMING', 'INDEXED', 'FINALIZED']);

/**
 * The transition function. Pure, total, and strict: an event that does not
 * apply to the current state is ignored rather than corrupting it, so a late
 * receipt callback cannot resurrect a rejected intent.
 */
export function transition(current: MachineState, event: TxEvent): MachineState {
  const s = current.state;
  switch (event.type) {
    case 'RESET':
      return INITIAL_MACHINE_STATE;

    case 'REVIEW':
      return s === 'DRAFT' ? { ...INITIAL_MACHINE_STATE, state: 'REVIEWING' } : current;

    case 'REVIEW_OK':
      return s === 'REVIEWING' ? { ...current, state: 'READY_FOR_SIGNATURE' } : current;

    case 'SEND':
      return s === 'READY_FOR_SIGNATURE' ? { ...current, state: 'AWAITING_WALLET' } : current;

    case 'WALLET_REJECTED':
      return s === 'AWAITING_WALLET' ? { ...current, state: 'REJECTED_BY_WALLET', errorCode: 'REJECTED_BY_WALLET' } : current;

    case 'SUBMITTED':
      return s === 'AWAITING_WALLET' ? { ...current, state: 'SUBMITTED', hash: event.hash } : current;

    case 'RECEIPT_SUCCESS': {
      if (s !== 'SUBMITTED' && s !== 'CONFIRMING') return current;
      // Confirmed is not indexed. Where the product does not expect to index
      // this transaction at all, the receipt is the end of the story.
      if (!event.expectsIndexing) {
        return { ...current, state: 'FINALIZED', confirmations: event.confirmations, indexing: 'not-expected' };
      }
      return { ...current, state: 'CONFIRMING', confirmations: event.confirmations, indexing: 'pending' };
    }

    case 'RECEIPT_REVERTED':
      return s === 'SUBMITTED' || s === 'CONFIRMING' ? { ...current, state: 'REVERTED', errorCode: 'REVERTED' } : current;

    case 'REPLACED':
      return s === 'SUBMITTED' || s === 'CONFIRMING' ? { ...current, state: 'REPLACED', replacedByHash: event.hash } : current;

    case 'INDEXED':
      return s === 'CONFIRMING' && current.indexing === 'pending'
        ? { ...current, state: 'FINALIZED', indexing: 'indexed' }
        : current;

    case 'INDEX_TIMEOUT':
      // The transaction IS final on chain; only the product's view of it is
      // late. Finalize honestly and let the UI say "not yet visible here".
      return s === 'CONFIRMING' && current.indexing === 'pending'
        ? { ...current, state: 'FINALIZED', indexing: 'timed-out' }
        : current;

    case 'INVALIDATE':
      // Only a draft can be invalidated. Once the wallet has been asked, the
      // outcome is whatever the wallet and the chain say.
      return PRE_BROADCAST.has(s) && s !== 'AWAITING_WALLET'
        ? { ...INITIAL_MACHINE_STATE, invalidationReason: event.reason }
        : current;

    case 'EXPIRE':
      return PRE_BROADCAST.has(s) && s !== 'AWAITING_WALLET'
        ? { ...current, state: 'EXPIRED', errorCode: 'INTENT_EXPIRED' }
        : current;

    case 'FAIL':
      return TERMINAL_STATES.has(s) ? current : { ...current, state: 'FAILED', errorCode: event.code };

    case 'REORG':
      // Accepted from any on-chain state (contract §WP1.1.6). WP2 produces it.
      return ON_CHAIN.has(s) ? { ...current, state: 'REORGED', errorCode: null } : current;

    default:
      return current;
  }
}

/** Collapse a lifecycle state onto what the modal can express. */
export function phaseOf(m: MachineState): TxPhase {
  switch (m.state) {
    case 'DRAFT':
    case 'REVIEWING':
    case 'READY_FOR_SIGNATURE':
      return 'review';
    case 'AWAITING_WALLET':
      return 'wallet';
    case 'SUBMITTED':
      return 'pending';
    case 'CONFIRMING':
      return m.indexing === 'pending' ? 'indexing' : 'pending';
    case 'INDEXED':
    case 'FINALIZED':
      return 'done';
    case 'REJECTED_BY_WALLET':
    case 'REVERTED':
    case 'REPLACED':
    case 'DROPPED':
    case 'EXPIRED':
    case 'REORGED':
    case 'FAILED':
      return 'failed';
  }
}
