import { beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, within } from '@testing-library/react';
import { renderWithIntl, renderWithIntlZh } from '@/test/renderWithIntl';
import { portfolioAsset, portfolioInventory, portfolioQuery, portfolioState, PORTFOLIO_OWNER } from '@/test/portfolio-fixtures';
import { PortfolioApiError, type AtlasPortfolioAssetsResponse, type AtlasPortfolioSummary } from '@/lib/api/portfolio';
import PortfolioPage from './PortfolioPage';

let state = portfolioState();
const openConnect = vi.fn();
vi.mock('@/hooks/usePortfolioData', () => ({ usePortfolioData: () => state }));
vi.mock('wagmi', () => ({ useAccount: () => ({ address: PORTFOLIO_OWNER }), useChainId: () => 11155111 }));
vi.mock('@/lib/web3', () => ({ supportedChains: [] }));
vi.mock('@/app/_atlas/AppContext', () => ({ useApp: () => ({ openConnect }) }));

beforeEach(() => { state = portfolioState(); openConnect.mockClear(); });

describe('Portfolio valuation display', () => {
  it('keeps first-load quantities and USD unknown', () => {
    state.assetsQuery = portfolioQuery<AtlasPortfolioAssetsResponse>(undefined, { isLoading: true });
    state.summaryQuery = portfolioQuery<AtlasPortfolioSummary>(undefined, { isLoading: true });
    renderWithIntl(<PortfolioPage />);
    expect(screen.getByTestId('portfolio-valuation')).toHaveTextContent('Loading assets');
    expect(document.body).not.toHaveTextContent('$0.00');
    expect(screen.queryByTestId('portfolio-asset')).not.toBeInTheDocument();
  });

  it('separates stake quantity, exact USD30 notional, and unconfirmed created holdings from wealth', () => {
    state = portfolioState(portfolioInventory([
      portfolioAsset(),
      portfolioAsset({ id: 'perp:one', assetType: 'perp', name: 'ETH position', symbol: 'ETH', rawBalance: '1000000000000000000000000000000001', balanceUnit: 'usd30', valuationReason: 'position-equity-unavailable' }),
      portfolioAsset({ id: 'token:created', assetType: 'token', name: 'Created coin', symbol: 'NEW', rawBalance: '', balanceUnit: 'base-unit', valuationReason: 'holder-valuation-unavailable' }),
    ]));
    renderWithIntl(<PortfolioPage />);
    expect(screen.getByTestId('portfolio-valuation')).toHaveTextContent('3 assets without a valuation');
    expect(screen.getByText('Staked quantity')).toBeInTheDocument();
    expect(screen.getByText('Notional position (USD)')).toBeInTheDocument();
    expect(screen.getByText('1000.000000000000000000000000000001')).toBeInTheDocument();
    expect(screen.getByText('Holding balance not confirmed')).toBeInTheDocument();
    expect(document.body).not.toHaveTextContent('$100');
    expect(screen.getAllByTestId('portfolio-asset-value').every((node) => node.textContent === '—')).toBe(true);
  });

  it('shows a genuine known zero without calling an unknown item zero', () => {
    state = portfolioState(portfolioInventory([portfolioAsset({ valueUsd: 0, valuationStatus: 'known', valuationReason: null })]));
    renderWithIntl(<PortfolioPage />);
    expect(screen.getByTestId('portfolio-valuation')).toHaveTextContent('$0.00');
    expect(screen.getByTestId('portfolio-valuation')).toHaveTextContent('1 valued asset');
    expect(screen.getByTestId('portfolio-asset-value')).toHaveTextContent('$0.00');
  });

  it('labels the subtotal and partial source coverage independently', () => {
    const inventory = portfolioInventory([portfolioAsset({ valueUsd: 25, valuationStatus: 'known', valuationReason: null }), portfolioAsset({ id: 'stake:two' })]);
    inventory.coverage!.sources.holdings = 'unavailable';
    state = portfolioState(inventory);
    renderWithIntl(<PortfolioPage />);
    expect(screen.getByTestId('portfolio-valuation')).toHaveTextContent('$25.00');
    expect(screen.getByTestId('portfolio-valuation')).toHaveTextContent('1 asset without a valuation');
    expect(screen.getAllByText('Some asset data is temporarily unavailable.')).toHaveLength(2);
    expect(screen.getByTestId('portfolio-scope')).toHaveTextContent('does not cover the full wallet balance');
    expect(document.body).not.toHaveTextContent('partial-live');
  });

  it('shows empty only after all asset sources were successfully read', () => {
    state = portfolioState(portfolioInventory([]));
    renderWithIntl(<PortfolioPage />);
    expect(screen.getByText('No assets recorded by this platform were found.')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Open trading' })).toHaveAttribute('href', '/trade');
    expect(document.body).not.toHaveTextContent('$0.00');
  });

  it('keeps summary failures visible even when the inventory request succeeded', () => {
    state.summaryQuery = portfolioQuery<AtlasPortfolioSummary>(undefined, { isError: true, error: new PortfolioApiError('unavailable', 503) });
    renderWithIntl(<PortfolioPage />);
    expect(screen.getByText('Assets could not be loaded.')).toBeInTheDocument();
    expect(screen.getByTestId('portfolio-asset')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
    expect(state.summaryQuery.refetch).toHaveBeenCalledOnce();
  });

  it('does not claim an empty wallet when a source failed and no rows were returned', () => {
    const inventory = portfolioInventory([]);
    inventory.coverage!.sources.staking = 'unavailable';
    state = portfolioState(inventory);
    renderWithIntl(<PortfolioPage />);
    expect(screen.getAllByText('Some asset data is temporarily unavailable.')).toHaveLength(2);
    expect(screen.queryByText('No assets recorded by this platform were found.')).not.toBeInTheDocument();
    expect(document.body).not.toHaveTextContent('$0.00');
  });

  it('keeps a privacy or filter failure distinct from empty inventory', () => {
    const error = new PortfolioApiError('filters unavailable', 503, 'PORTFOLIO_VISIBILITY_UNAVAILABLE');
    state.assetsQuery = portfolioQuery<AtlasPortfolioAssetsResponse>(undefined, { isError: true, error });
    state.summaryQuery = portfolioQuery<AtlasPortfolioSummary>(undefined, { isError: true, error });
    renderWithIntl(<PortfolioPage />);
    expect(screen.getAllByText('Assets could not be loaded.')).toHaveLength(2);
    expect(screen.queryByTestId('portfolio-asset')).not.toBeInTheDocument();
    expect(screen.queryByText('No assets recorded by this platform were found.')).not.toBeInTheDocument();
    expect(document.body).not.toHaveTextContent('$0.00');
  });

  it('preserves filtered data after an ordinary same-viewer refresh failure and marks it stale', () => {
    state.assetsQuery = { ...state.assetsQuery, isError: true, isStale: true, error: new PortfolioApiError('offline', 503) };
    renderWithIntl(<PortfolioPage />);
    expect(screen.getByText('Refresh failed. Showing previously retrieved data.')).toBeInTheDocument();
    expect(screen.getByTestId('portfolio-asset')).toBeInTheDocument();
    expect(screen.queryByText('No assets recorded by this platform were found.')).not.toBeInTheDocument();
  });

  it('does not show stale cached assets after authentication is lost', () => {
    const view = renderWithIntl(<PortfolioPage />);
    expect(screen.getByTestId('portfolio-asset')).toBeInTheDocument();
    state = { ...state, isAuthorized: false, accessState: 'signin-required' };
    view.rerender(<PortfolioPage />);
    expect(screen.queryByTestId('portfolio-asset')).not.toBeInTheDocument();
    expect(screen.queryByTestId('portfolio-valuation')).not.toBeInTheDocument();
    fireEvent.click(screen.getByTestId('portfolio-connect'));
    expect(openConnect).toHaveBeenCalledOnce();
  });

  it('renders normalized legacy records without dollars or machine codes in Chinese', () => {
    const inventory = portfolioInventory([portfolioAsset({ balanceUnit: null, rawBalance: '123456789012345678901234567890' })]);
    inventory.valuationVersion = null;
    inventory.coverage = null;
    state = portfolioState(inventory);
    renderWithIntlZh(<PortfolioPage />);
    expect(screen.getByTestId('portfolio-quantity')).toHaveTextContent('123456789012345678901234567890');
    expect(screen.getByText('记录数量（单位待确认）')).toBeInTheDocument();
    expect(screen.getAllByText('估值暂不可用；已确认单位的资产仍显示记录数量。')).toHaveLength(2);
    expect(within(screen.getByTestId('portfolio-valuation')).getByText('暂无估值')).toBeInTheDocument();
    expect(document.body.textContent).not.toMatch(/portfolioAtlas\.|partial-live|missing-usd-price/);
  });
});
