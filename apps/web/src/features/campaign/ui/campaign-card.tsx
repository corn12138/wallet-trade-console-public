'use client';

import Image from 'next/image';
import Link from 'next/link';
import { useState } from 'react';
import { useLocale, useTranslations } from 'next-intl';
import { tradeEntry } from '@/features/trade';
import type { MaterialTerms, QueryMyCampaignView, QueryPublicCampaignView } from '../model/contract.generated';
import type { CampaignAPI, CampaignSession, InteractionOperation } from '../model/ports';
import { campaignActionAllowed, campaignBaseline, campaignCopy, formatCampaignDate, knownPersonal, localCapabilitiesReady, operationLabel, stateForOperation } from '../model/presentation';
import { useCampaignCommand } from '../hooks/use-campaign-command';
import { trustedMedia } from '../model/media';
import { CampaignConfirmation } from './campaign-confirmation';
import { CampaignBaselineTerms, CampaignTerms } from './campaign-terms';

type Selection = { operation: InteractionOperation; publicationVersion: string; stateVersion: string; terms: MaterialTerms | null; scope: string };
export interface CampaignCardProps {
  api: CampaignAPI;
  campaignId: string;
  /** Null when the campaign has no public detail but the wallet has a
   * recorded state; the card then offers only cancellation. */
  item: QueryPublicCampaignView | null;
  personal: QueryMyCampaignView | null;
  session: CampaignSession | null;
  publicReady: boolean;
  personalReady: boolean;
  detailed?: boolean;
  onConnect(): void;
  onRefresh(): void;
  onUnauthorized?(): void;
}

export function CampaignCard(props: CampaignCardProps) {
  const { item, personal, session, campaignId } = props;
  const t = useTranslations('campaignV2');
  const locale = useLocale();
  const scope = session ? `${session.owner}:${session.generation}` : '';
  const [selection, setSelection] = useState<Selection | null>(null);
  const selected = selection?.scope === scope ? selection : null;
  const command = useCampaignCommand({ api: props.api, session, campaignId, onCurrent: props.onRefresh, onUnauthorized: props.onUnauthorized });
  const busy = ['submitting', 'unknown', 'reconciling'].includes(command.state.status);
  const copy = item ? campaignCopy(item, locale) : null;
  const baseline = item ? campaignBaseline(item) : null;
  const mediaId = item?.content?.displayContent.mediaAssetId;
  const media = trustedMedia(mediaId, locale);
  const cta = item?.content?.cta;
  // The trade feature decides whether this build can open the market.
  const trade = cta?.actionKind === 'openTrade' && cta.chainId !== undefined && cta.marketId !== undefined ? tradeEntry({ chainId: cta.chainId, marketId: cta.marketId }) : null;
  // Ready on the server but not fully renderable here: read-only in this build.
  const capabilityMissing = item?.contentState === 'ready' && !!item.content && !localCapabilitiesReady(item.content);
  const phase = item && ['active', 'upcoming', 'ended'].includes(item.effectivePhase) ? item.effectivePhase as 'active' | 'upcoming' | 'ended' : null;
  const positiveReady = !!item && props.publicReady && !!personal && personal.publicationVersion === item.publicationVersion;
  const current = command.state.response?.current.state;
  const awaitingCurrent = !!current && !!personal && BigInt(stateForOperation(personal, current.kind === 'join' ? 'join' : 'set-reminder')?.stateVersion ?? '0') < BigInt(current.stateVersion);
  const canChoose = (operation: InteractionOperation) => !!session && props.personalReady && command.state.storageAvailable && !awaitingCurrent && !busy
    && (command.state.status !== 'requiresReview' || command.state.reviewReady)
    && campaignActionAllowed(item, personal, operation, positiveReady);
  const choose = (operation: InteractionOperation) => {
    const state = stateForOperation(personal, operation);
    if (!canChoose(operation) || !knownPersonal(state)) return;
    setSelection({ operation, publicationVersion: item?.publicationVersion ?? '', stateVersion: state.stateVersion,
      terms: operation === 'join' || operation === 'set-reminder' ? item?.content?.materialTerms ?? null : null, scope });
  };
  const selectedState = selected ? stateForOperation(personal, selected.operation) : null;
  const changed = !!selected && (selected.stateVersion !== selectedState?.stateVersion
    || ((selected.operation === 'join' || selected.operation === 'set-reminder') && selected.publicationVersion !== item?.publicationVersion));
  const confirm = () => {
    if (!selected || changed || !canChoose(selected.operation)) return;
    const versions = { expectedStateVersion: selected.stateVersion };
    const intent = selected.operation === 'join' || selected.operation === 'set-reminder'
      ? { operation: selected.operation, ...versions, expectedPublicationVersion: selected.publicationVersion }
      : { operation: selected.operation, ...versions };
    setSelection(null);
    void command.confirm(intent);
  };
  const showStatus = (kind: 'participation' | 'reminder') => {
    const state = stateForOperation(personal, kind === 'participation' ? 'join' : 'set-reminder');
    if (!state || !knownPersonal(state)) return t('unknownState');
    if (state.state === 'absent') return t(kind === 'participation' ? 'left' : 'reminderRemoved');
    if (kind === 'participation') return t(state.status === 'joined' ? 'joined' : 'left');
    return t(state.status === 'set' ? 'reminderSet' : 'reminderRemoved');
  };

  return <article className="block col gap-18" data-campaign-id={campaignId}>
    {item ? <>
    <div className="row gap-10" style={{ flexWrap: 'wrap' }}>
      {phase ? <span className={`pill ${phase === 'active' ? 'live' : ''}`}>{t(phase)}</span> : null}
      {item.mode === 'paused' || item.mode === 'archived' ? <span className="pill">{t(item.mode)}</span> : null}
    </div>
    <h2 className="h-display" style={{ fontSize: 26, overflowWrap: 'anywhere' }}>{copy?.title ?? (baseline ? baseline.title || t('legacyUntitled') : t('unsupported'))}</h2>
    {copy ? <>
      <p style={{ whiteSpace: 'pre-wrap' }}>{copy.description}</p>
      <div><strong>{t('reward')}</strong><p style={{ whiteSpace: 'pre-wrap' }}>{copy.rewardDescription}</p><small>{t('rewardNotice')}</small></div>
      {item.content ? <time dateTime={phase === 'upcoming' ? item.content.materialTerms.startAt : item.content.materialTerms.endAt}>
        {t(phase === 'upcoming' ? 'startsAt' : 'endsAt', { time: formatCampaignDate(phase === 'upcoming' ? item.content.materialTerms.startAt : item.content.materialTerms.endAt, locale) })}
      </time> : null}
    </> : baseline ? <CampaignBaselineTerms baseline={baseline} /> : <p role="status">{t('unsupported')}</p>}
    <p>{item.statisticsState === 'ready' && item.participantCount !== null
      ? t('count', { count: new Intl.NumberFormat(locale).format(BigInt(item.participantCount)) }) : t('unknownCount')}</p>
    {media ? <Image src={media.path} width={media.width} height={media.height} alt={media.alt} sizes="(max-width: 768px) 100vw, 50vw"
      style={{ width: '100%', height: 'auto', borderRadius: 12 }} />
      : mediaId !== undefined ? <p>{t('mediaUnavailable')}</p> : null}
    {trade ? <div className="col gap-6">
      <Link className="btn btn-sm" href={trade.href}>{t('openTrade', { market: trade.marketId })}</Link>
      <small>{t('tradeNotice')}</small>
    </div> : cta?.actionKind === 'openTrade' ? <p>{t('tradeUnavailable')}</p> : null}
    </> : <>
      <h2 className="h-display" style={{ fontSize: 26, overflowWrap: 'anywhere' }}>{campaignId}</h2>
      <p role="note">{t('personalOnly')}</p>
    </>}
    {session ? <div className="col gap-10">
      <p aria-live="polite">{t('currentState')}: {showStatus('participation')} · {showStatus('reminder')}</p>
      {!props.personalReady ? <p role="status">{t('privateUnavailable')}</p> : null}
      <div className="row gap-10" style={{ flexWrap: 'wrap' }}>
        {(['join', 'leave', 'set-reminder', 'remove-reminder'] as const).map((operation) => {
          const state = stateForOperation(personal, operation);
          const visible = operation === 'leave' ? state?.status === 'joined' : operation === 'remove-reminder' ? state?.status === 'set'
            : baseline || capabilityMissing ? false : operation === 'join' ? phase === 'active' && state?.status !== 'joined' : phase === 'upcoming' && state?.status !== 'set';
          return visible ? <button type="button" key={operation} className="btn btn-sm" disabled={!canChoose(operation)} onClick={() => choose(operation)}>{t(operationLabel(operation))}</button> : null;
        })}
      </div>
      {capabilityMissing ? <p role="note">{t('capabilityUnavailable')}</p> : null}
      <p><small>{t('reminderNotice')}</small></p>
    </div> : <button type="button" className="btn btn-sm" onClick={props.onConnect}>{t('signIn')}</button>}
    {command.state.status !== 'idle' || command.state.error ? <div role="status" aria-live="polite" className="col gap-10">
      <p>{t(command.state.status === 'unknown' || command.state.status === 'reconciling' ? 'unknownResult'
        : command.state.status === 'submitting' ? 'submitting' : command.state.status === 'settled' ? 'settled'
          : command.state.status === 'requiresReview' ? 'requiresReview' : 'actionFailed')}</p>
      {!command.state.storageAvailable ? <p>{t('storageUnavailable')}</p> : null}
      {command.state.status === 'unknown' || command.state.status === 'requiresReview' || command.state.status === 'settled' && !command.state.storageAvailable ? <div className="row gap-10" style={{ flexWrap: 'wrap' }}>
        <button type="button" className="btn btn-sm" onClick={() => void command.reconcile()}>{t('reconcile')}</button>
        {command.state.status === 'unknown' && command.state.canRetry ? <button type="button" className="btn btn-sm" onClick={() => void command.retry()}>{t('retrySame')}</button> : null}
      </div> : null}
    </div> : null}
    {props.detailed || !item ? item?.content ? <details><summary>{t('rules')}</summary><CampaignTerms terms={item.content.materialTerms} /></details> : null
      : <Link className="btn btn-d btn-sm" href={`/campaign/${encodeURIComponent(campaignId)}`}>{t('details')}</Link>}
    {selected ? <CampaignConfirmation operation={selected.operation} terms={selected.terms} changed={changed}
      busy={busy || !props.personalReady || !canChoose(selected.operation)} onClose={() => setSelection(null)} onConfirm={confirm}
      onReview={() => choose(selected.operation)} /> : null}
  </article>;
}
