import { describe, expect, it } from 'vitest';
import { INITIAL_MACHINE_STATE, phaseOf, transition } from './machine';
import type { MachineState, TxEvent } from './machine';
import type { TxLifecycleState } from './types';

const HASH = '0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' as const;
const HASH2 = '0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb' as const;

function run(events: TxEvent[], from: MachineState = INITIAL_MACHINE_STATE): MachineState {
  return events.reduce(transition, from);
}
const toAwaiting: TxEvent[] = [{ type: 'REVIEW' }, { type: 'REVIEW_OK' }, { type: 'SEND' }];

describe('happy paths', () => {
  it('an approve (no indexing expected) finalizes on its receipt', () => {
    const m = run([...toAwaiting, { type: 'SUBMITTED', hash: HASH }, { type: 'RECEIPT_SUCCESS', confirmations: 1, expectsIndexing: false }]);
    expect(m.state).toBe('FINALIZED');
    expect(m.indexing).toBe('not-expected');
    expect(m.hash).toBe(HASH);
  });

  it('a swap (indexing expected) stays CONFIRMING after its receipt until the product can show it', () => {
    const confirmed = run([...toAwaiting, { type: 'SUBMITTED', hash: HASH }, { type: 'RECEIPT_SUCCESS', confirmations: 2, expectsIndexing: true }]);
    expect(confirmed.state).toBe('CONFIRMING');
    expect(confirmed.indexing).toBe('pending');
    expect(phaseOf(confirmed)).toBe('indexing');

    const indexed = transition(confirmed, { type: 'INDEXED' });
    expect(indexed.state).toBe('FINALIZED');
    expect(indexed.indexing).toBe('indexed');
  });

  it('finalizes honestly when indexing never arrives, and says so', () => {
    const confirmed = run([...toAwaiting, { type: 'SUBMITTED', hash: HASH }, { type: 'RECEIPT_SUCCESS', confirmations: 2, expectsIndexing: true }]);
    const timedOut = transition(confirmed, { type: 'INDEX_TIMEOUT' });
    expect(timedOut.state).toBe('FINALIZED');
    expect(timedOut.indexing).toBe('timed-out');
  });
});

describe('failure branches are distinct facts', () => {
  it('wallet rejection', () => {
    const m = run([...toAwaiting, { type: 'WALLET_REJECTED' }]);
    expect(m.state).toBe('REJECTED_BY_WALLET');
    expect(m.errorCode).toBe('REJECTED_BY_WALLET');
  });
  it('receipt status 0', () => {
    const m = run([...toAwaiting, { type: 'SUBMITTED', hash: HASH }, { type: 'RECEIPT_REVERTED' }]);
    expect(m.state).toBe('REVERTED');
  });
  it('same-nonce replacement carries the winning hash', () => {
    const m = run([...toAwaiting, { type: 'SUBMITTED', hash: HASH }, { type: 'REPLACED', hash: HASH2 }]);
    expect(m.state).toBe('REPLACED');
    expect(m.hash).toBe(HASH);
    expect(m.replacedByHash).toBe(HASH2);
  });
  it('a later canonicality signal moves any on-chain state to REORGED', () => {
    for (const events of [
      [{ type: 'SUBMITTED', hash: HASH }],
      [{ type: 'SUBMITTED', hash: HASH }, { type: 'RECEIPT_SUCCESS', confirmations: 1, expectsIndexing: true }],
      [{ type: 'SUBMITTED', hash: HASH }, { type: 'RECEIPT_SUCCESS', confirmations: 1, expectsIndexing: false }],
    ] as TxEvent[][]) {
      expect(run([...toAwaiting, ...events, { type: 'REORG' }]).state).toBe('REORGED');
    }
  });
  it('REORG does nothing before broadcast', () => {
    expect(run([{ type: 'REVIEW' }, { type: 'REORG' }]).state).toBe('REVIEWING');
  });
});

describe('invalidation and expiry are pre-broadcast only', () => {
  it('a draft under review is invalidated back to DRAFT with the reason', () => {
    const m = run([{ type: 'REVIEW' }, { type: 'REVIEW_OK' }, { type: 'INVALIDATE', reason: 'ACCOUNT_CHANGED' }]);
    expect(m.state).toBe('DRAFT');
    expect(m.invalidationReason).toBe('ACCOUNT_CHANGED');
  });
  it('once the wallet has been asked, an invalidation is ignored — the wallet decides', () => {
    const m = run([...toAwaiting, { type: 'INVALIDATE', reason: 'CHAIN_CHANGED' }]);
    expect(m.state).toBe('AWAITING_WALLET');
  });
  it('a submitted transaction cannot be invalidated or expired', () => {
    const base = run([...toAwaiting, { type: 'SUBMITTED', hash: HASH }]);
    expect(transition(base, { type: 'INVALIDATE', reason: 'QUOTE_OUT_OF_BAND' }).state).toBe('SUBMITTED');
    expect(transition(base, { type: 'EXPIRE' }).state).toBe('SUBMITTED');
  });
  it('expiry blocks signing', () => {
    expect(run([{ type: 'REVIEW' }, { type: 'REVIEW_OK' }, { type: 'EXPIRE' }]).state).toBe('EXPIRED');
  });
});

describe('strictness — late callbacks cannot corrupt a settled intent', () => {
  it('a receipt arriving after a wallet rejection is ignored', () => {
    const m = run([...toAwaiting, { type: 'WALLET_REJECTED' }, { type: 'SUBMITTED', hash: HASH }, { type: 'RECEIPT_SUCCESS', confirmations: 1, expectsIndexing: false }]);
    expect(m.state).toBe('REJECTED_BY_WALLET');
  });
  it('FAIL does not overwrite a terminal state', () => {
    const m = run([...toAwaiting, { type: 'SUBMITTED', hash: HASH }, { type: 'RECEIPT_REVERTED' }, { type: 'FAIL', code: 'UNKNOWN' }]);
    expect(m.state).toBe('REVERTED');
  });
  it('RESET returns to the initial state from anywhere', () => {
    expect(run([...toAwaiting, { type: 'SUBMITTED', hash: HASH }, { type: 'RESET' }])).toEqual(INITIAL_MACHINE_STATE);
  });
});

describe('phaseOf is exhaustive — no state can fall through to a spinner', () => {
  const ALL: TxLifecycleState[] = [
    'DRAFT', 'REVIEWING', 'READY_FOR_SIGNATURE', 'AWAITING_WALLET', 'SUBMITTED', 'CONFIRMING', 'INDEXED', 'FINALIZED',
    'REJECTED_BY_WALLET', 'REVERTED', 'REPLACED', 'DROPPED', 'EXPIRED', 'REORGED', 'FAILED',
  ];
  it.each(ALL)('%s maps to a phase', (state) => {
    const phase = phaseOf({ ...INITIAL_MACHINE_STATE, state });
    expect(['review', 'wallet', 'pending', 'indexing', 'done', 'failed']).toContain(phase);
  });
  it('every failure state is the failed phase, and every failure state is terminal-looking to the UI', () => {
    for (const state of ['REJECTED_BY_WALLET', 'REVERTED', 'REPLACED', 'DROPPED', 'EXPIRED', 'REORGED', 'FAILED'] as const) {
      expect(phaseOf({ ...INITIAL_MACHINE_STATE, state })).toBe('failed');
    }
  });
});
