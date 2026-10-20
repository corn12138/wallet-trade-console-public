import { afterEach, describe, expect, it, vi } from 'vitest';
import { createCampaignHTTP } from './http';
import { deferred, sample, sessionA } from '../hooks/read.test-fixtures';
import variantCases from '../../../../../../packages/shared/contracts/fixtures/campaign-v2-variant-cases.json';
import { campaignQueryKeys } from '../model/query-keys';

const variantRaw = (name: string) => {
  const found = variantCases.find((item) => item.name === name);
  if (!found) throw new Error(`missing variant case: ${name}`);
  return found.raw;
};
const raw = (body: string) => new Response(body, { status: 200, headers: { 'content-type': 'application/json' } });

const response = (value: unknown, status = 200) => new Response(JSON.stringify(value), { status, headers: { 'content-type': 'application/json' } });
afterEach(() => { vi.useRealTimers(); vi.unstubAllEnvs(); });

describe('campaign HTTP credential and wire boundary', () => {
  it('uses the configured API helper while public reads omit every identity source', async () => {
    vi.stubEnv('NEXT_PUBLIC_API_URL', 'https://api.example.test/api');
    vi.stubEnv('NEXT_PUBLIC_API_FORCE_SAME_ORIGIN', 'false');
    localStorage.setItem('web3_token', 'must-not-be-used');
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(response(sample('QueryListResponse')));
    await createCampaignHTTP({ fetch: fetcher }).list({ phase: 'active', limit: 20 });
    const [url, init] = fetcher.mock.calls[0];
    expect(url).toBe('https://api.example.test/api/v2/campaigns?phase=active&limit=20');
    expect(init).toMatchObject({ credentials: 'omit', cache: 'no-store', redirect: 'error', method: 'GET' });
    expect(new Headers(init?.headers).has('Authorization')).toBe(false);
    localStorage.removeItem('web3_token');
  });

  it('captures only the supplied private token and canonicalizes Me ids', async () => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(response(sample('QueryMeResponse')));
    const session = { ...sessionA };
    const work = createCampaignHTTP({ fetch: fetcher }).me(session, ['b', 'a', 'b']);
    session.token = 'later-token';
    await work;
    expect(String(fetcher.mock.calls[0][0])).toMatch(/\/campaigns\/me\?ids=a%2Cb$/);
    expect(fetcher.mock.calls[0][1]).toMatchObject({ credentials: 'omit', headers: { Authorization: 'Bearer token-a' } });
    expect(String(fetcher.mock.calls[0][0])).not.toContain(session.owner);
  });

  it('maps every typed command to its fixed method, route and closed input', async () => {
    // Administrator routes answer with the ledger-bound AdminCommandResponse;
    // participant routes keep CommandResponse for deployed clients.
    const fetcher = vi.fn<typeof fetch>().mockImplementation(async (url) => response(sample(String(url).includes('/campaign-admin/') ? 'AdminCommandResponse' : 'CommandResponse')));
    const api = createCampaignHTTP({ fetch: fetcher });
    const positive = sample('PositiveInteractionInput');
    const cancel = sample('CancelInteractionInput');
    await api.execute(sessionA, 'campaign-1', { operation: 'join', input: positive });
    await api.execute(sessionA, 'campaign-1', { operation: 'leave', input: cancel });
    await api.execute(sessionA, 'campaign-1', { operation: 'set-reminder', input: positive });
    await api.execute(sessionA, 'campaign-1', { operation: 'remove-reminder', input: cancel });
    await api.create(sessionA, sample('CreateCommandInput'));
    await api.revision(sessionA, 'campaign-1', sample('RevisionCommandInput'));
    await api.publication(sessionA, 'campaign-1', sample('PublicationCommandInput'));
    await api.command(sessionA, 'campaign-1', 'join', positive.clientCommandId);
    await api.adminCommand(sessionA, 'campaigns', 'create', positive.clientCommandId);
    await api.adminCommand(sessionA, 'campaign-1', 'publish', positive.clientCommandId);
    const calls = fetcher.mock.calls.map(([url, init]) => `${init?.method} ${new URL(String(url)).pathname}${new URL(String(url)).search}`);
    expect(calls.slice(0, 7)).toEqual([
      'POST /api/v2/campaigns/campaign-1/join', 'DELETE /api/v2/campaigns/campaign-1/join',
      'POST /api/v2/campaigns/campaign-1/reminder', 'DELETE /api/v2/campaigns/campaign-1/reminder',
      'POST /api/v2/campaign-admin/campaigns', 'POST /api/v2/campaign-admin/campaigns/campaign-1/revisions', 'POST /api/v2/campaign-admin/campaigns/campaign-1/publications',
    ]);
    expect(calls[7]).toContain(`/campaigns/campaign-1/commands/${positive.clientCommandId}?operation=join`);
    expect(calls[8]).toContain(`/campaign-admin/commands/${positive.clientCommandId}?operation=create`);
    expect(calls[9]).toContain(`/campaign-admin/campaigns/campaign-1/commands/${positive.clientCommandId}?operation=publish`);
    expect(JSON.parse(String(fetcher.mock.calls[1][1]?.body))).toEqual(cancel);
  });

  it.each([401, 403, 409, 410, 422, 503])('preserves status %s and its command reconciliation error without retries', async (status) => {
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(response({ schemaVersion: 1, code: 'CAMPAIGN_TEST_ERROR', requestId: 'trace-1' }, status));
    await expect(createCampaignHTTP({ fetch: fetcher }).detail('campaign-1')).rejects.toMatchObject({ code: 'CAMPAIGN_TEST_ERROR', status, requestId: 'trace-1' });
    expect(fetcher).toHaveBeenCalledTimes(1);
  });

  it.each(['duplicate', 'future', 'html', 'nul'])('rejects %s JSON before a response reaches the cache', async (kind) => {
    let raw = JSON.stringify(sample('QueryDetailResponse'));
    if (kind === 'duplicate') raw = raw.replace('"schemaVersion":1', '"schemaVersion":1,"schemaVersion":1');
    if (kind === 'future') raw = raw.replace('"schemaVersion":1', '"schemaVersion":2');
    if (kind === 'html') raw = '<html>Bad gateway</html>';
    if (kind === 'nul') raw = raw.replace('Test activity', 'Bad\\u0000text');
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(new Response(raw));
    await expect(createCampaignHTTP({ fetch: fetcher }).detail('campaign-1')).rejects.toMatchObject({ status: 502, code: kind === 'future' ? 'CAMPAIGN_UNSUPPORTED' : 'CAMPAIGN_RESPONSE_INVALID' });
  });

  it('rejects over-budget and owner-bearing requests before sending', async () => {
    const fetcher = vi.fn<typeof fetch>();
    const api = createCampaignHTTP({ fetch: fetcher });
    await expect(api.create(sessionA, { ...sample('CreateCommandInput'), reason: 'a'.repeat(65_536) })).rejects.toMatchObject({ code: 'CAMPAIGN_BODY_TOO_LARGE', status: 413 });
    await expect(api.create(sessionA, Object.assign(sample('CreateCommandInput'), { owner: sessionA.owner }))).rejects.toMatchObject({ code: 'CAMPAIGN_INVALID_INPUT', status: 400 });
    await expect(api.detail('../admin')).rejects.toMatchObject({ status: 400 });
    expect(fetcher).not.toHaveBeenCalled();
  });

  it('bounds actual streamed bytes without trusting Content-Length', async () => {
    const cancel = vi.fn();
    const body = new ReadableStream<Uint8Array>({ start(controller) { controller.enqueue(new Uint8Array(600_000)); controller.enqueue(new Uint8Array(600_000)); }, cancel });
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(new Response(body));
    await expect(createCampaignHTTP({ fetch: fetcher }).detail('campaign-1')).rejects.toMatchObject({ code: 'CAMPAIGN_RESPONSE_TOO_LARGE', status: 502 });
    expect(cancel).toHaveBeenCalledOnce();
  });

  it.each(['bom', 'invalid-utf8'])('rejects %s bytes without normalizing them into valid JSON', async (encoding) => {
    const valid = new TextEncoder().encode(JSON.stringify(sample('QueryDetailResponse')));
    const bytes = encoding === 'bom' ? new Uint8Array([0xef, 0xbb, 0xbf, ...valid]) : new Uint8Array([0xff, ...valid]);
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(new Response(bytes));
    await expect(createCampaignHTTP({ fetch: fetcher }).detail('campaign-1')).rejects.toMatchObject({ code: 'CAMPAIGN_RESPONSE_INVALID', status: 502 });
  });

  it('advertises read variants only on the reads that have them', async () => {
    const fetcher = vi.fn<typeof fetch>().mockImplementation(async (url) => {
      const path = new URL(String(url)).pathname;
      if (path.endsWith('/history')) return raw(variantRaw('negotiated history with a cancellation and two imports'));
      if (path.endsWith('/campaigns/me')) return response(sample('QueryMeResponse'));
      if (path.endsWith('/campaigns')) return raw(variantRaw('negotiated list mixing ready, unsupported and baseline items'));
      return raw(variantRaw('negotiated baseline detail'));
    });
    const api = createCampaignHTTP({ fetch: fetcher });
    const listed = await api.list({});
    const shown = await api.detail('legacy-1');
    const history = await api.history(sessionA, {});
    await api.me(sessionA, ['legacy-1']);
    const sent = fetcher.mock.calls.map(([, init]) => new Headers(init?.headers).get('X-Campaign-Variants'));
    expect(sent).toEqual(['legacyBaselineV1, contentCapabilitiesV1', 'legacyBaselineV1, contentCapabilitiesV1', 'legacyImportHistoryV1', null]);
    expect(listed.items.map((item) => item.contentState)).toEqual(['ready', 'unsupported', 'legacyBaseline']);
    expect(shown.item.baseline?.title).toBe('Public legacy <b>not markup</b>');
    expect(history.items.map((item) => item.entry)).toEqual(['command', 'legacyImport', 'legacyImport']);
  });

  it('accepts the base representation from a server that ignores the header', async () => {
    const fetcher = vi.fn<typeof fetch>().mockImplementation(async (url) => raw(variantRaw(String(url).includes('/history')
      ? 'history advertised but answered by a server that ignored the header' : 'base detail keeps an imported campaign unsupported')));
    const api = createCampaignHTTP({ fetch: fetcher });
    await expect(api.detail('legacy-1')).resolves.toMatchObject({ item: { contentState: 'unsupported', content: null } });
    await expect(api.history(sessionA, {})).resolves.toMatchObject({ items: [{ operation: 'leave' }] });
  });

  it.each([
    ['detail', 'baseline state without the baseline object'],
    ['detail', 'baseline window inverted'],
    ['list', 'negotiated list mixing ready, unsupported and baseline items'],
    ['history', 'history mixing tagged and untagged entries'],
    ['history', 'import entry presented as the user\'s join'],
    ['history', 'legacy record time differing from the state'],
  ] as const)('fails closed on a %s that breaks the variant rules: %s', async (read, name) => {
    const body = variantRaw(name);
    // In the list, one item claims executable content while carrying a baseline.
    const served = read === 'list' ? body.replace('"contentState":"legacyBaseline"', '"contentState":"ready"') : body;
    const api = createCampaignHTTP({ fetch: vi.fn<typeof fetch>().mockResolvedValue(raw(served)) });
    const work = read === 'detail' ? api.detail('legacy-1') : read === 'list' ? api.list({}) : api.history(sessionA, {});
    await expect(work).rejects.toMatchObject({ code: 'CAMPAIGN_RESPONSE_INVALID', status: 502 });
  });

  it('keys negotiated reads apart from the base representation', () => {
    expect(campaignQueryKeys.list('en', {})[2]).toBe('1+legacyBaselineV1+contentCapabilitiesV1');
    expect(campaignQueryKeys.detail('en', 'legacy-1')[2]).toBe('1+legacyBaselineV1+contentCapabilitiesV1');
    expect(campaignQueryKeys.history(sessionA, 20)[2]).toBe('1+legacyImportHistoryV1');
  });

  it('bounds a stalled request even when fetch ignores abort', async () => {
    vi.useFakeTimers();
    const pending = deferred<Response>();
    const fetcher = vi.fn<typeof fetch>().mockReturnValue(pending.promise);
    const result = createCampaignHTTP({ fetch: fetcher, timeoutMs: 100 }).list({});
    const assertion = expect(result).rejects.toMatchObject({ code: 'TIMEOUT', status: 0 });
    await vi.advanceTimersByTimeAsync(100);
    await assertion;
    expect(fetcher.mock.calls[0][1]?.signal?.aborted).toBe(true);
    pending.resolve(response(sample('QueryListResponse')));
    await Promise.resolve();
    expect(vi.getTimerCount()).toBe(0);
  });

  it('cancels a stalled response body on timeout and honors caller abort', async () => {
    vi.useFakeTimers();
    const cancel = vi.fn();
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(new Response(new ReadableStream({ cancel })));
    const api = createCampaignHTTP({ fetch: fetcher, timeoutMs: 100 });
    const work = api.detail('campaign-1');
    const assertion = expect(work).rejects.toMatchObject({ code: 'TIMEOUT' });
    await vi.advanceTimersByTimeAsync(100);
    await assertion;
    expect(cancel).toHaveBeenCalledOnce();
    const aborted = new AbortController();
    aborted.abort();
    await expect(api.detail('campaign-1', aborted.signal)).rejects.toMatchObject({ code: 'ABORTED' });
    expect(fetcher).toHaveBeenCalledTimes(1);
  });
});
