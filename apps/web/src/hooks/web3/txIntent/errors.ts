import type { TxErrorCode } from './types';

/**
 * Decode a wallet/viem/provider error into a stable public code. The engine
 * never surfaces the raw message: provider errors embed RPC URLs (and with
 * them API keys) and are English-only. Components translate the code.
 *
 * Matching is structural first (viem error names / EIP-1193 codes) and only
 * then textual, and the textual patterns are for revert reasons this product's
 * own contracts emit.
 */
export function decodeTxError(error: unknown): TxErrorCode {
  if (!error || typeof error !== 'object') return 'UNKNOWN';
  const err = error as { name?: string; code?: number; shortMessage?: string; message?: string; cause?: unknown };

  if (isUserRejection(err)) return 'REJECTED_BY_WALLET';

  const haystack = `${err.name ?? ''} ${err.shortMessage ?? ''} ${err.message ?? ''}`;
  if (/InsufficientFunds|insufficient funds/i.test(haystack)) return 'INSUFFICIENT_FUNDS';
  if (/ChainMismatch|chain mismatch|wrong chain/i.test(haystack)) return 'CHAIN_MISMATCH';
  if (/EXPIRED|deadline/i.test(haystack)) return 'DEADLINE_EXPIRED';
  if (/INSUFFICIENT_OUTPUT_AMOUNT|slippage|PRICE_TOO_HIGH|PRICE_TOO_LOW/i.test(haystack)) return 'SLIPPAGE_EXCEEDED';
  if (/ContractFunctionRevertedError|ContractFunctionExecutionError|reverted|execution reverted/i.test(haystack)) return 'REVERTED';

  if (err.cause && err.cause !== error) {
    const inner = decodeTxError(err.cause);
    if (inner !== 'UNKNOWN') return inner;
  }
  return 'UNKNOWN';
}

function isUserRejection(err: { name?: string; code?: number; cause?: unknown; message?: string }): boolean {
  if (err.name === 'UserRejectedRequestError' || err.name === 'TransactionRejectedRpcError') return true;
  // EIP-1193 4001 = user rejected. Some connectors surface it on the cause.
  if (err.code === 4001) return true;
  const cause = err.cause as { code?: number; name?: string } | undefined;
  if (cause && (cause.code === 4001 || cause.name === 'UserRejectedRequestError')) return true;
  return /user rejected|user denied|rejected the request/i.test(err.message ?? '');
}

/**
 * A short, non-secret technical hint for a collapsed "details" line. Strips
 * anything URL-shaped so a provider endpoint (which carries the API key in its
 * path) can never reach the screen.
 */
export function technicalHint(error: unknown, max = 160): string | null {
  if (!error || typeof error !== 'object') return null;
  const err = error as { shortMessage?: string; message?: string };
  const raw = (err.shortMessage || err.message || '').split('\n')[0];
  if (!raw) return null;
  const scrubbed = raw.replace(/https?:\/\/\S+/g, '[url]').replace(/wss?:\/\/\S+/g, '[url]');
  return scrubbed.length > max ? `${scrubbed.slice(0, max)}…` : scrubbed;
}
