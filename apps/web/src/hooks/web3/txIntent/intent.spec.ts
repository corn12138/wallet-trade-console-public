import { describe, expect, it } from 'vitest';
import { encodeFunctionData, erc20Abi, getAddress, keccak256 } from 'viem';
import {
  allowanceBelowRequired,
  canonicalize,
  expiryFor,
  freezeStep,
  hasDrifted,
  IntentDeadlineTooCloseError,
  isStepExpired,
  quoteOutOfBand,
} from './intent';
import { INTENT_EXPIRY_MARGIN_MS } from './types';
import type { FreezeStepInput } from './types';

const OWNER = '0x000000000000000000000000000000000000dead' as const;
const SPENDER = '0x000000000000000000000000000000000000cafe' as const;
const TOKEN = '0x000000000000000000000000000000000000beef' as const;
const NOW = 1_800_000_000_000;

function approveInput(overrides: Partial<FreezeStepInput> = {}): FreezeStepInput {
  return {
    kind: 'approve',
    actionType: 'approve',
    owner: OWNER,
    targetChainId: 11155111,
    target: TOKEN,
    abi: erc20Abi,
    functionName: 'approve',
    args: [SPENDER, 1_000n],
    expectsIndexing: false,
    now: NOW,
    ...overrides,
  };
}

describe('freezeStep — the reviewed bytes are the sent bytes', () => {
  it('encodes the calldata once and exposes exactly those bytes', () => {
    const step = freezeStep(approveInput());
    const expected = encodeFunctionData({ abi: erc20Abi, functionName: 'approve', args: [SPENDER, 1_000n] });
    expect(step.calldata).toBe(expected);
    expect(step.review.calldataHash).toBe(keccak256(expected));
  });

  it('lowercases owner and target so casing can never make two identical requests differ', () => {
    // A checksummed (mixed-case) form of the same address, produced by viem.
    const checksummedOwner = getAddress(OWNER);
    const checksummedToken = getAddress(TOKEN);
    expect(checksummedOwner).not.toBe(OWNER);
    const mixed = freezeStep(approveInput({ owner: checksummedOwner, target: checksummedToken }));
    const lower = freezeStep(approveInput());
    expect(hasDrifted(mixed, lower)).toBe(false);
    expect(mixed.owner).toBe(OWNER);
    expect(mixed.target).toBe(TOKEN);
  });

  it('refuses to freeze a transaction whose deadline is already inside the safety margin', () => {
    const deadlineSeconds = BigInt(Math.floor((NOW + INTENT_EXPIRY_MARGIN_MS / 2) / 1000));
    expect(() => freezeStep(approveInput({ guard: { deadline: deadlineSeconds } }))).toThrow(IntentDeadlineTooCloseError);
  });

  it('freezes a deadline that leaves room, and expires strictly before it', () => {
    const deadlineSeconds = BigInt(Math.floor((NOW + 20 * 60_000) / 1000));
    const step = freezeStep(approveInput({ guard: { deadline: deadlineSeconds } }));
    expect(step.expiresAt).toBe(Number(deadlineSeconds) * 1000 - INTENT_EXPIRY_MARGIN_MS);
    expect(isStepExpired(step, NOW)).toBe(false);
    expect(isStepExpired(step, step.expiresAt)).toBe(true);
    expect(step.expiresAt).toBeLessThan(Number(deadlineSeconds) * 1000);
  });

  it('never expires a step that carries no deadline', () => {
    const step = freezeStep(approveInput());
    expect(isStepExpired(step, Number.MAX_SAFE_INTEGER)).toBe(false);
  });
});

describe('drift — the only comparison that can actually fail', () => {
  it('reports no drift for a byte-identical rebuild', () => {
    expect(hasDrifted(freezeStep(approveInput()), freezeStep(approveInput()))).toBe(false);
  });

  it.each([
    ['amount', { args: [SPENDER, 999n] as const }],
    ['spender', { args: ['0x0000000000000000000000000000000000000001', 1_000n] as const }],
    ['owner', { owner: '0x0000000000000000000000000000000000000002' as const }],
    ['chain', { targetChainId: 84532 }],
    ['target', { target: '0x0000000000000000000000000000000000000003' as const }],
    ['value', { value: 1n }],
    ['guard', { guard: { slippageBps: 50 } }],
  ])('reports drift when the %s changes', (_label, change) => {
    expect(hasDrifted(freezeStep(approveInput()), freezeStep(approveInput(change as Partial<FreezeStepInput>)))).toBe(true);
  });
});

describe('canonicalize', () => {
  it('is key-order insensitive and distinguishes bigint from number', () => {
    expect(canonicalize({ b: 1n, a: 'x' })).toBe(canonicalize({ a: 'x', b: 1n }));
    expect(canonicalize({ n: 1n })).not.toBe(canonicalize({ n: 1 }));
  });
  it('drops undefined so "" and absent are the same request only when both are absent', () => {
    expect(canonicalize({ a: undefined, b: 1 })).toBe(canonicalize({ b: 1 }));
  });
});

describe('materiality predicates — values, never identity', () => {
  it('invalidates only when the allowance falls below the frozen requirement', () => {
    expect(allowanceBelowRequired(999n, 1_000n)).toBe(true);
    expect(allowanceBelowRequired(1_000n, 1_000n)).toBe(false);
    expect(allowanceBelowRequired(5_000n, 1_000n)).toBe(false);
    expect(allowanceBelowRequired(undefined, 1_000n)).toBe(false);
  });

  it('invalidates when the live quote would deliver less than the frozen minimum, not when it improves', () => {
    expect(quoteOutOfBand(100n, 99n)).toBe(true);
    expect(quoteOutOfBand(100n, 100n)).toBe(false);
    expect(quoteOutOfBand(100n, 150n)).toBe(false);
    expect(quoteOutOfBand(undefined, 1n)).toBe(false);
  });
});

describe('expiryFor', () => {
  it('subtracts the margin from the on-chain deadline', () => {
    expect(expiryFor(1_000n, 0)).toBe(1_000_000 - INTENT_EXPIRY_MARGIN_MS);
  });
  it('returns null with no deadline', () => {
    expect(expiryFor(undefined, NOW)).toBeNull();
  });
});
