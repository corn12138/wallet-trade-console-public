import { tradeEntry } from '@/features/trade';
import type { QueryLegacyBaselineView, QueryMyCampaignView, QueryPersonalStateView, QueryPublicCampaignView, RevisionInput } from './contract.generated';
import { trustedMedia } from './media';
import type { InteractionOperation } from './ports';

export function campaignCopy(item: QueryPublicCampaignView, locale: string) {
  return item.contentState === 'ready' && item.content ? item.content.materialTerms.copy[locale === 'zh' ? 'zh' : 'en'] : null;
}

/** Read-only original values of an imported campaign. Display support is not
 * admission support: a baseline never enables join or reminder. */
export function campaignBaseline(item: QueryPublicCampaignView): QueryLegacyBaselineView | null {
  return item.contentState === 'legacyBaseline' && item.baseline ? item.baseline : null;
}

export function stateForOperation(personal: QueryMyCampaignView | null, operation: InteractionOperation): QueryPersonalStateView | null {
  const participation = operation === 'join' || operation === 'leave';
  const state = participation ? personal?.participation : personal?.reminder;
  if (state?.state === 'known' && !(participation ? ['joined', 'left'] : ['set', 'removed']).includes(state.status ?? '')) return null;
  return state ?? null;
}

export function knownPersonal(state: QueryPersonalStateView | null): state is QueryPersonalStateView & { stateVersion: string } {
  return !!state && (state.state === 'known' && state.stateVersion !== null && BigInt(state.stateVersion) > 0n && state.status !== null
    || state.state === 'absent' && state.stateVersion === '0' && state.status === null);
}

/** Whether this build has every capability the content needs: its media in
 * the bundled registry and its trade target in the trade feature. The server
 * admits content from its own registries and stays authoritative; a build
 * that cannot show part of the content must not offer a new join or reminder
 * against it. Cancellation never depends on this. */
export function localCapabilitiesReady(content: RevisionInput | null | undefined): boolean {
  if (!content) return false;
  const media = content.displayContent.mediaAssetId;
  if (media !== undefined && !trustedMedia(media, 'en')) return false;
  const cta = content.cta;
  if (cta.actionKind === 'openTrade') return cta.chainId !== undefined && cta.marketId !== undefined && tradeEntry({ chainId: cta.chainId, marketId: cta.marketId }) !== null;
  return cta.actionKind === 'none';
}

/** Without a public view (an unpublished import) only cancelling a recorded
 * state is possible. */
export function campaignActionAllowed(item: QueryPublicCampaignView | null, personal: QueryMyCampaignView | null, operation: InteractionOperation, publicReady: boolean) {
  const state = stateForOperation(personal, operation);
  if (!knownPersonal(state)) return false;
  // Leaving a recorded state never depends on current content or admission.
  if (operation === 'leave') return state.status === 'joined';
  if (operation === 'remove-reminder') return state.status === 'set';
  if (!item || !publicReady || item.contentState !== 'ready' || !item.content || item.mode !== 'enabled' || !localCapabilitiesReady(item.content)) return false;
  if (operation === 'join') return item.effectivePhase === 'active' && state.status !== 'joined';
  return item.effectivePhase === 'upcoming' && state.status !== 'set';
}

export function formatCampaignDate(value: string, locale: string): string {
  return new Intl.DateTimeFormat(locale, { dateStyle: 'medium', timeStyle: 'short', timeZone: 'UTC' }).format(new Date(value)) + ' UTC';
}

export function operationLabel(operation: InteractionOperation) {
  return { join: 'join', leave: 'leave', 'set-reminder': 'setReminder', 'remove-reminder': 'removeReminder' }[operation] as 'join' | 'leave' | 'setReminder' | 'removeReminder';
}
