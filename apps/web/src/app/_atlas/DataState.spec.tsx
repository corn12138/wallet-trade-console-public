import { screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { renderWithIntl } from '@/test/renderWithIntl';
import { DataStatePanel, DataSkeleton } from './DataState';

// The panel's error state asks the backend which dependency is unhealthy, so
// the diagnostics endpoint is the one thing these specs stub.
const getProductStatus = vi.hoisted(() => vi.fn());
vi.mock('@/lib/api/atlas', async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  getProductStatus,
}));

function withQuery(ui: React.ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return renderWithIntl(<QueryClientProvider client={client}>{ui}</QueryClientProvider>);
}

const base = {
  endpoint: '/api/token',
  emptyTitle: 'Nothing yet',
  emptyBody: 'No rows.',
};

beforeEach(() => getProductStatus.mockResolvedValue({ warnings: [] }));
afterEach(() => vi.clearAllMocks());

describe('loading placeholders', () => {
  it('reserves the incoming layout when a shape is given', () => {
    withQuery(<DataStatePanel {...base} state="loading" skeleton="rows" skeletonCount={5} />);
    const skel = screen.getByTestId('data-skeleton');
    expect(skel.getAttribute('data-shape')).toBe('rows');
    expect(skel.querySelectorAll('.skel-row')).toHaveLength(5);
    // The generic panel must be gone — two loading affordances at once is worse
    // than either alone.
    expect(screen.queryByTestId('data-state')).toBeNull();
  });

  it('renders card placeholders for card grids', () => {
    withQuery(<DataStatePanel {...base} state="loading" skeleton="cards" skeletonCount={3} />);
    const skel = screen.getByTestId('data-skeleton');
    expect(skel.getAttribute('data-shape')).toBe('cards');
    expect(skel.querySelectorAll('.block')).toHaveLength(3);
  });

  it('keeps the spinner when no shape is given', () => {
    // Opt-in matters: a skeleton that guesses wrong promises a layout and then
    // breaks it, so callers that do not know the shape must keep the spinner.
    withQuery(<DataStatePanel {...base} state="loading" />);
    expect(screen.queryByTestId('data-skeleton')).toBeNull();
    expect(screen.getByTestId('data-state').getAttribute('data-state')).toBe('loading');
  });

  it('marks placeholders busy and hides them from assistive tech', () => {
    const { container } = withQuery(<DataSkeleton shape="rows" count={2} />);
    expect(container.querySelector('[aria-busy="true"]')).not.toBeNull();
    expect(container.querySelectorAll('[aria-hidden="true"]')).toHaveLength(2);
  });
});

describe('error state explains itself', () => {
  it('names the unhealthy dependency instead of only the failed endpoint', async () => {
    getProductStatus.mockResolvedValue({
      warnings: [{ code: 'DB_UNAVAILABLE', severity: 'warn', message: 'database unreachable' }],
    });
    withQuery(<DataStatePanel {...base} state="error" errorDetail="HTTP 502" />);
    // The raw detail still shows — it is what a bug report needs.
    expect(await screen.findByText(/HTTP 502/)).toBeTruthy();
    // …and now so does the reason, which is what a user needs.
    const hint = await screen.findByTestId('product-status-hint');
    expect(hint.querySelector('[data-status-code="DB_UNAVAILABLE"]')).not.toBeNull();
  });

  it('labels the diagnostics as adjacent, not as the cause', async () => {
    // Whatever is active right now is not necessarily why THIS request failed —
    // a bridge relayer being absent says nothing about a token read. The copy
    // has to carry that, or the block implies a causation nobody established.
    getProductStatus.mockResolvedValue({
      warnings: [{ code: 'BRIDGE_RELAYER_ABSENT', severity: 'warn', message: 'no relayer' }],
    });
    withQuery(<DataStatePanel {...base} state="error" errorDetail="HTTP 502" />);
    expect(await screen.findByText(/also reporting these/i)).toBeTruthy();
  });

  it('invents no cause when the backend reports none', async () => {
    withQuery(<DataStatePanel {...base} state="error" errorDetail="HTTP 500" />);
    expect(await screen.findByText(/HTTP 500/)).toBeTruthy();
    expect(screen.queryByTestId('product-status-hint')).toBeNull();
    // …and no heading introducing an empty list.
    expect(screen.queryByText(/also reporting these/i)).toBeNull();
  });

  it('stays out of the way on a healthy screen', () => {
    const { container } = withQuery(<DataStatePanel {...base} state="data" />);
    expect(container.textContent).toBe('');
  });
});
