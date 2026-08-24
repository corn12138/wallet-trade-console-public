'use client';
/**
 * Shared typed data-state primitives for the Atlas routes.
 *
 * Every data panel on the screenshot routes distinguishes, machine-readably:
 *   - loading            (request in flight, nothing cached)
 *   - wallet-required    (query is wallet-scoped and no wallet is connected)
 *   - siwe-required      (endpoint is SIWE-guarded and session is missing)
 *   - error              (request failed — service down/unreachable)
 *   - empty              (API responded 2xx with zero rows — real, honest emptiness)
 *   - data               (rows present)
 *
 * SourceMeta renders the "what real source was queried and how it refreshes"
 * strip: endpoint, chain, wallet scope, last refresh time, polling interval,
 * and a manual refresh button. No fabricated numbers — timestamps come from
 * react-query's dataUpdatedAt.
 */
import React, { useEffect, useState } from 'react';
import { useTranslations, useLocale } from 'next-intl';
import { Icon, type IconName } from './Icon';
import { ProductStatusHint, useProductStatus } from './ProductStatus';

export type DataPanelState =
  | 'loading'
  | 'wallet-required'
  | 'siwe-required'
  | 'error'
  | 'empty'
  | 'data';

export function deriveDataPanelState(input: {
  walletRequired?: boolean;
  hasWallet?: boolean;
  siweRequired?: boolean;
  isAuthenticated?: boolean;
  isLoading: boolean;
  isError: boolean;
  rowCount: number;
}): DataPanelState {
  if (input.walletRequired && !input.hasWallet) return 'wallet-required';
  if (input.siweRequired && !input.isAuthenticated) return 'siwe-required';
  if (input.isLoading) return 'loading';
  if (input.isError) return 'error';
  if (input.rowCount === 0) return 'empty';
  return 'data';
}

/** Ticks every `intervalMs` so relative times stay fresh. */
export function useNow(intervalMs = 1000): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), intervalMs);
    return () => clearInterval(id);
  }, [intervalMs]);
  return now;
}

export function formatRelativeShort(deltaMs: number): string {
  const s = Math.max(0, Math.floor(deltaMs / 1000));
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h`;
  return `${Math.floor(h / 24)}d`;
}

export type SourceMetaProps = {
  /** Real endpoint path, e.g. "GET /api/trading/candles". */
  endpoint: string;
  chainId?: number;
  wallet?: string | null;
  /** react-query dataUpdatedAt (0 = never). */
  updatedAt?: number;
  refetchIntervalMs?: number;
  rowCount?: number;
  state: DataPanelState;
  isFetching?: boolean;
  onRefresh?: () => void;
};

const STATE_PILL: Record<DataPanelState, { tone: string; labelKey: 'statusLive' | 'statusEmpty' | 'statusError' }> = {
  data: { tone: 'var(--g)', labelKey: 'statusLive' },
  empty: { tone: 'var(--y)', labelKey: 'statusEmpty' },
  loading: { tone: 'var(--bg-3)', labelKey: 'statusEmpty' },
  error: { tone: 'var(--o)', labelKey: 'statusError' },
  'wallet-required': { tone: 'var(--bg-3)', labelKey: 'statusEmpty' },
  'siwe-required': { tone: 'var(--bg-3)', labelKey: 'statusEmpty' },
};

export function SourceMeta({
  endpoint,
  chainId,
  wallet,
  updatedAt,
  refetchIntervalMs,
  rowCount,
  state,
  isFetching,
  onRefresh,
}: SourceMetaProps) {
  const t = useTranslations('dataHealth');
  const locale = useLocale();
  const now = useNow(1000);

  const chips: React.ReactNode[] = [];
  chips.push(
    <span key="endpoint" className="src-chip" data-testid="source-endpoint">
      {endpoint}
    </span>,
  );
  if (chainId !== undefined) {
    chips.push(
      <span key="chain" className="src-chip">
        {t('chain')} {chainId}
      </span>,
    );
  }
  if (wallet) {
    chips.push(
      <span key="wallet" className="src-chip">
        {t('wallet')} {wallet.slice(0, 6)}…{wallet.slice(-4)}
      </span>,
    );
  }
  if (updatedAt && updatedAt > 0) {
    const abs = new Intl.DateTimeFormat(locale, {
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
    }).format(updatedAt);
    chips.push(
      <span key="updated" className="src-chip" data-testid="source-updated">
        {t('lastUpdated')} {abs} · {formatRelativeShort(now - updatedAt)}
      </span>,
    );
  } else {
    chips.push(
      <span key="updated" className="src-chip">
        {t('lastUpdated')} {t('never')}
      </span>,
    );
  }
  chips.push(
    <span key="interval" className="src-chip">
      {refetchIntervalMs
        ? t('refreshEvery', { seconds: Math.round(refetchIntervalMs / 1000) })
        : t('manualRefresh')}
    </span>,
  );
  if (rowCount !== undefined && (state === 'data' || state === 'empty')) {
    chips.push(
      <span key="rows" className="src-chip">
        {t('rows', { count: rowCount })}
      </span>,
    );
  }

  const pill = STATE_PILL[state];

  return (
    <div className="src-meta" data-testid="source-meta" data-state={state}>
      <span
        className="src-chip src-state"
        style={{ background: pill.tone, color: state === 'error' ? '#fff' : 'var(--ink)' }}
      >
        {t(pill.labelKey)}
      </span>
      {chips}
      {onRefresh && (
        <button
          type="button"
          className="src-refresh"
          onClick={onRefresh}
          disabled={isFetching}
          aria-label={t('refreshNow')}
          data-testid="source-refresh"
        >
          <Icon name="refresh" size={11} /> {isFetching ? t('refreshing') : t('refreshNow')}
        </button>
      )}
    </div>
  );
}

/**
 * Loading placeholders shaped like what replaces them.
 *
 * A centred spinner is honest about "loading" and silent about size, so the
 * page reflows the moment data lands. Reserving the space up front removes
 * that jump — the same reasoning as the sidebar overlay, applied to the
 * network instead of the cursor.
 *
 * Deliberately NOT the default: a skeleton that guesses the wrong shape is
 * worse than a spinner, because it promises a layout and then breaks it. A
 * caller opts in when it knows what is coming.
 */
export type SkeletonShape = 'rows' | 'cards';

export function DataSkeleton({
  shape,
  count = 4,
}: {
  shape: SkeletonShape;
  count?: number;
}) {
  // aria-hidden + aria-busy on the wrapper: assistive tech should hear "busy",
  // not have every placeholder bar read out as content.
  if (shape === 'cards') {
    return (
      <div
        className="skel-grid"
        style={{ gridTemplateColumns: 'repeat(auto-fill, minmax(220px, 1fr))' }}
        aria-busy="true"
        data-testid="data-skeleton"
        data-shape="cards"
      >
        {Array.from({ length: count }, (_, i) => (
          <div key={i} className="block" style={{ padding: 18 }} aria-hidden="true">
            <div className="skel skel-line" style={{ width: '45%' }} />
            <div className="skel" style={{ height: 26, marginTop: 12, width: '72%' }} />
            <div className="skel skel-line" style={{ marginTop: 12, width: '55%' }} />
          </div>
        ))}
      </div>
    );
  }
  return (
    <div className="block" aria-busy="true" data-testid="data-skeleton" data-shape="rows">
      {Array.from({ length: count }, (_, i) => (
        <div key={i} className="skel-row" aria-hidden="true">
          <div className="skel skel-dot" />
          <div style={{ flex: 1 }}>
            <div className="skel skel-line" style={{ width: `${58 - (i % 3) * 9}%` }} />
            <div className="skel skel-line" style={{ width: '32%', marginTop: 8, height: 10 }} />
          </div>
          <div className="skel skel-line" style={{ width: 72 }} />
        </div>
      ))}
    </div>
  );
}

/**
 * Header for the diagnostics block in the error state. Separate so it renders
 * only when there is something to introduce — a heading above an empty list
 * is its own small lie.
 */
function ProductStatusHintLabel() {
  const t = useTranslations('dataHealth');
  const { data } = useProductStatus();
  if (!data?.warnings?.length) return null;
  return <div style={{ marginBottom: 6 }}>{t('activeDiagnostics')}</div>;
}

export type DataStatePanelProps = {
  state: DataPanelState;
  endpoint: string;
  /** Route-specific honest explanation for the empty (2xx, zero rows) case. */
  emptyTitle: string;
  emptyBody: React.ReactNode;
  errorDetail?: string | null;
  icon?: IconName;
  onRetry?: () => void;
  onConnect?: () => void;
  /** Extra action rendered in the empty state (e.g. "Launch a token"). */
  emptyAction?: React.ReactNode;
  /**
   * Render placeholders shaped like the incoming content instead of a centred
   * spinner. Opt-in: only pass a shape the caller actually renders.
   */
  skeleton?: SkeletonShape;
  skeletonCount?: number;
};

/**
 * Standard body for non-data states. Renders nothing when state === 'data'
 * (the caller renders the real rows).
 */
export function DataStatePanel({
  state,
  endpoint,
  emptyTitle,
  emptyBody,
  errorDetail,
  icon = 'cube',
  onRetry,
  onConnect,
  emptyAction,
  skeleton,
  skeletonCount,
}: DataStatePanelProps) {
  const t = useTranslations('dataHealth');
  const tc = useTranslations('commonAtlas');
  if (state === 'data') return null;
  if (state === 'loading' && skeleton) {
    return <DataSkeleton shape={skeleton} count={skeletonCount} />;
  }

  let title: string;
  let body: React.ReactNode;
  let actions: React.ReactNode = null;
  let iconName: IconName = icon;

  switch (state) {
    case 'loading':
      title = tc('loading');
      body = t('stateLoading', { endpoint });
      break;
    case 'wallet-required':
      title = tc('watchModeActive');
      body = t('stateWalletRequired');
      iconName = 'wallet';
      actions = onConnect ? (
        <button className="btn btn-sm btn-y" onClick={onConnect} data-testid="state-connect">
          {tc('connectWallet')}
        </button>
      ) : null;
      break;
    case 'siwe-required':
      title = tc('watchModeActive');
      body = t('stateSiweRequired');
      iconName = 'security';
      actions = onConnect ? (
        <button className="btn btn-sm btn-y" onClick={onConnect} data-testid="state-connect">
          {tc('connectWallet')}
        </button>
      ) : null;
      break;
    case 'error':
      title = t('statusError');
      body = (
        <>
          {t('stateError', { endpoint })}
          {errorDetail ? (
            <span className="mono" style={{ display: 'block', marginTop: 6, fontSize: 11 }}>
              {t('errorDetail')}: {errorDetail.slice(0, 160)}
            </span>
          ) : null}
          {/*
            Unfiltered on purpose. The backend already knows whether the
            database is down or the indexer is behind; a request that just
            failed is exactly when the user should be told, instead of reading
            "request failed" and having no idea whether to retry or wait.
            Renders nothing when the backend reports no warnings, so a one-off
            failure does not invent a cause.

            The label matters as much as the list. These warnings are whatever
            is active right now, which is not the same as the cause of THIS
            failure — a bridge relayer being absent says nothing about why a
            token request failed. Presenting them unlabelled next to an error
            implies a causation that is not established, so the copy says
            "also reporting", not "because".
          */}
          <div className="mt-14" style={{ fontSize: 12, color: 'var(--ink-2)' }}>
            <ProductStatusHintLabel />
            <ProductStatusHint />
          </div>
        </>
      );
      iconName = 'warn';
      actions = onRetry ? (
        <button className="btn btn-sm" onClick={onRetry} data-testid="state-retry">
          <Icon name="refresh" size={14} /> {tc('retry')}
        </button>
      ) : null;
      break;
    case 'empty':
    default:
      title = emptyTitle;
      body = (
        <>
          {emptyBody}
          <span className="mono" style={{ display: 'block', marginTop: 6, fontSize: 11, opacity: 0.8 }}>
            {t('stateEmptyOk')}
          </span>
        </>
      );
      actions = emptyAction ?? null;
      break;
  }

  return (
    <div className="block" style={{ padding: 32, textAlign: 'center' }} data-testid="data-state" data-state={state}>
      <div
        style={{
          width: 52,
          height: 52,
          borderRadius: 14,
          background: state === 'error' ? 'var(--o)' : 'var(--bg-2)',
          color: state === 'error' ? '#fff' : 'var(--ink)',
          border: '3px solid var(--ink)',
          display: 'grid',
          placeItems: 'center',
          margin: '0 auto 12px',
        }}
      >
        {state === 'loading' ? <span className="spinner" style={{ width: 18, height: 18 }} /> : <Icon name={iconName} size={24} />}
      </div>
      <div className="h-display" style={{ fontSize: 20 }}>
        {title}
      </div>
      <div style={{ marginTop: 8, color: 'var(--ink-2)', maxWidth: 460, margin: '8px auto 0', lineHeight: 1.55, fontSize: 13 }}>
        {body}
      </div>
      {actions && (
        <div className="row gap-10 mt-14" style={{ justifyContent: 'center' }}>
          {actions}
        </div>
      )}
    </div>
  );
}
