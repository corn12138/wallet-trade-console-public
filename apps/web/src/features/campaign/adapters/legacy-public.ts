import { buildApiUrl } from '@/lib/api/base-url';
import { boundedText } from './http-transport';
import { CampaignAPIError } from '../model/ports';

export type LegacyPublicCampaign = {
  id: string; title: string; description: string | null; startDate: string; endDate: string;
  status: 'upcoming' | 'active' | 'ended'; reward: string | null; participantCount: number;
};

function publicRow(input: unknown): LegacyPublicCampaign {
  if (!input || typeof input !== 'object' || Array.isArray(input)) throw new CampaignAPIError('CAMPAIGN_RESPONSE_INVALID', 502);
  const row = input as Record<string, unknown>;
  if (typeof row.id !== 'string' || !/^[a-zA-Z0-9_-]{1,128}$/.test(row.id)
    || typeof row.title !== 'string' || row.title.length > 600
    || !(row.description === null || typeof row.description === 'string')
    || !(row.reward === null || typeof row.reward === 'string')
    || typeof row.status !== 'string' || !['upcoming', 'active', 'ended'].includes(row.status)
    || typeof row.startDate !== 'string' || !Number.isFinite(Date.parse(row.startDate))
    || typeof row.endDate !== 'string' || !Number.isFinite(Date.parse(row.endDate))
    || typeof row.participantCount !== 'number' || !Number.isSafeInteger(row.participantCount) || row.participantCount < 0
    || 'isParticipating' in row || 'hasReminder' in row) throw new CampaignAPIError('CAMPAIGN_RESPONSE_INVALID', 502);
  // Only whitelisted public fields enter the view. Old metadata never becomes
  // a CTA, HTML source or a substitute for the versioned command contract.
  return { id: row.id, title: row.title, description: row.description, reward: row.reward,
    startDate: row.startDate, endDate: row.endDate, status: row.status as LegacyPublicCampaign['status'], participantCount: row.participantCount };
}

export async function readLegacyCampaigns(id: string | undefined, signal: AbortSignal): Promise<LegacyPublicCampaign[]> {
  if (id !== undefined && !/^[a-zA-Z0-9_-]{1,128}$/.test(id)) throw new CampaignAPIError('CAMPAIGN_INVALID_INPUT', 400);
  const response = await fetch(buildApiUrl(id ? `/campaign/${encodeURIComponent(id)}` : '/campaign?limit=20'), {
    signal, credentials: 'omit', cache: 'no-store', redirect: 'error', headers: { Accept: 'application/json' },
  });
  if (!response.ok) { void response.body?.cancel(); throw new CampaignAPIError('CAMPAIGN_UNAVAILABLE', response.status); }
  let parsed: unknown;
  try { parsed = JSON.parse(await boundedText(response, signal)); }
  catch { throw new CampaignAPIError('CAMPAIGN_RESPONSE_INVALID', 502); }
  const rows = id ? [parsed] : parsed;
  if (!Array.isArray(rows) || rows.length > 50) throw new CampaignAPIError('CAMPAIGN_RESPONSE_INVALID', 502);
  return rows.map(publicRow);
}
