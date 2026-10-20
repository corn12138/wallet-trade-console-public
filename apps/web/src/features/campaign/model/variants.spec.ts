import { describe, expect, it } from 'vitest';
import cases from '../../../../../../packages/shared/contracts/fixtures/campaign-v2-variant-cases.json';
import strictClientSchema from '../../../../../../packages/shared/contracts/fixtures/campaign-v2-schema-strict-client-2026-10-09.json';
import type { CampaignContractName, QueryDetailResponse, QueryHistoryResponse, QueryListResponse } from './contract.generated';
import { decodeCampaignContractJSON, decodeCampaignContractJSONUnder } from './decode-revision';
import { CAMPAIGN_HISTORY_VARIANTS, CAMPAIGN_PUBLIC_VARIANTS, validHistoryResponse, validPublicCampaignView } from './variants';

function semantic(contract: string, value: unknown, allowed: readonly string[]): 'valid' | 'invalid' {
  switch (contract) {
    case 'QueryDetailResponse': return validPublicCampaignView((value as QueryDetailResponse).item, allowed) ? 'valid' : 'invalid';
    case 'QueryListResponse': return (value as QueryListResponse).items.every((item) => validPublicCampaignView(item, allowed)) ? 'valid' : 'invalid';
    case 'QueryHistoryResponse': return validHistoryResponse(value as QueryHistoryResponse, allowed) ? 'valid' : 'invalid';
    default: throw new Error(`no semantic check for ${contract}`);
  }
}

// The same corpus runs in Go (internal/campaign/v2/variants_test.go).
describe('shared campaign variant corpus', () => {
  for (const item of cases) {
    it(item.name, () => {
      const decoded = decodeCampaignContractJSON(item.contract as CampaignContractName, item.raw);
      expect(decoded.supported ? 'valid' : decoded.reason).toBe(item.current);
      if (decoded.supported) expect(semantic(item.contract, decoded.value, item.variants)).toBe(item.semantic);
      // A client built before negotiation: only acceptance matters to it.
      expect(decodeCampaignContractJSONUnder(strictClientSchema, item.contract, item.raw).supported ? 'valid' : 'rejected').toBe(item.strictClient);
    });
  }

  it('advertises exactly the variants this client renders', () => {
    expect(CAMPAIGN_PUBLIC_VARIANTS).toEqual(['legacyBaselineV1', 'contentCapabilitiesV1']);
    expect(CAMPAIGN_HISTORY_VARIANTS).toEqual(['legacyImportHistoryV1']);
  });
});
