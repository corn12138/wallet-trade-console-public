import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { screen, waitFor } from '@testing-library/react';
import { renderWithIntl } from '@/test/renderWithIntl';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { DiscoverPage } from './DiscoverPage';

/**
 * Regression guards for /discover:
 *  - no-wallet visitors must query the Sepolia display chain (11155111),
 *    never wagmi's first-configured chain (mainnet 1)
 *  - dApp cards must not render no-op Open/Details buttons: Open is a real
 *    link to the API-provided slug, and there is no Details control
 */

const mockGetDiscoverHome = vi.fn();
const mockUseChainId = vi.fn();
const mockUseAccount = vi.fn();

vi.mock('@/lib/api/atlas', () => ({
    getDiscoverHome: (chainId?: number) => mockGetDiscoverHome(chainId),
    // ProductStatusHint (below the header) polls this; empty warnings → renders nothing.
    getProductStatus: () => Promise.resolve({ warnings: [] }),
}));

vi.mock('wagmi', () => ({
    useChainId: () => mockUseChainId(),
    useAccount: () => mockUseAccount(),
    useConnect: () => ({ connectors: [] }),
    // module-scope wagmi config (lib/web3/config.ts) pulled in via Common.tsx
    createConfig: vi.fn(() => ({})),
    createStorage: vi.fn(() => ({})),
    cookieStorage: {},
    http: vi.fn(),
}));

vi.mock('@wallet-trade/shared', async (importOriginal) => ({
    ...(await importOriginal<Record<string, unknown>>()),
    TOKENS: { 11155111: [{ symbol: 'USDC' }] },
}));

vi.mock('../AppContext', () => ({
    useApp: () => ({ chainId: 11155111, walletState: 'disconnected' }),
}));

function renderDiscover() {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    return renderWithIntl(
        <QueryClientProvider client={client}>
            <DiscoverPage />
        </QueryClientProvider>,
    );
}

describe('DiscoverPage', () => {
    beforeEach(() => {
        vi.clearAllMocks();
        mockUseChainId.mockReturnValue(1);
        mockUseAccount.mockReturnValue({ isConnected: false });
        mockGetDiscoverHome.mockResolvedValue({
            generatedAt: new Date().toISOString(),
            trendingTokens: [],
            curatedDapps: [
                { id: 'swap', name: 'Swap Center', slug: '/swap', category: 'Trading', summary: 'Real quotes.', risk: 'managed', status: 'live' },
                { id: 'ext', name: 'Ext App', slug: 'https://example.org/app', category: 'Misc', summary: '', risk: 'low', status: 'live' },
                { id: 'none', name: 'No Link', slug: '', category: 'Misc', summary: '', risk: 'low', status: 'live' },
                { id: 'bridge', name: 'Bridge Center', slug: '/bridge', category: 'Cross-chain', summary: 'Roadmap.', risk: 'preview', status: 'roadmap' },
            ],
            earnProducts: [],
            marketPulse: [],
            riskMarkers: [],
        });
    });

    it('requests the Sepolia display chain when no wallet is connected', async () => {
        renderDiscover();

        await waitFor(() => {
            expect(mockGetDiscoverHome).toHaveBeenCalledWith(11155111);
        });
        expect(mockGetDiscoverHome).not.toHaveBeenCalledWith(1);
    });

    it('renders Open as a real link and offers no Details control', async () => {
        renderDiscover();

        const internal = await screen.findAllByRole('link', { name: /Open/ });
        expect(internal[0]).toHaveAttribute('href', '/swap');
        expect(internal[1]).toHaveAttribute('href', 'https://example.org/app');
        expect(internal[1]).toHaveAttribute('rel', expect.stringContaining('noopener'));

        expect(screen.getByText('No launch link configured')).toBeInTheDocument();
        expect(screen.queryByRole('button', { name: 'Open' })).not.toBeInTheDocument();
        expect(screen.queryByText('Details')).not.toBeInTheDocument();
    });

    it('renders the Bridge roadmap card as non-launchable — no Open link', async () => {
        renderDiscover();

        // The roadmap card carries a Roadmap badge and an honest note instead
        // of a launch control, and does NOT link to /bridge as a normal app.
        expect(await screen.findByTestId('curated-roadmap-badge')).toBeInTheDocument();
        expect(screen.getByTestId('curated-roadmap-note')).toHaveTextContent('roadmap');
        const bridgeLinks = screen
            .queryAllByRole('link')
            .filter((el) => el.getAttribute('href') === '/bridge');
        expect(bridgeLinks).toHaveLength(0);
    });
});
