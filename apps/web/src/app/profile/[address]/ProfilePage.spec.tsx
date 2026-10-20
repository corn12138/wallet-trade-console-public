import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { renderWithIntl, renderWithIntlZh } from '@/test/renderWithIntl';
import { OTHER_PORTFOLIO_OWNER, PORTFOLIO_OWNER, portfolioAsset, portfolioInventory, portfolioState } from '@/test/portfolio-fixtures';
import ProfilePage from './ProfilePage';

let state = portfolioState();
let connected = PORTFOLIO_OWNER;
const portfolioHook = vi.fn();
const openConnect = vi.fn();
vi.mock('@/hooks/usePortfolioData', () => ({ usePortfolioData: (options: unknown) => { portfolioHook(options); return state; } }));
vi.mock('next/navigation', () => ({ useParams: () => ({ address: PORTFOLIO_OWNER }) }));
vi.mock('wagmi', () => ({ useAccount: () => ({ address: connected }) }));
vi.mock('@/lib/web3', () => ({ supportedChains: [] }));
vi.mock('@/lib/api/atlas', () => ({ getActivityFeed: async () => ({ items: [] }) }));
vi.mock('@/lib/api/social', () => ({ getFollowState: async () => ({ isFollowing: false, followers: 0 }), followProfile: vi.fn(), unfollowProfile: vi.fn() }));
vi.mock('@/app/_atlas/AppContext', () => ({ useApp: () => ({ openConnect, toast: vi.fn(), walletState: 'connected' }) }));

function renderProfile(chinese = false) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const tree = () => <QueryClientProvider client={client}><ProfilePage /></QueryClientProvider>;
  const result = (chinese ? renderWithIntlZh : renderWithIntl)(tree());
  return { ...result, rerenderProfile: () => result.rerender(tree()) };
}

beforeEach(() => {
  state = portfolioState();
  state.scope = 'cross-chain';
  connected = PORTFOLIO_OWNER;
  portfolioHook.mockClear();
  openConnect.mockClear();
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify([
    { id: 'created:one', symbol: 'PUBLIC', name: 'Public creation', address: '0x3333333333333333333333333333333333333333' },
  ]), { status: 200 })));
});
afterEach(() => vi.unstubAllGlobals());

describe('Profile private holdings', () => {
  it('shares unknown valuation and precise quantities while retaining the cross-chain scope', () => {
    state = portfolioState(portfolioInventory([portfolioAsset({ assetType: 'token', name: 'Tracked token', symbol: 'TRACK', rawBalance: '90071992547409931234567890', balanceUnit: 'base-unit', valuationReason: 'holder-valuation-unavailable' })]));
    renderProfile();
    expect(portfolioHook).toHaveBeenCalledWith({ targetAddress: PORTFOLIO_OWNER });
    expect(screen.getByTestId('portfolio-scope')).toHaveTextContent('Across supported chains');
    expect(screen.getByTestId('portfolio-quantity')).toHaveTextContent('90071992547409931234567890');
    expect(screen.getByText('Raw base units')).toBeInTheDocument();
    expect(screen.getByTestId('portfolio-valuation')).toHaveTextContent('Valuation unavailable');
    expect(document.body).not.toHaveTextContent('$0.00');
  });

  it('uses the supplied valuation subtotal, including known zero, instead of independently summing rows', () => {
    state = portfolioState(portfolioInventory([portfolioAsset({ valueUsd: 0, valuationStatus: 'known', valuationReason: null })]));
    renderProfile();
    expect(screen.getByTestId('portfolio-valuation')).toHaveTextContent('$0.00');
    expect(screen.getByTestId('portfolio-asset-value')).toHaveTextContent('$0.00');
  });

  it('immediately hides old rows, values and counts when switching viewers on the same profile', () => {
    const result = renderProfile();
    expect(screen.getByTestId('portfolio-asset')).toBeInTheDocument();
    state = { ...state, isAuthorized: false, accessState: 'other-account' };
    connected = OTHER_PORTFOLIO_OWNER;
    result.rerenderProfile();
    expect(screen.queryByTestId('portfolio-asset')).not.toBeInTheDocument();
    expect(screen.queryByTestId('portfolio-valuation')).not.toBeInTheDocument();
    expect(screen.getByTestId('profile-holdings-private')).toHaveTextContent('Holdings are only visible to their owner.');
    expect(screen.queryByRole('button', { name: 'Connect / sign in' })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Holdings · —' })).toBeInTheDocument();
  });

  it('keeps public created tokens accessible for another viewer', async () => {
    connected = OTHER_PORTFOLIO_OWNER;
    state = { ...state, isAuthorized: false, accessState: 'other-account' };
    renderProfile();
    await waitFor(() => expect(screen.getByRole('button', { name: 'Created tokens · 1' })).toBeInTheDocument());
    fireEvent.click(screen.getByRole('button', { name: 'Created tokens · 1' }));
    expect(screen.getByText('Public creation')).toBeInTheDocument();
    expect(screen.queryByTestId('portfolio-asset')).not.toBeInTheDocument();
  });

  it('offers authentication only for a disconnected or matching wallet, with Chinese private copy', () => {
    state = { ...state, isAuthorized: false, accessState: 'signin-required' };
    renderProfile(true);
    const notice = screen.getByTestId('profile-holdings-private');
    expect(notice).toHaveTextContent('持仓仅本人可查看。');
    expect(notice).toHaveTextContent('使用此钱包登录后查看持仓。');
    fireEvent.click(within(notice).getByRole('button', { name: '连接 / 登录' }));
    expect(openConnect).toHaveBeenCalledOnce();
    expect(document.body.textContent).not.toMatch(/profileDetail\.|portfolioAtlas\./);
  });
});
