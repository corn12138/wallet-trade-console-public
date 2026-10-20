import { describe, expect, it, vi } from 'vitest';
import { createCampaignHTTP } from './http';
import { feed, hint } from '../model/event.test-fixtures';
import { sessionA } from '../hooks/read.test-fixtures';

const response = (value: unknown) => new Response(JSON.stringify(value));

describe('campaign event feed HTTP boundary', () => {
  it('keeps public streams credential-free and binds personal streams only to the captured session', async () => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValueOnce(response(feed())).mockResolvedValueOnce(response(feed('personal')));
    const api = createCampaignHTTP({ fetch: fetcher });
    await api.events!('campaign-1', 'publication', { cursor: 'cursor1', limit: 50 });
    await api.meEvents!(sessionA, 'campaign-1', { limit: 20 });
    const [publicURL, publicInit] = fetcher.mock.calls[0];
    const [privateURL, privateInit] = fetcher.mock.calls[1];
    expect(new URL(String(publicURL)).pathname).toBe('/api/v2/campaigns/campaign-1/events');
    expect(new URL(String(publicURL)).searchParams.get('family')).toBe('publication');
    expect(new URL(String(publicURL)).searchParams.get('cursor')).toBe('cursor1');
    expect(new Headers(publicInit?.headers).has('Authorization')).toBe(false);
    expect(publicInit).toMatchObject({ credentials: 'omit', cache: 'no-store', redirect: 'error', method: 'GET' });
    expect(new URL(String(privateURL)).pathname).toBe('/api/v2/campaigns/campaign-1/me/events');
    expect(String(privateURL)).not.toContain(sessionA.owner);
    expect(new URL(String(privateURL)).searchParams.has('family')).toBe(false);
    expect(new Headers(privateInit?.headers).get('Authorization')).toBe('Bearer token-a');
  });

  it.each(['schema', 'event-schema', 'payload', 'duplicate-key'])('rejects %s without exposing unrecognized event data', async (kind) => {
    let raw = JSON.stringify(feed('personal', { events: [hint(2)] }));
    if (kind === 'schema') raw = raw.replace('"schemaVersion":1', '"schemaVersion":2');
    if (kind === 'event-schema') raw = raw.replace('"eventSchema":1', '"eventSchema":2');
    if (kind === 'payload') raw = raw.replace('"source":"campaign"', '"source":"campaign","owner":"must-not-escape"');
    if (kind === 'duplicate-key') raw = raw.replace('"cursor":"cursor1"', '"cursor":"cursor1","cursor":"other"');
    const api = createCampaignHTTP({ fetch: vi.fn<typeof fetch>().mockResolvedValue(new Response(raw)) });
    await expect(api.meEvents!(sessionA, 'campaign-1', {})).rejects.toMatchObject({ status: 502 });
  });

  it('rejects invalid routes, families and oversized event pages before transport', async () => {
    const fetcher = vi.fn<typeof fetch>();
    const api = createCampaignHTTP({ fetch: fetcher });
    await expect(api.events!('../private', 'publication', {})).rejects.toMatchObject({ status: 400 });
    await expect(api.events!('campaign-1', 'personal' as 'publication', {})).rejects.toMatchObject({ status: 400 });
    await expect(api.events!('campaign-1', 'publication', { limit: 51 })).rejects.toMatchObject({ status: 400 });
    await expect(api.meEvents!(sessionA, 'campaign-1', { cursor: 'a'.repeat(1025) })).rejects.toMatchObject({ status: 400 });
    expect(fetcher).not.toHaveBeenCalled();
  });
});
