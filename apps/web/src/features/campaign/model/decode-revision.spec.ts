import { describe, expect, it } from 'vitest';
import fixture from '../../../../../../packages/shared/contracts/fixtures/campaign-v2-revision.json';
import cases from '../../../../../../packages/shared/contracts/fixtures/campaign-v2-cases.json';
import { decodeCampaignRevision } from './decode-revision';

function valueFixture(): Record<string, unknown> {
  return JSON.parse(JSON.stringify(fixture));
}

describe('campaign revision wire and display contract', () => {
  it('accepts the Go fixture and separates the returned snapshot', () => {
    const raw = valueFixture();
    const result = decodeCampaignRevision(raw);
    expect(result.supported).toBe(true);
    if (!result.supported) throw new Error('fixture rejected');
    (raw.displayContent as Record<string, unknown>).sortOrder = 99;
    expect(result.revision.displayContent.sortOrder).toBe(10);
  });

  for (const item of cases) {
    it(item.name, () => {
      const input = valueFixture();
      let target = input;
      for (const segment of item.path.slice(0, -1)) target = target[segment] as Record<string, unknown>;
      const last = item.path[item.path.length - 1];
      if (item.remove) delete target[last];
      else target[last] = item.value;
      expect(decodeCampaignRevision(input)).toEqual({ supported: false, reason: item.reason });
    });
  }

  it('counts astral Unicode as code points and rejects lone surrogates', () => {
    const input = JSON.parse(JSON.stringify(fixture));
    input.materialTerms.copy.en.title = '🙂'.repeat(120);
    expect(decodeCampaignRevision(input).supported).toBe(true);
    input.materialTerms.copy.en.title += 'x';
    expect(decodeCampaignRevision(input).supported).toBe(false);
    input.materialTerms.copy.en.title = '\ud800';
    expect(decodeCampaignRevision(input).supported).toBe(false);
  });

  it.each([null, [], false, {}, { ...fixture, displayContent: null }])('fails closed on malformed payload %j', (value) => {
    expect(decodeCampaignRevision(value).supported).toBe(false);
  });
});
