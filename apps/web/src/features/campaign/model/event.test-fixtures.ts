import type { EventFeedHint, EventFeedResponse } from './contract.generated';

export const eventClock = { schemaVersion: 1 as const, serverNow: '2026-10-15T00:00:00Z', asOf: '2026-10-15T00:00:00Z' };
export function hint(sequence: number): EventFeedHint {
  return { eventId: `00000000-0000-4000-8000-${String(sequence).padStart(12, '0')}`, source: 'campaign', streamVersion: String(sequence),
    publicationVersion: String(sequence), interactionVersion: String(sequence), eventSchema: 1, occurredAt: eventClock.serverNow };
}
export function feed(family: EventFeedResponse['family'] = 'publication', overrides: Partial<EventFeedResponse> = {}): EventFeedResponse {
  return { ...eventClock, campaignId: 'campaign-1', family, headVersion: '1', cursor: 'cursor1', hasMore: false, resetRequired: true, events: [], ...overrides };
}
