import { act, fireEvent, screen, waitFor, within } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { renderWithIntl, renderWithIntlZh } from '@/test/renderWithIntl';
import { CampaignExperience } from './campaign-experience';
import { CampaignCard, type CampaignCardProps } from './campaign-card';
import { apiFixture, detail, me, response, sessionA, sessionB, at } from '../model/command-test-fixtures';
import { CampaignAPIError } from '../model/ports';
import type { QueryHistoryView } from '../model/contract.generated';

let sequence = 0;
function setup() {
  const id = `ui-campaign-${++sequence}`;
  const api = apiFixture();
  const item = { ...detail().item, id };
  const personal = { ...me().items[0], id };
  api.list.mockResolvedValue({ schemaVersion: 1, serverNow: at, asOf: at, items: [item], nextCursor: null });
  api.detail.mockResolvedValue({ ...detail(), item });
  api.me.mockResolvedValue({ ...me(), items: [personal] });
  api.execute.mockImplementation(async (_session, _id, command) => {
    const result = response(command, command.operation === 'leave' ? 'left' : 'joined');
    return { ...result, original: { ...result.original, campaignId: id }, current: { ...result.current, campaignId: id } };
  });
  const props: CampaignCardProps & { item: typeof item } = { api, campaignId: id, item, personal, session: sessionA, publicReady: true,
    personalReady: true, onConnect: vi.fn(), onRefresh: vi.fn() };
  return { api, item, personal, props };
}

beforeEach(() => {
  Object.defineProperty(HTMLDialogElement.prototype, 'showModal', { configurable: true, value: function(this: HTMLDialogElement) { this.setAttribute('open', ''); } });
  Object.defineProperty(HTMLDialogElement.prototype, 'close', { configurable: true, value: function(this: HTMLDialogElement) { this.removeAttribute('open'); } });
});

describe('campaign participation views', () => {
  it('keeps guest reads public and delegates wallet connection', async () => {
    const { api } = setup(); const connect = vi.fn();
    renderWithIntl(<CampaignExperience api={api} enabled session={null} onConnect={connect} />);
    expect(await screen.findByText('0 participants')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Connect and sign in' }));
    expect(connect).toHaveBeenCalledOnce(); expect(api.me).not.toHaveBeenCalled(); expect(api.execute).not.toHaveBeenCalled();
  });
  it('keeps a personal failure unknown while public content stays readable', async () => {
    const { api } = setup(); api.me.mockRejectedValue(new CampaignAPIError('UNAVAILABLE', 503));
    renderWithIntl(<CampaignExperience api={api} enabled session={sessionA} onConnect={vi.fn()} />);
    await screen.findByText('0 participants');
    await waitFor(() => expect(api.me).toHaveBeenCalled());
    expect(screen.getByRole('button', { name: 'Join' })).toBeDisabled();
    expect(screen.getByText(/Your participation status could not be verified/)).toBeInTheDocument();
    expect(screen.queryByText('Not participating')).not.toBeInTheDocument();
  });
  it('requires explicit confirmation for authoritative absence and restores focus on cancel', async () => {
    const { props, api } = setup(); renderWithIntl(<CampaignCard {...props} />);
    const join = screen.getByRole('button', { name: 'Join' }); join.focus(); fireEvent.click(join);
    const dialog = screen.getByRole('dialog');
    expect(within(dialog).getByText('Rules')).toBeInTheDocument(); expect(api.execute).not.toHaveBeenCalled();
    fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }));
    expect(join).toHaveFocus();
    fireEvent.click(join); fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Confirm' }));
    await waitFor(() => expect(api.execute).toHaveBeenCalledOnce());
    expect(api.execute.mock.calls[0][2]).toMatchObject({ operation: 'join', input: { expectedStateVersion: '0', expectedPublicationVersion: '1' } });
  });
  it('freezes displayed terms and requires review again after publication changes', async () => {
    const { props, api } = setup(); const view = renderWithIntl(<CampaignCard {...props} />);
    fireEvent.click(screen.getByRole('button', { name: 'Join' }));
    const changed = { ...props.item, publicationVersion: '2', content: structuredClone(props.item.content!) };
    changed.content.materialTerms.copy.en.title = 'Changed campaign';
    view.rerender(<CampaignCard {...props} item={changed} personal={{ ...props.personal!, publicationVersion: '2' }} />);
    const dialog = screen.getByRole('dialog');
    expect(within(dialog).getByText('Campaign')).toBeInTheDocument();
    expect(within(dialog).queryByRole('button', { name: 'Confirm' })).not.toBeInTheDocument();
    fireEvent.click(within(dialog).getByRole('button', { name: 'Review current terms' }));
    expect(within(screen.getByRole('dialog')).getByText('Changed campaign')).toBeInTheDocument();
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Confirm' }));
    await waitFor(() => expect(api.execute).toHaveBeenCalledOnce());
    expect(api.execute.mock.calls[0][2].input).toMatchObject({ expectedPublicationVersion: '2' });
  });
  it('preserves cancellation for archived unsupported content without a publication CAS', async () => {
    const { props, api } = setup(); const personal = { ...me('3').items[0], id: props.item.id };
    renderWithIntl(<CampaignCard {...props} item={{ ...props.item, mode: 'archived', contentState: 'unsupported', effectivePhase: 'unsupported', content: null }} personal={personal} publicReady={false} />);
    fireEvent.click(screen.getByRole('button', { name: 'Leave campaign' }));
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Confirm' }));
    await waitFor(() => expect(api.execute).toHaveBeenCalledOnce());
    const command = api.execute.mock.calls[0][2];
    expect(command.operation).toBe('leave'); expect(command.input.expectedStateVersion).toBe('3');
    expect(command.input).not.toHaveProperty('expectedPublicationVersion');
  });
  it('removes the prior wallet confirmation synchronously on identity change', () => {
    const { props, api } = setup(); const view = renderWithIntl(<CampaignCard {...props} />);
    fireEvent.click(screen.getByRole('button', { name: 'Join' }));
    view.rerender(<CampaignCard {...props} session={sessionB} personal={null} personalReady={false} />);
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument(); expect(screen.getByRole('button', { name: 'Join' })).toBeDisabled();
    expect(api.execute).not.toHaveBeenCalled();
  });
  it('allows a failed review refresh to be reconciled without a new write', async () => {
    const { props, api } = setup();
    api.execute.mockRejectedValue(new CampaignAPIError('CAMPAIGN_STATE_CONFLICT', 409));
    api.me.mockRejectedValueOnce(new CampaignAPIError('UNAVAILABLE', 503));
    renderWithIntl(<CampaignCard {...props} />);
    fireEvent.click(screen.getByRole('button', { name: 'Join' }));
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Confirm' }));
    const reconcile = await screen.findByRole('button', { name: 'Check request result' });
    expect(screen.getByRole('button', { name: 'Join' })).toBeDisabled();
    fireEvent.click(reconcile);
    await waitFor(() => expect(screen.getByRole('button', { name: 'Join' })).toBeEnabled());
    expect(api.execute).toHaveBeenCalledOnce();
  });
  it('keeps history linked to fresh detail when a campaign leaves the public list', async () => {
    const { api, item } = setup();
    const record: QueryHistoryView = { campaignId: item.id, kind: 'join', operation: 'leave', state: me('2', 'left').items[0].participation,
      beforeStatus: 'joined', clientCommandId: '01900000-0000-7000-8000-000000000001', interactionVersion: '2', occurredAt: at,
      acceptedTerms: null, snapshotState: 'legacyUnknown' };
    api.history.mockResolvedValue({ schemaVersion: 1, serverNow: at, asOf: at, items: [record], nextCursor: null });
    renderWithIntl(<CampaignExperience api={api} enabled history session={sessionA} onConnect={vi.fn()} />);
    await screen.findByText('Historical acceptance cannot be verified.');
    expect(screen.getByRole('link', { name: 'Current status' })).toHaveAttribute('href', `/campaign/${item.id}`);
    expect(api.list).not.toHaveBeenCalled(); expect(api.execute).not.toHaveBeenCalled();
  });
  it('renders the Chinese confirmation without untranslated identifiers', async () => {
    const { props } = setup(); renderWithIntlZh(<CampaignCard {...props} />);
    await act(async () => {});
    expect(document.body.textContent).not.toContain('campaignV2.');
    expect(screen.getByRole('button', { name: '参加' })).toBeEnabled();
  });
});
