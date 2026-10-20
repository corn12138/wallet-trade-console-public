'use client';

import { createCampaignRecovery } from '../model/event-recovery';
import type { QueryHistoryResponse, QueryHistoryView, QueryMeResponse, QueryMyCampaignView } from '../model/contract.generated';
import { CampaignAPIError, type CampaignAPI, type CampaignPageQuery, type CampaignSession } from '../model/ports';
import { campaignIds, campaignQueryKey, campaignQueryKeys } from '../model/query-keys';
import { MAX_CAMPAIGN_ITEMS, mergePage, personalCampaign, validatePersonalState, type PageSnapshot } from '../model/read-state';
import { useCampaignRead } from './use-campaign-read';

export function useCampaignMe({ api, session, ids }: { api: CampaignAPI; session: CampaignSession | null; ids: readonly string[] }) {
  const requestSession = session ? { ...session } : null;
  const selected = campaignIds(ids);
  const state = useCampaignRead<QueryMeResponse, QueryMyCampaignView[]>({
    key: requestSession && selected.length ? campaignQueryKey(campaignQueryKeys.me(requestSession, selected)) : null,
    identity: api, token: requestSession?.token, privateData: true,
    // Per-campaign streams stay bounded; larger groups keep their batched REST read.
    recover: api.meEvents && requestSession && selected.length === 1 ? (read) => createCampaignRecovery(read, [{
      campaignId: selected[0], family: 'personal',
      read: (signal, cursor) => api.meEvents!(requestSession, selected[0], { ...(cursor ? { cursor } : {}), limit: 50 }, signal),
    }]) : undefined,
    push: api.push && requestSession && selected.length === 1
      ? (listener) => api.push!.watch({ campaignId: selected[0], family: 'personal' }, requestSession, listener) : undefined,
    read: (signal) => {
      if (!requestSession) throw new CampaignAPIError('UNAUTHORIZED', 401);
      return api.me(requestSession, selected, signal);
    },
    merge: (previous, next) => {
      const incoming = new Set(next.items.map((item) => item.id));
      // A successful but incomplete Me response does not prove absence for a
      // missing ID; only an explicit server-side `state: absent` can do that.
      if (incoming.size !== selected.length || next.items.length !== selected.length || selected.some((id) => !incoming.has(id))) throw new CampaignAPIError('CAMPAIGN_RESPONSE_INVALID', 502);
      const current = new Map(previous?.map((item) => [item.id, item]));
      return next.items.map((item) => {
        validatePersonalState(item.participation, 'join');
        validatePersonalState(item.reminder, 'reminder');
        return personalCampaign(current.get(item.id), item);
      });
    },
    covered: (wire, view) => view.filter((row) => {
      const read = wire.items.find((item) => item.id === row.id);
      return read && read.publicationVersion === row.publicationVersion && read.interactionVersion === row.interactionVersion && read.participation === row.participation && read.reminder === row.reminder;
    }).map((item) => item.id),
  });
  const { data, scope: _scope, loadMore: _loadMore, isLoadingMore: _isLoadingMore, ...result } = state;
  return { ...result, items: data ?? [] };
}

export function useCampaignHistory({ api, session, query = {} }: { api: CampaignAPI; session: CampaignSession | null; query?: Omit<CampaignPageQuery, 'cursor'> }) {
  const requestSession = session ? { ...session } : null;
  const requestQuery = { ...query };
  const state = useCampaignRead<QueryHistoryResponse, PageSnapshot<QueryHistoryView>>({
    key: requestSession ? campaignQueryKey(campaignQueryKeys.history(requestSession, requestQuery.limit ?? 20)) : null,
    identity: api, token: requestSession?.token, privateData: true,
    read: (signal, cursor) => {
      if (!requestSession) throw new CampaignAPIError('UNAUTHORIZED', 401);
      return api.history(requestSession, { ...requestQuery, ...(cursor ? { cursor } : {}) }, signal);
    },
    merge: (previous, next, mode) => {
      for (const item of next.items) validatePersonalState(item.state, item.kind);
      return mergePage(previous, next, mode, (item) => `${item.campaignId}:${item.kind}:${item.interactionVersion}`, (_old, item) => item);
    },
    cursor: (view) => view.items.length < MAX_CAMPAIGN_ITEMS && !view.hasUpdates ? view.nextCursor : null,
  });
  const { data, scope: _scope, ...result } = state;
  return { ...result, items: data?.items ?? [], nextCursor: data?.nextCursor ?? null, hasUpdates: data?.hasUpdates ?? false,
    hasMore: Boolean(data?.nextCursor && !data.hasUpdates && data.items.length < MAX_CAMPAIGN_ITEMS) };
}
