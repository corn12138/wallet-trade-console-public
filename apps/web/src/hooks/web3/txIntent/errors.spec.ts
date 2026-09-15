import { describe, expect, it } from 'vitest';
import { decodeTxError, technicalHint } from './errors';

describe('decodeTxError — stable codes, never raw provider text', () => {
  it('recognizes a wallet rejection by viem name, by EIP-1193 code, and through a cause', () => {
    expect(decodeTxError({ name: 'UserRejectedRequestError', message: 'x' })).toBe('REJECTED_BY_WALLET');
    expect(decodeTxError({ code: 4001, message: 'x' })).toBe('REJECTED_BY_WALLET');
    expect(decodeTxError({ name: 'TransactionExecutionError', cause: { code: 4001 } })).toBe('REJECTED_BY_WALLET');
    expect(decodeTxError({ message: 'User rejected the request.' })).toBe('REJECTED_BY_WALLET');
  });
  it('maps this product\'s own revert reasons to codes', () => {
    expect(decodeTxError({ message: 'execution reverted: Router: EXPIRED' })).toBe('DEADLINE_EXPIRED');
    expect(decodeTxError({ message: 'execution reverted: INSUFFICIENT_OUTPUT_AMOUNT' })).toBe('SLIPPAGE_EXCEEDED');
    expect(decodeTxError({ message: 'PositionManager: PRICE_TOO_HIGH' })).toBe('SLIPPAGE_EXCEEDED');
    expect(decodeTxError({ name: 'ContractFunctionRevertedError', message: 'reverted' })).toBe('REVERTED');
    expect(decodeTxError({ name: 'InsufficientFundsError', message: 'insufficient funds for gas' })).toBe('INSUFFICIENT_FUNDS');
  });
  it('returns UNKNOWN rather than guessing', () => {
    expect(decodeTxError(new Error('something else entirely'))).toBe('UNKNOWN');
    expect(decodeTxError(null)).toBe('UNKNOWN');
    expect(decodeTxError('string')).toBe('UNKNOWN');
  });
});

describe('technicalHint — never a provider URL', () => {
  it('scrubs http and ws URLs, which carry the provider API key in their path', () => {
    const hint = technicalHint({ message: 'HTTP request failed. URL: https://rpc.example/v3/SECRETKEY status 500\nDetails: more' });
    expect(hint).not.toContain('SECRETKEY');
    expect(hint).toContain('[url]');
    expect(hint).not.toContain('\n');
  });
  it('truncates and prefers shortMessage', () => {
    const hint = technicalHint({ shortMessage: 'a'.repeat(300), message: 'ignored' }, 40);
    expect(hint?.length).toBeLessThanOrEqual(41);
    expect(hint?.startsWith('aaaa')).toBe(true);
  });
  it('returns null when there is nothing to show', () => {
    expect(technicalHint(null)).toBeNull();
    expect(technicalHint({})).toBeNull();
  });
});
