import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { renderWithIntl, renderWithIntlZh } from '@/test/renderWithIntl';
import variantCases from '../../../../../../packages/shared/contracts/fixtures/campaign-v2-variant-cases.json';
import { CampaignExperience } from './campaign-experience';
import { apiFixture, at, deferred, sessionA, sessionB } from '../model/command-test-fixtures';
import { CampaignAPIError } from '../model/ports';
import type { QueryHistoryResponse, QueryListResponse, QueryMyCampaignView, QueryPersonalStateView } from '../model/contract.generated';

function variant<T>(name: string): T {
  const found = variantCases.find((item) => item.name === name);
  if (!found) throw new Error(`missing variant case: ${name}`);
  return JSON.parse(found.raw) as T;
}
const absent: QueryPersonalStateView = { state: 'absent', stateVersion: '0', status: null, acceptanceProvenance: null, acceptedRevisionId: null,
  acceptedPublicationVersion: null, acceptedTermsHash: null, acceptedAt: null, changedAt: null };
const imported = (status: 'joined' | 'set'): QueryPersonalStateView => ({ state: 'known', stateVersion: '1', status, acceptanceProvenance: 'legacyUnknown',
  acceptedRevisionId: null, acceptedPublicationVersion: null, acceptedTermsHash: null, acceptedAt: '2020-01-15T00:00:00Z', changedAt: at });
const personal = (id: string, participation = absent, reminder = absent, interactionVersion = '2'): QueryMyCampaignView =>
  ({ id, publicationVersion: '1', interactionVersion, participation, reminder });

function baselineList() {
  const api = apiFixture();
  const list = variant<QueryListResponse>('negotiated list mixing ready, unsupported and baseline items');
  api.list.mockResolvedValue(list);
  api.me.mockImplementation(async (_session, ids) => ({ schemaVersion: 1, serverNow: at, asOf: at,
    items: ids.map((id) => personal(id, id === 'legacy-1' ? imported('joined') : absent, absent, list.items.find((item) => item.id === id)?.interactionVersion)) }));
  return api;
}

beforeEach(() => {
  Object.defineProperty(HTMLDialogElement.prototype, 'showModal', { configurable: true, value: function(this: HTMLDialogElement) { this.setAttribute('open', ''); } });
  Object.defineProperty(HTMLDialogElement.prototype, 'close', { configurable: true, value: function(this: HTMLDialogElement) { this.removeAttribute('open'); } });
});

describe('imported legacy campaigns', () => {
  it('renders baseline content read-only and as plain text beside other content', async () => {
    const api = baselineList();
    renderWithIntl(<CampaignExperience api={api} enabled session={null} onConnect={vi.fn()} />);
    const card = (await screen.findByText('Public legacy <b>not markup</b>')).closest('article')!;
    // Legacy markup stays text: the only link is the app's own detail route.
    expect(card.querySelector('b, img')).toBeNull();
    expect([...card.querySelectorAll('a')].map((link) => link.getAttribute('href'))).toEqual(['/campaign/legacy-1']);
    expect(within(card).getByText(/Read-only description/)).toBeInTheDocument();
    expect(within(card).getByText('100 USDC pool')).toBeInTheDocument();
    expect(within(card).getByRole('note')).toHaveTextContent('Imported from the previous campaign system');
    expect(within(card).getByText(/Original details captured/)).toBeInTheDocument();
    expect(within(card).getByText('2 participants')).toBeInTheDocument();
    // The ready campaign and the client-unsupported one keep their own rendering.
    expect(screen.getByText('Test activity')).toBeInTheDocument();
    const unsupported = document.querySelector('[data-campaign-id="legacy-2"]') as HTMLElement;
    expect(within(unsupported).getByRole('status')).toHaveTextContent('This campaign version is not supported. New actions are unavailable.');
    expect(unsupported.textContent).not.toContain('Imported from the previous campaign system');
  });

  it('never offers joining or a reminder on a baseline, but keeps cancellation', async () => {
    const api = baselineList();
    api.execute.mockRejectedValue(new CampaignAPIError('CAMPAIGN_TEST_STOP', 409));
    renderWithIntl(<CampaignExperience api={api} enabled session={sessionA} onConnect={vi.fn()} />);
    const card = (await screen.findByText('Public legacy <b>not markup</b>')).closest('article')!;
    await waitFor(() => expect(within(card).getByRole('button', { name: 'Leave campaign' })).toBeEnabled());
    expect(within(card).queryByRole('button', { name: 'Join' })).toBeNull();
    expect(within(card).queryByRole('button', { name: 'Save reminder preference' })).toBeNull();
    // The ready campaign still offers joining.
    expect(within(screen.getByText('Test activity').closest('article')!).getByRole('button', { name: 'Join' })).toBeInTheDocument();
    fireEvent.click(within(card).getByRole('button', { name: 'Leave campaign' }));
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Confirm' }));
    await waitFor(() => expect(api.execute).toHaveBeenCalledOnce());
    const [, id, command] = api.execute.mock.calls[0];
    expect(id).toBe('legacy-1');
    expect(command).toMatchObject({ operation: 'leave', input: { expectedStateVersion: '1' } });
    expect(command.input).not.toHaveProperty('expectedPublicationVersion');
  });

  it('shows imported history as the previous system\'s record, never as the user\'s command', async () => {
    const api = apiFixture();
    api.history.mockResolvedValue(variant<QueryHistoryResponse>('negotiated history with a cancellation and two imports'));
    renderWithIntl(<CampaignExperience api={api} enabled history session={sessionA} onConnect={vi.fn()} />);
    expect(await screen.findByText('Imported participation')).toBeInTheDocument();
    expect(screen.getByText('Imported reminder preference')).toBeInTheDocument();
    expect(screen.getAllByText('Imported from the previous campaign system. This entry is not a command you submitted.')).toHaveLength(2);
    expect(screen.getAllByText(/Recorded by the previous campaign system .* The terms accepted then are unknown\./)).toHaveLength(2);
    expect(screen.getAllByText(/^Imported /).length).toBeGreaterThanOrEqual(2);
    const entries = screen.getAllByRole('article');
    expect(entries).toHaveLength(3);
    expect(within(entries[0]).getByText(/Leave campaign/)).toBeInTheDocument();
    for (const entry of entries.slice(1)) {
      expect(within(entry).queryByText(/^Join/)).toBeNull();
      expect(within(entry).getByRole('link', { name: 'Current status' })).toHaveAttribute('href', '/campaign/legacy-1');
    }
    expect(api.execute).not.toHaveBeenCalled();
  });

  it('states an unknown legacy record time instead of inventing one', async () => {
    const api = apiFixture();
    api.history.mockResolvedValue(variant<QueryHistoryResponse>('import entry with an unknown legacy record time'));
    renderWithIntl(<CampaignExperience api={api} enabled history session={sessionA} onConnect={vi.fn()} />);
    expect(await screen.findByText('The previous campaign system did not record when this happened. The terms accepted then are unknown.')).toBeInTheDocument();
  });

  it('keeps an unpublished import reachable for cancellation through the wallet\'s own state', async () => {
    const api = apiFixture();
    api.detail.mockRejectedValue(new CampaignAPIError('CAMPAIGN_NOT_FOUND', 404));
    api.me.mockResolvedValue({ schemaVersion: 1, serverNow: at, asOf: at, items: [personal('hidden-1', imported('joined'), absent, '1')] });
    api.execute.mockRejectedValue(new CampaignAPIError('CAMPAIGN_TEST_STOP', 409));
    renderWithIntl(<CampaignExperience api={api} enabled id="hidden-1" session={sessionA} onConnect={vi.fn()} />);
    expect(await screen.findByText('This campaign is not publicly listed. Your recorded status remains, and you can still cancel it.')).toBeInTheDocument();
    expect(screen.queryByText('This campaign is not available.')).toBeNull();
    await waitFor(() => expect(screen.getByRole('button', { name: 'Leave campaign' })).toBeEnabled());
    expect(screen.queryByRole('button', { name: 'Join' })).toBeNull();
    fireEvent.click(screen.getByRole('button', { name: 'Leave campaign' }));
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Confirm' }));
    await waitFor(() => expect(api.execute).toHaveBeenCalledOnce());
    expect(api.execute.mock.calls[0][2]).toMatchObject({ operation: 'leave', input: { expectedStateVersion: '1' } });
    expect(api.me.mock.calls[0][1]).toEqual(['hidden-1']);
  });

  it.each([
    ['no recorded state', new CampaignAPIError('CAMPAIGN_NOT_FOUND', 404), 'This campaign is not available.'],
    ['a personal read failure', new CampaignAPIError('UNAVAILABLE', 503), 'Your participation status could not be verified. No changes have been assumed.'],
  ])('does not turn %s behind a public 404 into a state', async (_label, error, message) => {
    const api = apiFixture();
    api.detail.mockRejectedValue(new CampaignAPIError('CAMPAIGN_NOT_FOUND', 404));
    api.me.mockRejectedValue(error);
    renderWithIntl(<CampaignExperience api={api} enabled id="hidden-1" session={sessionA} onConnect={vi.fn()} />);
    expect(await screen.findByText(message)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Leave campaign' })).toBeNull();
  });

  it('does not probe personal state for a guest behind a public 404', async () => {
    const api = apiFixture();
    api.detail.mockRejectedValue(new CampaignAPIError('CAMPAIGN_NOT_FOUND', 404));
    renderWithIntl(<CampaignExperience api={api} enabled id="hidden-1" session={null} onConnect={vi.fn()} />);
    expect(await screen.findByText('This campaign is not available.')).toBeInTheDocument();
    expect(api.me).not.toHaveBeenCalled();
  });

  it('masks the previous wallet\'s unpublished state at once on A to B', async () => {
    const api = apiFixture();
    const pendingB = deferred<Awaited<ReturnType<typeof api.me>>>();
    api.detail.mockRejectedValue(new CampaignAPIError('CAMPAIGN_NOT_FOUND', 404));
    api.me.mockImplementation(async (session) => session.owner === sessionA.owner
      ? { schemaVersion: 1, serverNow: at, asOf: at, items: [personal('hidden-1', imported('joined'), absent, '1')] } : pendingB.promise);
    const view = renderWithIntl(<CampaignExperience api={api} enabled id="hidden-1" session={sessionA} onConnect={vi.fn()} />);
    await screen.findByRole('button', { name: 'Leave campaign' });
    view.rerender(<CampaignExperience api={api} enabled id="hidden-1" session={sessionB} onConnect={vi.fn()} />);
    expect(screen.queryByRole('button', { name: 'Leave campaign' })).toBeNull();
    expect(screen.queryByText(/Joined/)).toBeNull();
    await act(async () => { pendingB.reject(new CampaignAPIError('CAMPAIGN_NOT_FOUND', 404)); });
    expect(await screen.findByText('This campaign is not available.')).toBeInTheDocument();
  });

  it('renders baselines and imported entries in Chinese without raw keys', async () => {
    const api = baselineList();
    api.history.mockResolvedValue(variant<QueryHistoryResponse>('negotiated history with a cancellation and two imports'));
    const list = renderWithIntlZh(<CampaignExperience api={api} enabled session={null} onConnect={vi.fn()} />);
    expect(await screen.findByText(/此活动从旧活动系统导入/)).toBeInTheDocument();
    expect(document.body.textContent).not.toContain('campaignV2.');
    list.unmount();
    renderWithIntlZh(<CampaignExperience api={api} enabled history session={sessionA} onConnect={vi.fn()} />);
    expect(await screen.findByText('导入的参加记录')).toBeInTheDocument();
    expect(document.body.textContent).not.toContain('campaignV2.');
  });
});
