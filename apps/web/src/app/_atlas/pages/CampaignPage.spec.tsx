import { fireEvent, screen, waitFor } from '@testing-library/react';
import { renderWithIntl } from '@/test/renderWithIntl';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { CampaignPage } from './CampaignPage';

/**
 * Campaign interactions are backed by durable API rows: Join/Remind-me call
 * the real endpoints and the rendered state comes from the API response
 * (isParticipating/hasReminder/participantCount), not local optimism.
 */

const mockGetCampaigns = vi.fn();
const mockJoin = vi.fn();
const mockSetReminder = vi.fn();
const mockRemoveReminder = vi.fn();
const mockToast = vi.fn();

vi.mock('@/lib/api/atlas', () => ({
    getCampaigns: () => mockGetCampaigns(),
}));

vi.mock('@/lib/api/social', () => ({
    joinCampaign: (id: string) => mockJoin(id),
    setCampaignReminder: (id: string) => mockSetReminder(id),
    removeCampaignReminder: (id: string) => mockRemoveReminder(id),
}));

vi.mock('wagmi', () => ({
    useAccount: () => ({ address: '0x1111111111111111111111111111111111111111', isConnected: true }),
    useChainId: () => 11155111,
    // module-scope wagmi config (lib/web3/config.ts) pulled in via Common.tsx
    createConfig: vi.fn(() => ({})),
    createStorage: vi.fn(() => ({})),
    cookieStorage: {},
    http: vi.fn(),
}));

vi.mock('../AppContext', () => ({
    useApp: () => ({
        walletState: 'connected',
        openConnect: vi.fn(),
        toast: mockToast,
    }),
}));

const future = new Date(Date.now() + 86_400_000).toISOString();

function campaign(overrides: Record<string, unknown>) {
    return {
        id: 'c1',
        title: 'Perp Tournament',
        description: null,
        banner: null,
        startDate: new Date().toISOString(),
        endDate: future,
        status: 'active',
        reward: '5,000 USDC',
        participants: 0,
        participantCount: 3,
        ...overrides,
    };
}

describe('CampaignPage interactions', () => {
    beforeEach(() => {
        vi.clearAllMocks();
        mockJoin.mockResolvedValue({ campaignId: 'c1', isParticipating: true });
        mockSetReminder.mockResolvedValue({ campaignId: 'c2', hasReminder: true });
    });

    it('joins a live campaign through the API and re-renders server state', async () => {
        mockGetCampaigns
            .mockResolvedValueOnce([campaign({ isParticipating: false })])
            .mockResolvedValueOnce([campaign({ isParticipating: true, participantCount: 4 })]);

        renderWithIntl(<CampaignPage />);

        const join = await screen.findByRole('button', { name: 'Join' });
        expect(screen.getByText('3 joined')).toBeInTheDocument();

        fireEvent.click(join);
        await waitFor(() => expect(mockJoin).toHaveBeenCalledWith('c1'));

        // State comes from the reloaded API payload — Joined pill, no button.
        await screen.findByText('Joined');
        expect(screen.queryByRole('button', { name: 'Join' })).not.toBeInTheDocument();
        expect(screen.getByText('4 joined')).toBeInTheDocument();
    });

    it('stores a reminder intent for an upcoming campaign', async () => {
        mockGetCampaigns
            .mockResolvedValueOnce([
                campaign({ id: 'c2', status: 'upcoming', hasReminder: false }),
            ])
            .mockResolvedValueOnce([
                campaign({ id: 'c2', status: 'upcoming', hasReminder: true }),
            ]);

        renderWithIntl(<CampaignPage />);

        const remind = await screen.findByRole('button', { name: 'Remind me' });
        fireEvent.click(remind);
        await waitFor(() => expect(mockSetReminder).toHaveBeenCalledWith('c2'));

        await screen.findByRole('button', { name: /Reminder saved/ });
        // The toast is honest about what happened: stored, not delivered.
        expect(mockToast).toHaveBeenCalledWith('Reminder saved to your account.', 'ok');
    });
});
