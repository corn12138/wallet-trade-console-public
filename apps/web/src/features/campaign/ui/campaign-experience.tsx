'use client';

import Link from 'next/link';
import { useState } from 'react';
import { useMessages, useTranslations } from 'next-intl';
import { createCampaignHTTP } from '../adapters/http';
import { createCampaignPush } from '../adapters/push';
import { campaignCatalogReady } from '../model/catalog';
import type { CampaignAPI, CampaignSession } from '../model/ports';
import { CampaignBoundary } from './campaign-boundary';
import { CampaignDetailView, CampaignListView, type CampaignViewProps } from './campaign-current';
import { CampaignHistoryView } from './campaign-history';
import { LegacyCampaigns } from './legacy-campaigns';

type ExperienceProps = {
  id?: string; history?: boolean; enabled: boolean; api?: CampaignAPI; session: CampaignSession | null;
  onConnect(): void; onUnauthorized?(): void;
};

function VersionedCampaigns(props: CampaignViewProps & { id?: string; history?: boolean }) {
  const t = useTranslations('campaignV2');
  return <CampaignBoundary message={t('unavailable')} retry={t('retryRender')}>
    <section className="col gap-24">
      <header><h1 className="h-display">{t(props.history ? 'history' : 'title')}</h1><p>{t('intro')}</p></header>
      <nav className="row gap-10" aria-label={t('title')}>
        <Link className="btn btn-d btn-sm" href="/campaign">{t('publicList')}</Link>
        <Link className="btn btn-d btn-sm" href="/campaign/history">{t('viewHistory')}</Link>
      </nav>
      {props.history ? <CampaignHistoryView {...props} /> : props.id ? <CampaignDetailView {...props} id={props.id} /> : <CampaignListView {...props} />}
    </section>
  </CampaignBoundary>;
}

export function CampaignExperience(props: ExperienceProps) {
  const messages = useMessages();
  const [defaultAPI] = useState<CampaignAPI>(() => ({ ...createCampaignHTTP(), push: createCampaignPush() }));
  if (!campaignCatalogReady(messages)) return <LegacyCampaigns id={props.id} />;
  if (!props.enabled) return <LegacyCampaigns id={props.id} readonlyLabel={(messages.campaignV2 as Record<string, string>).readonly} />;
  return <VersionedCampaigns {...props} api={props.api ?? defaultAPI} />;
}
