import { fetchApi } from './auth-fetch';

/**
 * Durable campaign participation/reminders and profile follows (Go API,
 * SIWE-guarded mutations backed by campaign_participants /
 * campaign_reminders / profile_follows rows). fetchApi attaches the web3
 * token; a 401 means the wallet has no signed-in session.
 */

async function requestJSON<T>(pathname: string, init?: RequestInit): Promise<T> {
  const response = await fetchApi(pathname, init);
  if (!response.ok) {
    let message = `Request failed (${response.status})`;
    try {
      const body = (await response.json()) as { message?: string };
      if (body?.message) message = body.message;
    } catch {
      // keep the status fallback
    }
    const error = new Error(message) as Error & { status?: number };
    error.status = response.status;
    throw error;
  }
  return (await response.json()) as T;
}

export interface CampaignInteractionResult {
  campaignId: string;
  walletAddress: string;
  isParticipating?: boolean;
  hasReminder?: boolean;
  created?: boolean;
}

export function joinCampaign(campaignId: string) {
  return requestJSON<CampaignInteractionResult>(
    `/campaign/${encodeURIComponent(campaignId)}/join`,
    { method: 'POST' },
  );
}

export function setCampaignReminder(campaignId: string) {
  return requestJSON<CampaignInteractionResult>(
    `/campaign/${encodeURIComponent(campaignId)}/reminder`,
    { method: 'POST' },
  );
}

export function removeCampaignReminder(campaignId: string) {
  return requestJSON<CampaignInteractionResult>(
    `/campaign/${encodeURIComponent(campaignId)}/reminder`,
    { method: 'DELETE' },
  );
}

export interface ProfileFollowState {
  address: string;
  followers: number;
  following: number;
  isFollowing?: boolean;
}

export function getFollowState(address: string) {
  return requestJSON<ProfileFollowState>(
    `/profile/${encodeURIComponent(address)}/follow-state`,
  );
}

export function followProfile(address: string) {
  return requestJSON<{ address: string; isFollowing: boolean }>(
    `/profile/${encodeURIComponent(address)}/follow`,
    { method: 'POST' },
  );
}

export function unfollowProfile(address: string) {
  return requestJSON<{ address: string; isFollowing: boolean }>(
    `/profile/${encodeURIComponent(address)}/follow`,
    { method: 'DELETE' },
  );
}
