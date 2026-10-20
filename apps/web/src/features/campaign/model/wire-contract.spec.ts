import { describe, expect, it } from 'vitest';
import cases from '../../../../../../packages/shared/contracts/fixtures/campaign-v2-wire-cases.json';
import type { CampaignContractName } from './contract.generated';
import { decodeCampaignContract, decodeCampaignContractJSON } from './decode-revision';
import { parseCampaignJSON } from './decode-contract-json';

function sample(name: CampaignContractName): string {
  const item = cases.find((candidate) => candidate.name === `valid ${name}`);
  if (!item) throw new Error(`missing shared wire fixture: ${name}`);
  return item.raw;
}

describe('shared campaign JSON wire contract', () => {
  for (const item of cases) {
    it(item.name, () => {
      const result = decodeCampaignContractJSON(item.contract as CampaignContractName, item.raw);
      if (item.reason === 'valid') expect(result.supported).toBe(true);
      else expect(result).toEqual({ supported: false, reason: item.reason });
    });
  }

  it('returns a separate typed response snapshot', () => {
    const source = JSON.parse(sample('QueryDetailResponse'));
    const result = decodeCampaignContract('QueryDetailResponse', source);
    expect(result.supported).toBe(true);
    if (!result.supported) throw new Error('shared fixture rejected');
    source.item.publicationVersion = '7';
    expect(result.value.item.publicationVersion).toBe('1');
  });

  it.each([
    ['CancelInteractionInput', 64 * 1024], ['QueryMeResponse', 1024 * 1024],
  ] as const)('enforces the %s byte budget at the boundary', (name, budget) => {
    const source = sample(name);
    const raw = source + ' '.repeat(budget - new TextEncoder().encode(source).length);
    expect(decodeCampaignContractJSON(name, raw).supported).toBe(true);
    expect(decodeCampaignContractJSON(name, raw + ' ')).toEqual({ supported: false, reason: 'invalid' });
  });

  it('enforces raw JSON nesting independently of a schema shape failure', () => {
    expect(() => parseCampaignJSON('['.repeat(24) + '1' + ']'.repeat(24), 1024)).not.toThrow();
    expect(() => parseCampaignJSON('['.repeat(25) + '1' + ']'.repeat(25), 1024)).toThrow();
  });

  it.each([new Date(), Object.create({}), { toJSON: () => ({}) }])('rejects non-wire object prototypes and functions', (value) => {
    expect(decodeCampaignContract('EmptyParams', value)).toEqual({ supported: false, reason: 'invalid' });
  });

  it('rejects escaped duplicate keys without changing object prototypes', () => {
    expect(() => parseCampaignJSON('{"a":1,"\\u0061":2}', 1024)).toThrow();
    const value = parseCampaignJSON('{"__proto__":{"polluted":true}}', 1024) as object;
    expect(Object.getPrototypeOf(value)).toBeNull();
    expect(decodeCampaignContract('EmptyParams', value).supported).toBe(false);
  });
});
