import type { CampaignListQuery, CampaignSession } from './ports';
import { CAMPAIGN_HISTORY_VARIANTS, CAMPAIGN_PUBLIC_VARIANTS } from './variants';

export const campaignIds = (ids: readonly string[]) => [...new Set(ids)].sort();

// The representation a read negotiated is part of its identity: a cached base
// response is never reused for a negotiated read, or the reverse.
const publicSchema = ['1', ...CAMPAIGN_PUBLIC_VARIANTS].join('+');
const historySchema = ['1', ...CAMPAIGN_HISTORY_VARIANTS].join('+');

export const campaignQueryKeys = {
  list: (locale: string, query: Omit<CampaignListQuery, 'cursor'>) => ['campaign', 'public', publicSchema, locale, 'list', query.phase ?? '', query.limit ?? 20] as const,
  detail: (locale: string, id: string) => ['campaign', 'public', publicSchema, locale, 'detail', id] as const,
  me: (session: CampaignSession, ids: readonly string[]) => ['campaign', 'me', 1, session.owner.toLowerCase(), session.generation, campaignIds(ids)] as const,
  history: (session: CampaignSession, limit: number) => ['campaign', 'history', historySchema, session.owner.toLowerCase(), session.generation, limit] as const,
};

// Tokens never enter cache keys, logs or a shared public query's identity.
export const campaignQueryKey = (parts: readonly unknown[]) => JSON.stringify(parts);
