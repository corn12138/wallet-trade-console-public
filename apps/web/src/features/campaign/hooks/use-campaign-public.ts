'use client';

import { watchCampaignKeys } from '../adapters/push';
import { createCampaignRecovery } from '../model/event-recovery';
import type { QueryDetailResponse, QueryListResponse, QueryPublicCampaignView } from '../model/contract.generated';
import { CampaignAPIError, type CampaignAPI, type CampaignListQuery } from '../model/ports';
import { campaignQueryKey, campaignQueryKeys } from '../model/query-keys';
import { MAX_CAMPAIGN_ITEMS, mergePage, publicCampaign, timestamp, type PageSnapshot } from '../model/read-state';
import { useCampaignRead } from './use-campaign-read';

function nextTransition(items: readonly QueryPublicCampaignView[], serverNow: string): string | null {
  return items.flatMap((item) => item.nextTransitionAt === null ? [] : [item.nextTransitionAt])
    .filter((at) => timestamp(at) > timestamp(serverNow))
    .sort((a, b) => timestamp(a) - timestamp(b))[0] ?? null;
}

export function useCampaignList({ api, locale, query = {} }: { api: CampaignAPI; locale: string; query?: Omit<CampaignListQuery, 'cursor'> }) {
  const requestQuery = { ...query };
  const state = useCampaignRead<QueryListResponse, PageSnapshot<QueryPublicCampaignView>>({
    key: campaignQueryKey(campaignQueryKeys.list(locale, requestQuery)), identity: api,
    read: (signal, cursor) => api.list({ ...requestQuery, ...(cursor ? { cursor } : {}) }, signal),
    merge: (previous, next, mode) => mergePage(previous, next, mode, (item) => item.id, publicCampaign),
    cursor: (view) => view.items.length < MAX_CAMPAIGN_ITEMS && !view.hasUpdates ? view.nextCursor : null,
    transition: (wire, view) => nextTransition(view.items, wire.serverNow),
    covered: (wire, view) => {
      const shown = new Map(view.items.map((item) => [item.id, item]));
      return wire.items.filter((item) => shown.get(item.id) === item).map((item) => item.id);
    },
  });
  const { data, scope: _scope, ...result } = state;
  return { ...result, items: data?.items ?? [], nextCursor: data?.nextCursor ?? null, hasUpdates: data?.hasUpdates ?? false,
    hasMore: Boolean(data?.nextCursor && !data.hasUpdates && data.items.length < MAX_CAMPAIGN_ITEMS) };
}

export function useCampaignDetail({ api, locale, id }: { api: CampaignAPI; locale: string; id: string | null }) {
  const state = useCampaignRead<QueryDetailResponse, QueryPublicCampaignView>({
    key: id === null ? null : campaignQueryKey(campaignQueryKeys.detail(locale, id)), identity: api,
    read: (signal) => api.detail(id ?? '', signal),
    recover: api.events && id ? (read) => createCampaignRecovery(read, (['publication', 'interaction'] as const).map((family) => ({
      campaignId: id, family, read: (signal, cursor) => api.events!(id, family, { ...(cursor ? { cursor } : {}), limit: 50 }, signal),
    }))) : undefined,
    push: api.push && id ? (listener) => watchCampaignKeys(api.push!, (['publication', 'interaction'] as const).map((family) => ({ campaignId: id, family })), null, listener) : undefined,
    merge: (previous, next) => {
      if (next.item.id !== id) throw new CampaignAPIError('CAMPAIGN_RESPONSE_INVALID', 502);
      return publicCampaign(previous ?? undefined, next.item);
    },
    transition: (wire) => wire.item.nextTransitionAt,
    covered: (wire, view) => view === wire.item ? [view.id] : [],
  });
  const { data, scope: _scope, loadMore: _loadMore, isLoadingMore: _isLoadingMore, ...result } = state;
  return { ...result, item: data };
}
