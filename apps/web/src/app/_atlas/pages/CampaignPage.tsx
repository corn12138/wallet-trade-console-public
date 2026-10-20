'use client';

import { useMemo } from 'react';
import { CampaignExperience, type CampaignSession } from '@/features/campaign';
import { useAuth } from '@/lib/web3/auth-provider';
import { useApp } from '../AppContext';

export function CampaignPage({ id, history = false }: { id?: string; history?: boolean }) {
  const app = useApp();
  const auth = useAuth();
  const session = useMemo<CampaignSession | null>(() => auth.isAuthenticated && !auth.isRestoring && !auth.isLoading && auth.token && auth.authenticatedAddress
    ? { owner: auth.authenticatedAddress.toLowerCase(), generation: auth.sessionGeneration, token: auth.token } : null,
  [auth.isAuthenticated, auth.isRestoring, auth.isLoading, auth.token, auth.authenticatedAddress, auth.sessionGeneration]);
  return <CampaignExperience id={id} history={history} session={session} onConnect={app.openConnect}
    onUnauthorized={auth.retrySession} enabled={process.env.NEXT_PUBLIC_CAMPAIGN_V2_ENABLED === 'true'} />;
}
