import type { CommandResponse, CommandState, QueryDetailResponse, QueryMeResponse, QueryPersonalStateView } from './contract.generated';
import { decodeCampaignContract } from './decode-revision';
import { CampaignAPIError } from './ports';
import { timestamp, validatePersonalState } from './read-state';
import type { CampaignCommandIntent, CommandRecord } from './command-types';

function invalid(): never { throw new CampaignAPIError('CAMPAIGN_RESPONSE_INVALID', 0); }
export function checkedCommand(value: unknown, record: CommandRecord): CommandResponse {
  const decoded = decodeCampaignContract('CommandResponse', value);
  if (!decoded.supported) invalid();
  const response = decoded.value; const { original, current } = response;
  const kind = record.command.operation === 'join' || record.command.operation === 'leave' ? 'join' : 'reminder';
  if (original.campaignId !== record.campaignId || current.campaignId !== record.campaignId
    || !original.state || !current.state || original.state.kind !== kind || current.state.kind !== kind) invalid();
  const target = { join: 'joined', leave: 'left', 'set-reminder': 'set', 'remove-reminder': 'removed' }[record.command.operation];
  if (original.state.status !== target) invalid();
  checkedCommandState(original.state); checkedCommandState(current.state);
  timestamp(response.serverNow); timestamp(response.asOf);
  const expected = BigInt(record.command.input.expectedStateVersion);
  if (BigInt(original.state.stateVersion) !== expected + (original.changed ? 1n : 0n)
    || BigInt(current.state.stateVersion) < BigInt(original.state.stateVersion)
    || BigInt(current.publicationVersion) < BigInt(original.publicationVersion)
    || BigInt(current.interactionVersion) < BigInt(original.interactionVersion)) invalid();
  if ('expectedPublicationVersion' in record.command.input && original.publicationVersion !== record.command.input.expectedPublicationVersion) invalid();
  return response;
}
export function checkedMe(value: unknown, campaignId: string): QueryMeResponse {
  const decoded = decodeCampaignContract('QueryMeResponse', value);
  if (!decoded.supported || decoded.value.items.length !== 1 || decoded.value.items[0].id !== campaignId) invalid();
  validatePersonalState(decoded.value.items[0].participation, 'join');
  validatePersonalState(decoded.value.items[0].reminder, 'reminder');
  timestamp(decoded.value.serverNow); timestamp(decoded.value.asOf);
  return decoded.value;
}
export function checkedDetail(value: unknown, campaignId: string): QueryDetailResponse {
  const decoded = decodeCampaignContract('QueryDetailResponse', value);
  if (!decoded.supported || decoded.value.item.id !== campaignId) invalid();
  return decoded.value;
}
export function personalVersion(state: QueryPersonalStateView): string | null {
  if (state.state === 'absent') return state.stateVersion === '0' && state.status === null ? '0' : null;
  return state.state === 'known' && state.stateVersion !== null && BigInt(state.stateVersion) > 0n ? state.stateVersion : null;
}
export function reviewMatches(intent: CampaignCommandIntent, me: QueryMeResponse | null, detail: QueryDetailResponse | null): boolean {
  if (!me) return false;
  const state = intent.operation === 'join' || intent.operation === 'leave' ? me.items[0].participation : me.items[0].reminder;
  if (personalVersion(state) !== intent.expectedStateVersion) return false;
  if (intent.operation === 'leave' || intent.operation === 'remove-reminder') return true;
  return 'expectedPublicationVersion' in intent && !!detail && detail.item.contentState === 'ready' && detail.item.content !== null && detail.item.mode === 'enabled'
    && detail.item.effectivePhase === (intent.operation === 'join' ? 'active' : 'upcoming')
    && detail.item.publicationVersion === intent.expectedPublicationVersion
    && detail.item.publicationVersion === me.items[0].publicationVersion;
}

function checkedCommandState(state: CommandState): void {
  const positive = state.kind === 'join' ? 'joined' : 'set';
  const negative = state.kind === 'join' ? 'left' : 'removed';
  if (state.stateVersion === '0' || state.status !== positive && state.status !== negative) invalid();
  const terms = [state.acceptedRevisionId, state.acceptedPublicationVersion, state.acceptedTermsHash];
  if (state.acceptanceProvenance === 'verified') {
    if (terms.some((value) => value === null) || state.acceptedPublicationVersion === '0' || state.acceptedAt === null) invalid();
  } else if (terms.some((value) => value !== null) || state.acceptanceProvenance === 'neverAccepted' && (state.status !== negative || state.acceptedAt !== null)) invalid();
  if (state.acceptedAt) timestamp(state.acceptedAt);
}
