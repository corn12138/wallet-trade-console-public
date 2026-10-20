import type { TransactionReceipt } from 'viem';
import type { MachineState } from './machine';
import type { TxErrorCode, TxIntentSnapshot, TxStep } from './types';

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
