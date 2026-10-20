import { vi } from 'vitest';
import cases from '../../../../../../packages/shared/contracts/fixtures/campaign-v2-wire-cases.json';
import type { CampaignContractMap, CampaignContractName, QueryPublicCampaignView } from '../model/contract.generated';
import type { CampaignAPI, CampaignSession } from '../model/ports';

export function sample<Name extends CampaignContractName>(name: Name): CampaignContractMap[Name] {
  const raw = cases.find((item) => item.name === `valid ${name}`)?.raw;
  if (!raw) throw new Error(`missing fixture ${name}`);
  return JSON.parse(raw) as CampaignContractMap[Name];
}
export const clock = { schemaVersion: 1 as const, serverNow: '2026-10-08T00:00:00Z', asOf: '2026-10-08T00:00:00Z' };
export const sessionA: CampaignSession = { owner: `0x${'1'.repeat(40)}`, generation: 1, token: 'token-a' };
export const sessionB: CampaignSession = { owner: `0x${'2'.repeat(40)}`, generation: 2, token: 'token-b' };
export function campaign(id = 'campaign-1', version = '1'): QueryPublicCampaignView {
  return { ...sample('QueryPublicCampaignView'), id, publicationVersion: version, nextTransitionAt: null };
}
export function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: unknown) => void;
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}
export function mockAPI() {
  return {
    list: vi.fn<CampaignAPI['list']>().mockResolvedValue({ ...clock, items: [campaign()], nextCursor: null }),
    detail: vi.fn<CampaignAPI['detail']>().mockResolvedValue({ ...clock, item: campaign() }),
    me: vi.fn<CampaignAPI['me']>().mockResolvedValue({ ...sample('QueryMeResponse'), ...clock }),
    history: vi.fn<CampaignAPI['history']>().mockResolvedValue({ ...sample('QueryHistoryResponse'), ...clock }),
    command: vi.fn<CampaignAPI['command']>().mockResolvedValue(sample('CommandResponse')),
    execute: vi.fn<CampaignAPI['execute']>().mockResolvedValue(sample('CommandResponse')),
    create: vi.fn<CampaignAPI['create']>().mockResolvedValue(sample('AdminCommandResponse')),
    revision: vi.fn<CampaignAPI['revision']>().mockResolvedValue(sample('AdminCommandResponse')),
    publication: vi.fn<CampaignAPI['publication']>().mockResolvedValue(sample('AdminCommandResponse')),
    preview: vi.fn<CampaignAPI['preview']>().mockResolvedValue(sample('QueryPreviewResponse')),
    adminCommand: vi.fn<CampaignAPI['adminCommand']>().mockResolvedValue(sample('AdminCommandResponse')),
  };
}
