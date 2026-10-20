'use client';

import { useEffect, useId, useRef } from 'react';
import { useTranslations } from 'next-intl';
import type { MaterialTerms } from '../model/contract.generated';
import type { InteractionOperation } from '../model/ports';
import { operationLabel } from '../model/presentation';
import { CampaignTerms } from './campaign-terms';

export interface CampaignConfirmationProps {
  operation: InteractionOperation;
  terms: MaterialTerms | null;
  changed: boolean;
  busy: boolean;
  onClose(): void;
  onConfirm(): void;
  onReview(): void;
}

export function CampaignConfirmation(props: CampaignConfirmationProps) {
  const t = useTranslations('campaignV2');
  const title = useId();
  const dialog = useRef<HTMLDialogElement>(null);
  const cancel = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    const previous = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    const element = dialog.current;
    if (element && !element.open) {
      if (typeof element.showModal === 'function') element.showModal();
      else element.setAttribute('open', '');
    }
    cancel.current?.focus();
    return () => {
      element?.close?.();
      if (previous?.isConnected) previous.focus();
    };
  }, []);
  return <dialog ref={dialog} className="block" aria-labelledby={title}
    style={{ width: 'min(620px, calc(100vw - 32px))', maxHeight: '85vh', overflowY: 'auto', padding: 24 }}
    onCancel={(event) => { event.preventDefault(); props.onClose(); }}>
    <div className="col gap-18">
      <h2 id={title}>{t('confirmTitle')} · {t(operationLabel(props.operation))}</h2>
      {props.terms ? <CampaignTerms terms={props.terms} /> : null}
      {props.operation.includes('reminder') ? <p>{t('reminderNotice')}</p> : null}
      {props.changed ? <p role="alert">{t('changed')}</p> : null}
      <div className="row gap-10" style={{ flexWrap: 'wrap' }}>
        <button ref={cancel} type="button" className="btn btn-sm" onClick={props.onClose}>{t('cancel')}</button>
        {props.changed ? <button type="button" className="btn btn-y btn-sm" disabled={props.busy} onClick={props.onReview}>{t('reviewLatest')}</button>
          : <button type="button" className="btn btn-y btn-sm" disabled={props.busy} onClick={props.onConfirm}>{t('confirm')}</button>}
      </div>
    </div>
  </dialog>;
}
