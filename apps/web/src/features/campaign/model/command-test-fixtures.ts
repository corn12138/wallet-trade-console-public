import { vi } from 'vitest';
import type { CampaignAPI, CampaignSession, InteractionCommand } from './ports';
import { CampaignAPIError } from './ports';
import type { CommandResponse, CommandState, QueryDetailResponse, QueryMeResponse, QueryPersonalStateView, RevisionInput } from './contract.generated';
import type { CommandStorage } from './command-storage';
import { CampaignCommandStore } from './command-store';
import { CampaignCommandController } from './command-controller';

export const ownerA = '0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa';
export const ownerB = '0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb';
export const sessionA: CampaignSession = { owner: ownerA, generation: 1, token: 'test-a' };
export const sessionB: CampaignSession = { owner: ownerB, generation: 2, token: 'test-b' };
export const campaignId = 'campaign-fixture';
export const revisionId = '11111111-1111-4111-8111-111111111111';
export const at = '2026-10-08T10:00:00Z';
export const joinIntent = { operation: 'join' as const, expectedStateVersion: '0', expectedPublicationVersion: '1' };
export function deferred<T>() {
  let resolve!: (value: T) => void; let reject!: (error: unknown) => void;
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}
export class MemoryCommandStorage implements CommandStorage {
  data = new Map<string, string>();
  getItem(key: string): string | null { return this.data.get(key) ?? null; }
  setItem(key: string, value: string): void { this.data.set(key, value); }
}
export function absent(): QueryPersonalStateView {
  return { state: 'absent', stateVersion: '0', status: null, acceptanceProvenance: null,
    acceptedRevisionId: null, acceptedPublicationVersion: null, acceptedTermsHash: null, acceptedAt: null, changedAt: null };
}
export function commandState(status: CommandState['status'] = 'joined', version = '1', kind: CommandState['kind'] = 'join'): CommandState {
  return { kind, status, stateVersion: version, acceptanceProvenance: 'verified', acceptedRevisionId: revisionId,
    acceptedPublicationVersion: '1', acceptedTermsHash: 'a'.repeat(64), acceptedAt: at };
}
export function me(version = '0', status: CommandState['status'] = 'joined', publicationVersion = '1'): QueryMeResponse {
  const state = version === '0' ? absent() : { ...commandState(status, version), state: 'known' as const, changedAt: at };
  if ('kind' in state) delete (state as Partial<CommandState>).kind;
  return { schemaVersion: 1, serverNow: at, asOf: at, items: [{ id: campaignId, publicationVersion, interactionVersion: version, participation: state, reminder: absent() }] };
}
export function response(command: InteractionCommand, status: CommandState['status'] = 'joined', currentVersion?: string): CommandResponse {
  const kind = command.operation === 'join' || command.operation === 'leave' ? 'join' : 'reminder';
  const originalStatus = { join: 'joined', leave: 'left', 'set-reminder': 'set', 'remove-reminder': 'removed' }[command.operation] as CommandState['status'];
  const version = (BigInt(command.input.expectedStateVersion) + 1n).toString();
  const publicationVersion = 'expectedPublicationVersion' in command.input ? command.input.expectedPublicationVersion : '2';
  return { schemaVersion: 1, serverNow: at, asOf: at, replayed: false,
    original: { campaignId, revisionId, publicationVersion, interactionVersion: version, changed: true, state: commandState(originalStatus, version, kind) },
    current: { campaignId, revisionId, publicationVersion, interactionVersion: currentVersion ?? version, changed: false, state: commandState(status, currentVersion ?? version, kind) } };
}
export function detail(publicationVersion = '1'): QueryDetailResponse {
  const copy = { title: 'Campaign', description: 'Description', rules: 'Rules', rewardDescription: 'Display only' };
  const content: RevisionInput = { schemaVersion: 1, termsCanonicalVersion: 1, templateId: 'campaignCardV1',
    displayContent: { layout: 'compact', sortOrder: 0 }, materialTerms: { copy: { en: copy, zh: copy }, startAt: '2026-10-01T00:00:00Z', endAt: '2026-11-01T00:00:00Z',
      rules: { join: { kind: 'walletJoinV1', version: 1, params: {} }, reminder: { kind: 'reminderIntentV1', version: 1, params: {} } }, rewardDelivery: 'display-only' }, cta: { actionKind: 'none' } };
  return { schemaVersion: 1, serverNow: at, asOf: at, item: { id: campaignId, slug: null, revisionId, publicationVersion,
    interactionVersion: '0', mode: 'enabled', effectivePhase: 'active', contentState: 'ready', content,
    materialTermsHash: 'a'.repeat(64), contentHash: 'b'.repeat(64), participantCount: '0', statisticsState: 'ready', nextTransitionAt: '2026-11-01T00:00:00Z' } };
}
export function apiFixture() {
  const unavailable = () => Promise.reject(new Error('not configured'));
  return {
    list: vi.fn<CampaignAPI['list']>(unavailable), detail: vi.fn<CampaignAPI['detail']>(async () => detail()),
    me: vi.fn<CampaignAPI['me']>(async () => me()), history: vi.fn<CampaignAPI['history']>(unavailable),
    command: vi.fn<CampaignAPI['command']>(async () => { throw new CampaignAPIError('CAMPAIGN_NOT_FOUND', 404); }),
    execute: vi.fn<CampaignAPI['execute']>(async (_session, _id, command) => response(command)),
    create: vi.fn<CampaignAPI['create']>(unavailable), revision: vi.fn<CampaignAPI['revision']>(unavailable),
    publication: vi.fn<CampaignAPI['publication']>(unavailable), preview: vi.fn<CampaignAPI['preview']>(unavailable),
    adminCommand: vi.fn<CampaignAPI['adminCommand']>(unavailable),
  } satisfies CampaignAPI;
}
export function fixture() {
  const storage = new MemoryCommandStorage(); const store = new CampaignCommandStore(() => storage);
  const api = apiFixture(); const onCurrent = vi.fn(); const onUnauthorized = vi.fn(); let active = true;
  const controller = new CampaignCommandController({ api, session: sessionA, campaignId, isCurrent: () => active, onCurrent, onUnauthorized }, store);
  return { controller, api, store, storage, onCurrent, onUnauthorized, setActive: (value: boolean) => { active = value; } };
}
