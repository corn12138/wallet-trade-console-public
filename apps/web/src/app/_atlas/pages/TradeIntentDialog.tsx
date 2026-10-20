'use client';

import { useRef, useState } from 'react';
import { useSwitchChain } from 'wagmi';
import { useTranslations } from 'next-intl';
import { formatUnits } from 'viem';
import { TxIntentModal, type TxIntentModalProps } from '../Common';
import { TxPreflight } from '../TxPreflight';
import { useApp } from '../AppContext';
import { buildTransactionExplorerUrl } from '@/lib/web3/explorer';
import { decodeTxError, technicalHint } from '@/hooks/web3/txIntent/errors';
import type { useTradingTransactions } from '@/hooks/useTradingTransactions';
import { buildTradePreflightInput } from './tradePreflight';

export function TradeIntentDialog({ transaction, close }: {
  transaction: ReturnType<typeof useTradingTransactions>;
  close: () => void;
}) {
  const t = useTranslations('trade.intent');
  const tError = useTranslations('atlasShell.modals.intent.err');
  const tTx = useTranslations('trade.tx');
  const app = useApp();
  const { switchChainAsync } = useSwitchChain();
  const [switching, setSwitching] = useState(false);
  const switchLock = useRef(false);
  const { intent } = transaction;
  const step = intent.activeStep;
  if (!step) return null;

  const invalidated = intent.state === 'DRAFT';
  const metadata = step.metadata ?? {};
  const isApproval = step.kind === 'approve';
  const title = isApproval ? t('approve') : step.actionType === 'open-position' ? t('open') : t('close');
  const summary = t('summary', {
    market: String(metadata.market ?? ''),
    side: metadata.isLong ? tTx('long') : tTx('short'),
    size: formatUnits(BigInt(String(metadata.size ?? '0')), 30),
  }) + (typeof metadata.leverage === 'number' ? ` · ${t('leverage', { leverage: metadata.leverage })}` : '');

  const props: TxIntentModalProps = {
    mode: 'intent',
    state: intent.state,
    phase: invalidated ? 'failed' : intent.phase,
    title,
    summary: isApproval ? `${summary} · ${t('approvalThenReview')}` : summary,
    step: { current: 1, total: intent.steps.length },
    review: step.review,
    expiresAt: step.expiresAt,
    expectsIndexing: step.expectsIndexing,
    indexing: intent.indexing,
    hash: intent.hash,
    replacedByHash: intent.replacedByHash,
    explorerUrl: intent.hash ? buildTransactionExplorerUrl(intent.hash, step.targetChainId) : null,
    errorCode: intent.errorCode,
    invalidationReason: intent.invalidationReason,
    technicalHint: technicalHint(intent.rawError),
    signLabel: intent.wrongChain ? t('switchChain', { chainId: step.targetChainId }) : undefined,
    signDisabled: switching,
    // Preflight receives the same frozen bytes as sendTransaction. It is an
    // advisory RPC simulation, not permission to skip the local send guards.
    preflight: <TxPreflight input={buildTradePreflightInput(step)} />,
    onSign: () => {
      if (switchLock.current) return;
      if (!intent.wrongChain) { void intent.send(); return; }
      setSwitching(true);
      switchLock.current = true;
      // A network switch never shares a click with a money-moving action.
      void switchChainAsync({ chainId: step.targetChainId as 11155111 }).then(() => {
        intent.reset();
        close();
        app.toast(t('chainSwitchedReview'), 'ok');
      }).catch((error: unknown) => {
        app.toast(tError(decodeTxError(error)), 'err');
      }).finally(() => { switchLock.current = false; setSwitching(false); });
    },
    onRetry: !intent.hash && !transaction.locked ? () => {
      const error = transaction.retry();
      if (error) app.toast(tError(error), 'err');
    } : undefined,
  };

  const dismiss = () => { if (!switchLock.current) close(); };
  return <TxIntentModal props={props} close={dismiss} onBg={(event) => {
    if (event.target === event.currentTarget) dismiss();
  }} />;
}
