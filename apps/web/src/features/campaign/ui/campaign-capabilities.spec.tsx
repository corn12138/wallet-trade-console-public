import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { renderWithIntl, renderWithIntlZh } from '@/test/renderWithIntl';
import { CampaignCard, type CampaignCardProps } from './campaign-card';
import { apiFixture, detail, me, sessionA } from '../model/command-test-fixtures';
import { trustedMedia } from '../model/media';
import type { CTA, QueryPublicCampaignView } from '../model/contract.generated';

function card(mediaAssetId: string | undefined, cta: CTA, phase: 'active' | 'upcoming' = 'active') {
  const item: QueryPublicCampaignView = { ...structuredClone(detail().item), effectivePhase: phase };
  item.content!.displayContent = { ...item.content!.displayContent, ...(mediaAssetId ? { mediaAssetId } : {}) };
  item.content!.cta = cta;
  const api = apiFixture();
  const props: CampaignCardProps = { api, campaignId: item.id, item, personal: me().items[0], session: sessionA, publicReady: true,
    personalReady: true, onConnect: vi.fn(), onRefresh: vi.fn() };
  return { props, api };
}
const openTrade = (chainId: number, marketId: string): CTA => ({ actionKind: 'openTrade', chainId, marketId });

beforeEach(() => {
  Object.defineProperty(HTMLDialogElement.prototype, 'showModal', { configurable: true, value: function(this: HTMLDialogElement) { this.setAttribute('open', ''); } });
  Object.defineProperty(HTMLDialogElement.prototype, 'close', { configurable: true, value: function(this: HTMLDialogElement) { this.removeAttribute('open'); } });
});

describe('registry-backed campaign media and trade entry', () => {
  it('renders a registry asset from this origin with its own size and alt text', () => {
    const { props } = card('trading-week-banner-v1', { actionKind: 'none' });
    renderWithIntl(<CampaignCard {...props} />);
    const image = screen.getByRole('img', { name: 'Abstract amber and violet gradient banner' });
    expect(decodeURIComponent(image.getAttribute('src') ?? '')).toContain('/campaign-media/trading-week-banner-v1.png');
    expect(image.getAttribute('width')).toBe('1200');
    expect(screen.queryByText('Campaign media is unavailable.')).toBeNull();
  });

  it('keeps an asset unknown to this build as unavailable, never a URL from content', () => {
    for (const id of ['newer-asset-v2', 'https', 'javascript']) {
      const { props } = card(id, { actionKind: 'none' });
      const view = renderWithIntl(<CampaignCard {...props} />);
      expect(screen.getByText('Campaign media is unavailable.')).toBeInTheDocument();
      expect(screen.queryByRole('img')).toBeNull();
      view.unmount();
    }
    expect(trustedMedia(undefined, 'en')).toBeNull();
    expect(trustedMedia('trading-week-banner-v1', 'zh')?.alt).toBe('琥珀色到紫色的抽象渐变横幅');
  });

  it('opens the ordinary trading page through the trade facade and does not act', () => {
    const { props, api } = card(undefined, openTrade(11155111, 'ETH-USD'));
    renderWithIntl(<CampaignCard {...props} />);
    const link = screen.getByRole('link', { name: 'Open ETH-USD on the trading page' });
    expect(link).toHaveAttribute('href', '/trade?symbol=ETH-USD&chainId=11155111');
    expect(screen.getByText('Opening the trading page does not join this campaign or place an order.')).toBeInTheDocument();
    link.click();
    expect(api.execute).not.toHaveBeenCalled();
  });

  it.each([[11155111, 'DOGE-USD'], [1, 'ETH-USD']])('keeps a target the trade feature cannot open (%s %s) read-only: no new join', (chainId, marketId) => {
    const { props, api } = card(undefined, openTrade(chainId, marketId));
    renderWithIntl(<CampaignCard {...props} />);
    expect(screen.getByText('The linked trading destination is not currently available.')).toBeInTheDocument();
    expect(screen.queryByRole('link', { name: /trading page/ })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Join' })).toBeNull();
    expect(screen.getByRole('note')).toHaveTextContent('new participation and reminders are unavailable here');
    expect(api.execute).not.toHaveBeenCalled();
  });

  it.each([
    ['an unknown media asset', 'newer-asset-v2', { actionKind: 'none' } as CTA],
    ['an untradable target', undefined, openTrade(11155111, 'DOGE-USD')],
  ])('offers no new reminder for an upcoming campaign with %s', (_label, media, cta) => {
    const { props } = card(media, cta, 'upcoming');
    renderWithIntl(<CampaignCard {...props} />);
    expect(screen.queryByRole('button', { name: 'Save reminder preference' })).toBeNull();
    expect(screen.getByRole('note')).toHaveTextContent('new participation and reminders are unavailable here');
  });

  it('offers the same reminder when every capability is present', () => {
    const { props } = card('trading-week-banner-v1', openTrade(11155111, 'ETH-USD'), 'upcoming');
    renderWithIntl(<CampaignCard {...props} />);
    expect(screen.getByRole('button', { name: 'Save reminder preference' })).toBeEnabled();
    expect(screen.queryByRole('note')).toBeNull();
  });

  it('refuses a confirmation that is open when the capability disappears', async () => {
    const { props, api } = card(undefined, openTrade(11155111, 'ETH-USD'));
    const view = renderWithIntl(<CampaignCard {...props} />);
    fireEvent.click(screen.getByRole('button', { name: 'Join' }));
    const dialog = screen.getByRole('dialog');
    const lost = { ...props.item!, content: { ...props.item!.content!, cta: openTrade(11155111, 'DOGE-USD') } };
    view.rerender(<CampaignCard {...props} item={lost} />);
    const confirm = within(dialog).getByRole('button', { name: 'Confirm' });
    expect(confirm).toBeDisabled();
    fireEvent.click(confirm);
    await waitFor(() => expect(api.execute).not.toHaveBeenCalled());
  });

  it('keeps cancelling a recorded participation when a capability is missing', async () => {
    const { props, api } = card('newer-asset-v2', openTrade(1, 'ETH-USD'));
    const joined = { ...me('2').items[0], id: props.campaignId };
    renderWithIntl(<CampaignCard {...props} personal={joined} />);
    fireEvent.click(screen.getByRole('button', { name: 'Leave campaign' }));
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Confirm' }));
    await waitFor(() => expect(api.execute).toHaveBeenCalledOnce());
    expect(api.execute.mock.calls[0][2]).toMatchObject({ operation: 'leave', input: { expectedStateVersion: '2' } });
  });

  it('renders media and the trade entry in Chinese', () => {
    const { props } = card('trading-week-banner-v1', openTrade(11155111, 'ETH-USD'));
    renderWithIntlZh(<CampaignCard {...props} />);
    const article = document.querySelector('article')!;
    expect(within(article).getByRole('img', { name: '琥珀色到紫色的抽象渐变横幅' })).toBeInTheDocument();
    expect(within(article).getByRole('link', { name: '在交易页打开 ETH-USD' })).toBeInTheDocument();
    expect(article.textContent).not.toContain('campaignV2.');
  });
});
