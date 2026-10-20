import { describe, expect, it } from 'vitest';
import { readSimulationEvidence } from './simulation-evidence';

function snapshot() {
  const observation = {
    observationStatus: 'succeeded', source: 'rpc', pinning: 'hash-pinned',
    observedAt: '2026-10-08T02:00:00Z',
    blockReference: { number: '12345678901234567890', hash: `0x${'a'.repeat(64)}`, pinning: 'hash-pinned' },
  };
  return {
    simulationScope: 'full-envelope', consistentSnapshot: true,
    observations: { call: observation, gas: observation, balance: observation,
      allowance: { ...observation, observationStatus: 'not-required', source: 'not-requested', pinning: 'unavailable', blockReference: undefined },
      feeQuote: { ...observation, pinning: 'latest-only', blockReference: undefined },
    },
  };
}

describe('simulation evidence boundary', () => {
  it('preserves large block numbers and excludes the latest fee from snapshot consistency', () => {
    const result = readSimulationEvidence(snapshot());
    expect(result.consistent).toBe(true);
    expect(result.observations.call?.blockReference?.number).toBe('12345678901234567890');
    expect(result.observations.feeQuote?.pinning).toBe('latest-only');
  });

  it.each([undefined, null, {}, { callSucceeded: true }, { simulationScope: 'future-v3', consistentSnapshot: true }])(
    'does not infer complete simulation from legacy or unknown data: %j', (raw) => {
      expect(readSimulationEvidence(raw).consistent).toBe(false);
      expect(readSimulationEvidence(raw).scope).toBe('unavailable');
    },
  );

  it.each([
    { observationStatus: 'future-state' }, { pinning: 'latest-only', blockReference: undefined },
    { blockReference: { number: '123', hash: `0x${'b'.repeat(64)}`, pinning: 'hash-pinned' } },
    { blockReference: { number: '0x123', hash: `0x${'a'.repeat(64)}`, pinning: 'hash-pinned' } },
    { observedAt: 'invalid' }, { observationStatus: 'reverted' }, { source: 'client' },
  ])('rejects incomplete, mismatched or future evidence even if the server claims consistency: %j', (change) => {
    const raw = snapshot();
    expect(readSimulationEvidence({ ...raw, observations: { ...raw.observations, gas: { ...raw.observations.gas, ...change } } }).consistent).toBe(false);
  });

  it('requires allowance evidence when allowance is relevant', () => {
    const raw = snapshot();
    expect(readSimulationEvidence({ ...raw, observations: { ...raw.observations, allowance: undefined } }).consistent).toBe(false);
  });
});
