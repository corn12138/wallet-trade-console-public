import { describe, expect, it } from 'vitest';
import { campaignQueryKey, campaignQueryKeys } from './query-keys';
import { MAX_CAMPAIGN_ITEMS, mergePage, publicCampaign, timestamp } from './read-state';
import { campaign, sessionA } from '../hooks/read.test-fixtures';

describe('campaign read identities and bounded page state', () => {
  it('puts locale only on public keys and keeps credentials out of keys', () => {
    const a = campaignQueryKey(campaignQueryKeys.me(sessionA, ['b', 'a', 'b']));
    expect(a).toBe(campaignQueryKey(campaignQueryKeys.me({ ...sessionA, token: 'new-token' }, ['a', 'b'])));
    expect(a).not.toContain(sessionA.token);
    expect(a).not.toBe(campaignQueryKey(campaignQueryKeys.me({ ...sessionA, generation: 2 }, ['a', 'b'])));
    expect(campaignQueryKeys.list('en', {})).not.toEqual(campaignQueryKeys.list('zh', {}));
  });
  it('accepts nanosecond UTC metadata but rejects impossible calendar values', () => {
    expect(timestamp('2026-10-08T00:00:00.123456789Z')).toBe(Date.parse('2026-10-08T00:00:00.123Z'));
    expect(() => timestamp('2026-02-31T00:00:00Z')).toThrow();
  });
  it('caps memory and stops a repeated cursor rather than looping through pages', () => {
    const first = mergePage(null, { items: Array.from({ length: 150 }, (_, i) => campaign(String(i))), nextCursor: 'same' }, 'replace', (row) => row.id, publicCampaign);
    const next = mergePage(first, { items: Array.from({ length: 50 }, (_, i) => campaign(String(i + 150))), nextCursor: 'same' }, 'more', (row) => row.id, publicCampaign);
    expect(next.items).toHaveLength(MAX_CAMPAIGN_ITEMS);
    expect(next.hasUpdates).toBe(true);
    expect(next.nextCursor).toBeNull();
  });
});
