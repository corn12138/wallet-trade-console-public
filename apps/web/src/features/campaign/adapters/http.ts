import { CampaignAPIError, type CampaignAPI, type CampaignListQuery, type CampaignPageQuery } from '../model/ports';
import { CAMPAIGN_HISTORY_VARIANTS, CAMPAIGN_PUBLIC_VARIANTS, validHistoryResponse, validPublicCampaignView } from '../model/variants';
import { createTransport, encodeInput, type CampaignHTTPOptions } from './http-transport';

function id(value: string): string {
  if (!/^[a-zA-Z0-9_-]{1,128}$/.test(value) || value.includes('\n')) throw new CampaignAPIError('CAMPAIGN_INVALID_INPUT', 400);
  return encodeURIComponent(value);
}
function uuid(value: string, command = false): string {
  const version = command ? '7' : '[1-8]';
  if (!new RegExp(`^[0-9a-f]{8}-[0-9a-f]{4}-${version}[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).test(value) || value.includes('\n')) throw new CampaignAPIError('CAMPAIGN_INVALID_INPUT', 400);
  return value;
}
// Cross-field rules run with the decoder, before a response reaches a cache.
function checked<T>(value: T, valid: (value: T) => boolean): T {
  if (!valid(value)) throw new CampaignAPIError('CAMPAIGN_RESPONSE_INVALID', 502);
  return value;
}

function query(value: CampaignListQuery | CampaignPageQuery): string {
  const params = new URLSearchParams();
  if ('phase' in value && value.phase !== undefined) {
    if (!['upcoming', 'active', 'ended', 'unsupported'].includes(value.phase)) throw new CampaignAPIError('CAMPAIGN_INVALID_INPUT', 400);
    params.set('phase', value.phase);
  }
  if (value.cursor !== undefined) {
    if (!/^[A-Za-z0-9_-]+$/.test(value.cursor) || value.cursor.length > 2048 || value.cursor.includes('\n')) throw new CampaignAPIError('CAMPAIGN_INVALID_INPUT', 400);
    params.set('cursor', value.cursor);
  }
  if (value.limit !== undefined) {
    if (!Number.isInteger(value.limit) || value.limit < 1 || value.limit > 50) throw new CampaignAPIError('CAMPAIGN_INVALID_INPUT', 400);
    params.set('limit', String(value.limit));
  }
  const encoded = params.toString();
  return encoded ? `?${encoded}` : '';
}

function eventQuery(value: CampaignPageQuery): string {
  if (value.cursor !== undefined && value.cursor.length > 1024) throw new CampaignAPIError('CAMPAIGN_INVALID_INPUT', 400);
  return query(value);
}

/** The adapter owns routes and wire validation, never session discovery. A
 * public read cannot inherit credentials from cookies or localStorage. */
export function createCampaignHTTP(options: CampaignHTTPOptions = {}): CampaignAPI {
  const request = createTransport(options);
  return {
    events: async (campaign, family, q, signal) => {
      if (family !== 'publication' && family !== 'interaction') throw new CampaignAPIError('CAMPAIGN_INVALID_INPUT', 400);
      const suffix = eventQuery(q);
      return request('EventFeedResponse', `/campaigns/${id(campaign)}/events${suffix || '?'}${suffix ? '&' : ''}family=${family}`, signal);
    },
    meEvents: async (session, campaign, q, signal) => request('EventFeedResponse', `/campaigns/${id(campaign)}/me/events${eventQuery(q)}`, signal, session),
    list: async (q, signal) => checked(await request('QueryListResponse', `/campaigns${query(q)}`, signal, undefined, undefined, 'GET', CAMPAIGN_PUBLIC_VARIANTS),
      (page) => page.items.every((item) => validPublicCampaignView(item, CAMPAIGN_PUBLIC_VARIANTS))),
    detail: async (campaign, signal) => checked(await request('QueryDetailResponse', `/campaigns/${id(campaign)}`, signal, undefined, undefined, 'GET', CAMPAIGN_PUBLIC_VARIANTS),
      (value) => validPublicCampaignView(value.item, CAMPAIGN_PUBLIC_VARIANTS)),
    me: async (session, ids, signal) => {
      const selected = [...new Set(ids)].sort();
      if (!selected.length || ids.length > 50) return Promise.reject(new CampaignAPIError('CAMPAIGN_INVALID_INPUT', 400));
      selected.forEach(id);
      return request('QueryMeResponse', `/campaigns/me?${new URLSearchParams({ ids: selected.join(',') })}`, signal, session);
    },
    history: async (session, q, signal) => checked(await request('QueryHistoryResponse', `/campaigns/me/history${query(q)}`, signal, session, undefined, 'GET', CAMPAIGN_HISTORY_VARIANTS),
      (value) => validHistoryResponse(value, CAMPAIGN_HISTORY_VARIANTS)),
    command: async (session, campaign, operation, commandId, signal) => {
      if (!['join', 'leave', 'set-reminder', 'remove-reminder'].includes(operation)) return Promise.reject(new CampaignAPIError('CAMPAIGN_INVALID_INPUT', 400));
      return request('CommandResponse', `/campaigns/${id(campaign)}/commands/${uuid(commandId, true)}?operation=${operation}`, signal, session);
    },
    execute: async (session, campaign, command, signal) => {
      const positive = command.operation === 'join' || command.operation === 'set-reminder';
      if (!['join', 'leave', 'set-reminder', 'remove-reminder'].includes(command.operation)) return Promise.reject(new CampaignAPIError('CAMPAIGN_INVALID_INPUT', 400));
      const suffix = command.operation === 'join' || command.operation === 'leave' ? 'join' : 'reminder';
      return request('CommandResponse', `/campaigns/${id(campaign)}/${suffix}`, signal, session, encodeInput(positive ? 'PositiveInteractionInput' : 'CancelInteractionInput', command.input), positive ? 'POST' : 'DELETE');
    },
    create: async (session, input, signal) => request('AdminCommandResponse', '/campaign-admin/campaigns', signal, session, encodeInput('CreateCommandInput', input), 'POST'),
    revision: async (session, campaign, input, signal) => request('AdminCommandResponse', `/campaign-admin/campaigns/${id(campaign)}/revisions`, signal, session, encodeInput('RevisionCommandInput', input), 'POST'),
    publication: async (session, campaign, input, signal) => request('AdminCommandResponse', `/campaign-admin/campaigns/${id(campaign)}/publications`, signal, session, encodeInput('PublicationCommandInput', input), 'POST'),
    preview: async (session, campaign, revision, signal) => request('QueryPreviewResponse', `/campaign-admin/campaigns/${id(campaign)}/revisions/${uuid(revision)}/preview`, signal, session),
    adminCommand: async (session, target, operation, commandId, signal) => {
      if (!['create', 'revision', 'publish', 'pause', 'resume', 'archive', 'rollback'].includes(operation) || operation === 'create' && target !== 'campaigns') return Promise.reject(new CampaignAPIError('CAMPAIGN_INVALID_INPUT', 400));
      const base = operation === 'create' ? '/campaign-admin' : `/campaign-admin/campaigns/${id(target)}`;
      return request('AdminCommandResponse', `${base}/commands/${uuid(commandId, true)}?operation=${operation}`, signal, session);
    },
  };
}
