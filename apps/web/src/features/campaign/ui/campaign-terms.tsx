'use client';

import { useLocale, useTranslations } from 'next-intl';
import type { MaterialTerms, QueryLegacyBaselineView } from '../model/contract.generated';
import { formatCampaignDate } from '../model/presentation';

/** Imported legacy values, verbatim and as plain text: no markup is parsed and
 * nothing becomes a link. */
export function CampaignBaselineTerms({ baseline }: { baseline: QueryLegacyBaselineView }) {
  const t = useTranslations('campaignV2');
  const locale = useLocale();
  return <div className="col gap-14">
    <p role="note">{t('legacyBaseline')}</p>
    {baseline.description !== null ? <p style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{baseline.description}</p> : null}
    {baseline.reward !== null ? <div><strong>{t('reward')}</strong><p style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{baseline.reward}</p><small>{t('rewardNotice')}</small></div> : null}
    <div className="col gap-6">
      <time dateTime={baseline.startAt}>{t('startsAt', { time: formatCampaignDate(baseline.startAt, locale) })}</time>
      <time dateTime={baseline.endAt}>{t('endsAt', { time: formatCampaignDate(baseline.endAt, locale) })}</time>
      <small><time dateTime={baseline.capturedAt}>{t('legacyCapturedAt', { time: formatCampaignDate(baseline.capturedAt, locale) })}</time></small>
    </div>
  </div>;
}

export function CampaignTerms({ terms }: { terms: MaterialTerms }) {
  const t = useTranslations('campaignV2');
  const locale = useLocale();
  const copy = terms.copy[locale === 'zh' ? 'zh' : 'en'];
  return <div className="col gap-14">
    <h3>{copy.title}</h3>
    <p style={{ whiteSpace: 'pre-wrap' }}>{copy.description}</p>
    <div><strong>{t('rules')}</strong><p style={{ whiteSpace: 'pre-wrap' }}>{copy.rules}</p></div>
    <div><strong>{t('reward')}</strong><p style={{ whiteSpace: 'pre-wrap' }}>{copy.rewardDescription}</p><p>{t('rewardNotice')}</p></div>
    <div className="col gap-6">
      <time dateTime={terms.startAt}>{t('startsAt', { time: formatCampaignDate(terms.startAt, locale) })}</time>
      <time dateTime={terms.endAt}>{t('endsAt', { time: formatCampaignDate(terms.endAt, locale) })}</time>
    </div>
  </div>;
}
