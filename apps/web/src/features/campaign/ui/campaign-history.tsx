'use client';

import Link from 'next/link';
import { useLocale, useTranslations } from 'next-intl';
import { useCampaignHistory } from '../hooks/use-campaign-personal';
import { formatCampaignDate, operationLabel } from '../model/presentation';
import type { CampaignViewProps } from './campaign-current';
import { CampaignPaging, CampaignReadStatus } from './campaign-read-status';
import { CampaignTerms } from './campaign-terms';

export function CampaignHistoryView(props: CampaignViewProps) {
  const t = useTranslations('campaignV2');
  const locale = useLocale();
  const history = useCampaignHistory({ api: props.api, session: props.session });
  if (!props.session) return <button type="button" className="btn" onClick={props.onConnect}>{t('signIn')}</button>;
  return <div className="col gap-24">
    <p>{t('privacy')}</p>
    <CampaignReadStatus {...history} privateData empty={history.items.length === 0} />
    {history.error?.status === 401 ? <button type="button" className="btn btn-sm" onClick={props.onUnauthorized ?? props.onConnect}>{t('signIn')}</button> : null}
    {history.items.map((item) => {
      // An import is the previous system's record, never a command this
      // wallet submitted: it states when it was imported and recorded, and
      // that the accepted terms are unknown.
      const imported = item.entry === 'legacyImport' ? item.legacyImport ?? null : null;
      return <article className="block col gap-18" key={`${item.campaignId}:${item.kind}:${item.interactionVersion}`}>
        {imported ? <>
          <h2>{t(item.kind === 'reminder' ? 'importedReminder' : 'importedParticipation')}</h2>
          <small style={{ overflowWrap: 'anywhere' }}>{item.campaignId}</small>
          <p>{t('importedFrom')}</p>
          <p><time dateTime={imported.importedAt}>{t('importedAt', { time: formatCampaignDate(imported.importedAt, locale) })}</time></p>
          <p>{imported.legacyRecordedAt !== null ? t('legacyRecordedAt', { time: formatCampaignDate(imported.legacyRecordedAt, locale) }) : t('legacyRecordedUnknown')}</p>
        </> : <>
          <h2>{item.acceptedTerms?.copy[locale === 'zh' ? 'zh' : 'en'].title ?? t('historyUnknown')}</h2>
          <p>{item.operation ? t(operationLabel(item.operation)) : null} · <time dateTime={item.occurredAt}>{formatCampaignDate(item.occurredAt, locale)}</time></p>
          {item.snapshotState === 'ready' && item.acceptedTerms ? <details><summary>{t('acceptedTerms')}</summary><CampaignTerms terms={item.acceptedTerms} /></details>
            : <p>{t(item.snapshotState === 'legacyUnknown' ? 'legacyUnknown' : item.snapshotState === 'none' ? 'neverAccepted' : 'historyUnknown')}</p>}
        </>}
        {/* Historical row versions cannot authorize cancellation; detail reloads current Me. */}
        <Link className="btn btn-d btn-sm" href={`/campaign/${encodeURIComponent(item.campaignId)}`}>{t('currentState')}</Link>
      </article>;
    })}
    <CampaignPaging {...history} />
  </div>;
}
