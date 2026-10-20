'use client';

import Link from 'next/link';
import { useEffect, useState } from 'react';
import { useLocale, useTranslations } from 'next-intl';
import { readLegacyCampaigns, type LegacyPublicCampaign } from '../adapters/legacy-public';
import { formatCampaignDate } from '../model/presentation';

export function LegacyCampaigns({ id, readonlyLabel }: { id?: string; readonlyLabel?: string }) {
  const t = useTranslations('campaignAtlas');
  const locale = useLocale();
  const [state, setState] = useState<{ id?: string; rows: LegacyPublicCampaign[]; status: 'loading' | 'ready' | 'error' }>({ id, rows: [], status: 'loading' });
  useEffect(() => {
    const controller = new AbortController();
    let active = true;
    const timer = setTimeout(() => controller.abort(), 10_000);
    setState({ id, rows: [], status: 'loading' });
    void readLegacyCampaigns(id, controller.signal).then((rows) => {
      if (active) setState({ id, rows, status: 'ready' });
    }).catch(() => { if (active) setState({ id, rows: [], status: 'error' }); }).finally(() => clearTimeout(timer));
    return () => { active = false; controller.abort(); clearTimeout(timer); };
  }, [id]);
  const current = state.id === id ? state : { rows: [], status: 'loading' };
  return <section className="col gap-24">
    <h1 className="h-display">{t('headerTitle')}</h1>
    {readonlyLabel ? <p role="status">{readonlyLabel}</p> : null}
    {current.status === 'loading' ? <p role="status">{t('loading')}</p> : null}
    {current.status === 'error' ? <p role="alert">{t('errorTitle')}</p> : null}
    {current.status === 'ready' && current.rows.length === 0 ? <p>{t('emptyTitle')}</p> : null}
    <div className="grid-2">{current.rows.map((item) => <article key={item.id} className="block col gap-18">
      <span className="pill">{t(item.status === 'upcoming' ? 'statusUpcoming' : item.status === 'active' ? 'statusLive' : 'statusEnded')}</span>
      <h2>{item.title}</h2><p style={{ whiteSpace: 'pre-wrap' }}>{item.description}</p>
      <p>{t('prizePool')}: {item.reward}</p><p>{t('joinedCount', { count: item.participantCount })}</p>
      <time dateTime={item.status === 'upcoming' ? item.startDate : item.endDate}>{formatCampaignDate(item.status === 'upcoming' ? item.startDate : item.endDate, locale)}</time>
      {!id ? <Link className="btn btn-d btn-sm" href={`/campaign/${encodeURIComponent(item.id)}`}>{t('enter')}</Link> : null}
    </article>)}</div>
  </section>;
}
