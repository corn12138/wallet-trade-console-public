import type { QueryMyCampaignView, QueryPersonalStateView, QueryPublicCampaignView } from './contract.generated';
import { CampaignAPIError } from './ports';

export type ReadMode = 'poll' | 'replace' | 'more';
export type SnapshotRead<Wire> = (signal: AbortSignal, cursor?: string) => Promise<Wire>;
export type ReadRecovery<Wire> = { read: SnapshotRead<Wire>; reset(): void };
export type ResponseClock = { serverNow: string; asOf: string };
export const MAX_CAMPAIGN_ITEMS = 200;
export type PageSnapshot<Row> = { items: Row[]; nextCursor: string | null; head: Row[]; headCursor: string | null; paged: boolean; hasUpdates: boolean };

export function timestamp(value: string): number {
  const match = /^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.(\d{1,9}))?Z$/.exec(value);
  const canonical = match ? `${match[1]}.${(match[2] ?? '').padEnd(3, '0').slice(0, 3)}Z` : '';
  const result = Date.parse(canonical);
  if (!Number.isFinite(result) || new Date(result).toISOString() !== canonical) throw new CampaignAPIError('CAMPAIGN_RESPONSE_INVALID', 502);
  return result;
}

const lower = (next: string, current: string) => BigInt(next) < BigInt(current);

export function validatePersonalState(state: QueryPersonalStateView, kind: 'join' | 'reminder'): void {
  const invalid = () => { throw new CampaignAPIError('CAMPAIGN_RESPONSE_INVALID', 502); };
  const metadata = [state.status, state.acceptanceProvenance, state.acceptedRevisionId, state.acceptedPublicationVersion, state.acceptedTermsHash, state.acceptedAt, state.changedAt];
  if (state.state !== 'known') {
    if (state.stateVersion !== (state.state === 'absent' ? '0' : null) || metadata.some((value) => value !== null)) invalid();
    return;
  }
  const negative = kind === 'join' ? 'left' : 'removed';
  const positive = kind === 'join' ? 'joined' : 'set';
  if (!state.stateVersion || state.stateVersion === '0' || state.status !== positive && state.status !== negative || state.acceptanceProvenance === null || state.changedAt === null) invalid();
  if (state.changedAt) timestamp(state.changedAt);
  if (state.acceptedAt) timestamp(state.acceptedAt);
  const terms = [state.acceptedRevisionId, state.acceptedPublicationVersion, state.acceptedTermsHash];
  if (state.acceptanceProvenance === 'verified') {
    if (terms.some((value) => value === null) || state.acceptedPublicationVersion === '0' || state.acceptedAt === null) invalid();
  } else if (terms.some((value) => value !== null) || state.acceptanceProvenance === 'neverAccepted' && (state.status !== negative || state.acceptedAt !== null)) invalid();
}

export function publicCampaign(current: QueryPublicCampaignView | undefined, next: QueryPublicCampaignView): QueryPublicCampaignView {
  if (!current) return next;
  if (lower(next.publicationVersion, current.publicationVersion) || lower(next.interactionVersion, current.interactionVersion)) return current;
  // Time boundaries do not increment publicationVersion. Equal versions still
  // accept a newer authoritative phase, count or capability state.
  return next;
}

function personalState(current: QueryPersonalStateView, next: QueryPersonalStateView): QueryPersonalStateView {
  if (next.state === 'unknown' || next.stateVersion === null || current.stateVersion === null) return next;
  return lower(next.stateVersion, current.stateVersion) ? current : next;
}

export function personalCampaign(current: QueryMyCampaignView | undefined, next: QueryMyCampaignView): QueryMyCampaignView {
  if (!current) return next;
  if (lower(next.publicationVersion, current.publicationVersion) || lower(next.interactionVersion, current.interactionVersion)) return current;
  return { ...next, participation: personalState(current.participation, next.participation), reminder: personalState(current.reminder, next.reminder) };
}

export function mergePage<Row>(previous: PageSnapshot<Row> | null, page: { items: Row[]; nextCursor: string | null }, mode: ReadMode, identity: (row: Row) => string, merge: (current: Row | undefined, next: Row) => Row): PageSnapshot<Row> {
  const old = new Map(previous?.items.map((row) => [identity(row), row]));
  const seen = new Set<string>();
  const fresh = page.items.map((row) => {
    const key = identity(row);
    if (seen.has(key)) throw new CampaignAPIError('CAMPAIGN_RESPONSE_INVALID', 502);
    seen.add(key);
    return merge(old.get(key), row);
  });
  if (!previous || mode === 'replace' || mode === 'poll' && !previous.paged) {
    return { items: fresh, head: fresh, nextCursor: page.nextCursor, headCursor: page.nextCursor, paged: false, hasUpdates: false };
  }
  const updated = new Map(fresh.map((row) => [identity(row), row]));
  const items = previous.items.map((row) => updated.get(identity(row)) ?? row);
  if (mode === 'poll') {
    // A head refresh cannot safely splice a weakly consistent multi-page list.
    // Update known rows, but keep its order/cursor until an explicit refresh.
    const changed = JSON.stringify(fresh) !== JSON.stringify(previous.head) || page.nextCursor !== previous.headCursor;
    return { ...previous, items, hasUpdates: previous.hasUpdates || changed };
  }
  const additions = fresh.filter((row) => !old.has(identity(row)));
  const repeatedCursor = page.nextCursor !== null && page.nextCursor === previous.nextCursor;
  return {
    ...previous, items: [...items, ...additions].slice(0, MAX_CAMPAIGN_ITEMS), paged: true,
    nextCursor: repeatedCursor ? null : page.nextCursor,
    hasUpdates: previous.hasUpdates || fresh.length !== additions.length || repeatedCursor || items.length + additions.length >= MAX_CAMPAIGN_ITEMS && page.nextCursor !== null,
  };
}
