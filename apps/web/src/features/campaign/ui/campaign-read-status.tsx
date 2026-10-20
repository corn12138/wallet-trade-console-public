'use client';

import { useTranslations } from 'next-intl';
import type { CampaignAPIError } from '../model/ports';

export function CampaignReadStatus({ status, error, empty, refresh, privateData = false }: {
  status: 'idle' | 'loading' | 'ready' | 'error'; error: CampaignAPIError | null;
  empty: boolean; refresh(): void; privateData?: boolean;
}) {
  const t = useTranslations('campaignV2');
  if (status === 'loading') return <p role="status">{t('loading')}</p>;
  if (status === 'error') return <div className="block col gap-10" role="alert">
    <p>{t(privateData ? 'privateUnavailable' : error?.status === 404 ? 'notFound' : 'unavailable')}</p>
    {error?.requestId ? <small>{t('requestId', { id: error.requestId })}</small> : null}
    <button type="button" className="btn btn-sm" onClick={refresh}>{t('refresh')}</button>
  </div>;
  if (status === 'ready' && empty) return <p role="status">{t(privateData ? 'historyEmpty' : 'empty')}</p>;
  return null;
}

export function CampaignPaging({ hasMore, hasUpdates, isLoadingMore, loadMore, refresh }: {
  hasMore: boolean; hasUpdates: boolean; isLoadingMore: boolean; loadMore(): void; refresh(): void;
}) {
  const t = useTranslations('campaignV2');
  return <div className="col gap-10">
    {hasUpdates ? <div role="status"><p>{t('updates')}</p><button type="button" className="btn btn-sm" onClick={refresh}>{t('refresh')}</button></div> : null}
    {hasMore ? <button type="button" className="btn btn-sm" disabled={isLoadingMore} onClick={loadMore}>{t(isLoadingMore ? 'loadingMore' : 'loadMore')}</button> : null}
  </div>;
}
