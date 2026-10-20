import type { QueryHistoryResponse, QueryHistoryView, QueryPublicCampaignView } from './contract.generated';
import { timestamp } from './read-state';

/** Response representations this client decodes, sent as X-Campaign-Variants
 * (docs/campaign-legacy-presentation-protocol-2026-10-12.md §2). A server that
 * ignores the header answers with the base representation, which stays valid. */
export const CAMPAIGN_VARIANTS_HEADER = 'X-Campaign-Variants';
// contentCapabilitiesV1: this client checks media and trade targets against its
// own registries before offering new admission (localCapabilitiesReady).
export const CAMPAIGN_PUBLIC_VARIANTS = ['legacyBaselineV1', 'contentCapabilitiesV1'] as const;
export const CAMPAIGN_HISTORY_VARIANTS = ['legacyImportHistoryV1'] as const;

function before(start: string, end: string): boolean {
  try { return timestamp(start) < timestamp(end); } catch { return false; }
}

/** The content rules the schema cannot express; mirrors v2.CheckPublicCampaignView. */
export function validPublicCampaignView(item: QueryPublicCampaignView, allowed: readonly string[]): boolean {
  const baseline = Object.hasOwn(item, 'baseline') ? item.baseline : undefined;
  switch (item.contentState) {
    case 'ready':
      return item.content !== null && baseline === undefined
        && (allowed.includes('contentCapabilitiesV1') || item.content.displayContent.mediaAssetId === undefined && item.content.cta.actionKind === 'none');
    case 'unsupported': return item.content === null && baseline === undefined;
    case 'legacyBaseline':
      return allowed.includes('legacyBaselineV1') && item.content === null && baseline !== undefined && before(baseline.startAt, baseline.endAt);
    default: return false;
  }
}

function validHistoryEntry(item: QueryHistoryView): boolean {
  const imported = Object.hasOwn(item, 'legacyImport') ? item.legacyImport : undefined;
  if (item.entry === undefined || item.entry === 'command') return item.operation !== null && item.clientCommandId !== null && imported === undefined;
  if (item.entry !== 'legacyImport' || !imported) return false;
  const { state } = item;
  return item.operation === null && item.clientCommandId === null && item.beforeStatus === null && item.snapshotState === 'legacyUnknown'
    && item.acceptedTerms === null && imported.importedAt === item.occurredAt && state.state === 'known' && state.stateVersion === '1'
    && state.status === (item.kind === 'reminder' ? 'set' : 'joined') && state.acceptanceProvenance === 'legacyUnknown'
    && state.acceptedRevisionId === null && state.acceptedPublicationVersion === null && state.acceptedTermsHash === null
    && state.changedAt === item.occurredAt && state.acceptedAt === imported.legacyRecordedAt;
}

/** The entry rules; mirrors v2.CheckHistoryResponse. Either every item carries
 * the discriminator (a variant-aware server) or none does (one that ignored
 * the header); an import never presents an administrator's command as ours. */
export function validHistoryResponse(response: QueryHistoryResponse, allowed: readonly string[]): boolean {
  const tagged = response.items.filter((item) => item.entry !== undefined).length;
  if (tagged !== 0 && (!allowed.includes('legacyImportHistoryV1') || tagged !== response.items.length)) return false;
  return response.items.every(validHistoryEntry);
}
