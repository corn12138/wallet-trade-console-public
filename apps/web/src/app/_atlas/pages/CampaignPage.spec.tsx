import { render, screen } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { CampaignPage } from './CampaignPage';

const auth = vi.hoisted(() => ({ isAuthenticated: true, isRestoring: false, isLoading: false, token: 'test-token', authenticatedAddress: '0xAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA', sessionGeneration: 3, retrySession: vi.fn() }));
const captured = vi.hoisted(() => vi.fn());
vi.mock('@/lib/web3/auth-provider', () => ({ useAuth: () => auth }));
vi.mock('../AppContext', () => ({ useApp: () => ({ openConnect: vi.fn() }) }));
vi.mock('@/features/campaign', () => ({ CampaignExperience: (props: unknown) => { captured(props); return <div>Campaign feature</div>; } }));

describe('CampaignPage composition', () => {
  beforeEach(() => { vi.clearAllMocks(); auth.isRestoring = false; auth.isLoading = false; auth.isAuthenticated = true; });
  it('passes the verified identity and stable route to the feature', () => {
    render(<CampaignPage id="stable-id" />);
    expect(screen.getByText('Campaign feature')).toBeInTheDocument();
    expect(captured).toHaveBeenLastCalledWith(expect.objectContaining({ id: 'stable-id', session: { owner: auth.authenticatedAddress.toLowerCase(), token: 'test-token', generation: 3 } }));
  });
  it('does not expose private scope while authentication is busy', () => {
    auth.isLoading = true; render(<CampaignPage />);
    expect(captured).toHaveBeenLastCalledWith(expect.objectContaining({ session: null }));
  });
  it('does not expose private scope while restoring the session', () => {
    auth.isRestoring = true; render(<CampaignPage history />);
    expect(captured).toHaveBeenLastCalledWith(expect.objectContaining({ history: true, session: null }));
  });
});
