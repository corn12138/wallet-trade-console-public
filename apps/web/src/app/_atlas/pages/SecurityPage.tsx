'use client';
import { Fragment, useMemo, useState } from 'react';
import { useAccount, useChainId } from 'wagmi';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useTranslations } from 'next-intl';
import {
  getConnectedSites,
  getSecurityAlerts,
  getSecurityApprovals,
  removeConnectedSite,
  type AtlasTxReviewInput,
} from '@/lib/api/atlas';
import { useApp } from '../AppContext';
import { Icon, LogoCube } from '../Icon';
import { MetricCard, PageHeader, TabBar } from '../Common';
import { DataStatePanel, SourceMeta, deriveDataPanelState } from '../DataState';
import { TxPreflight } from '../TxPreflight';
import { formatAllowance, riskColor, shortAddr } from './assetUtils';
import { TxReviewPanel } from './TxReviewPanel';
import { revokeKeyFor, useRevokeApproval, type RevokeTarget } from './useRevokeApproval';

type SecurityTab = 'approvals' | 'sites' | 'alerts' | 'review';

const SEVERITY_COLOR: Record<string, string> = {
  critical: 'r',
  high: 'o',
  medium: 'y',
  low: 'g',
  info: 'c',
};

export function SecurityPage() {
  const app = useApp();
  const t = useTranslations('security');
  const tc = useTranslations('commonAtlas');
  const { address } = useAccount();
  const chainId = useChainId();
  const queryClient = useQueryClient();
  const [tab, setTab] = useState<SecurityTab>('approvals');
  // The approval a user has selected to revoke, awaiting its pre-sign review.
  const [armed, setArmed] = useState<RevokeTarget | null>(null);

  const enabled = Boolean(address);

  const approvalsQ = useQuery({
    queryKey: ['atlas-design-approvals', address, chainId],
    queryFn: () => getSecurityApprovals(address, chainId),
    enabled,
    refetchInterval: 30_000,
  });

  const alertsQ = useQuery({
    queryKey: ['atlas-design-alerts', address, chainId],
    queryFn: () => getSecurityAlerts(address, chainId),
    enabled,
    refetchInterval: 30_000,
  });

  const sitesQ = useQuery({
    queryKey: ['atlas-design-sites', address, chainId],
    queryFn: () => getConnectedSites(address, chainId),
    enabled,
    refetchInterval: 60_000,
  });

  const approvals = approvalsQ.data ?? [];
  const alerts = alertsQ.data ?? [];
  const sites = sitesQ.data ?? [];

  const unlimitedCount = approvals.filter((a) => formatAllowance(a.allowance) === 'Unlimited').length;
  const highRiskCount = alerts.filter((a) => a.severity === 'high' || a.severity === 'critical').length;

  // Real wallet-signed revoke: submits `approve(spender, 0)`, waits for the
  // receipt, then refetches every server view that an allowance change moves
  // (approvals, alerts, connected sites, the wallet activity feed, and the
  // Go product-status diagnostics).
  const { revoke, pendingKey, isRevoking } = useRevokeApproval({
    fromAddress: address,
    onConfirmed: () => {
      setArmed(null);
      approvalsQ.refetch();
      alertsQ.refetch();
      sitesQ.refetch();
      queryClient.invalidateQueries({ queryKey: ['atlas-design-user-events'] });
      queryClient.invalidateQueries({ queryKey: ['atlas-product-status'] });
    },
  });

  const toRevokeTarget = (input: {
    tokenAddress: string;
    spender: string;
    chainId: number;
  }): RevokeTarget => ({
    tokenAddress: input.tokenAddress as `0x${string}`,
    spender: input.spender as `0x${string}`,
    chainId: input.chainId,
  });

  // A revoke is reviewed before it is signed, so the first click arms the row
  // and the second one signs. The intermediate state is the whole point: an
  // approval list has no form to type into, so without it the review would only
  // ever appear after the wallet had already been asked to sign.
  const armedKey = armed ? revokeKeyFor(armed) : null;

  // Matches `useRevokeApproval` exactly: same token, same spender, and the
  // server encodes the same approve(spender, 0) the wallet will be handed.
  const revokePreflightInput: AtlasTxReviewInput | null = useMemo(() => {
    if (!armed || !address) return null;
    return {
      operationType: 'revoke-approval',
      fromAddress: address,
      chainId: armed.chainId,
      tokenAddress: armed.tokenAddress,
      spender: armed.spender,
    };
  }, [armed, address]);

  const revokeReview = armed && (
    <div className="col gap-8" data-testid="revoke-preflight">
      <div style={{ fontSize: 11, color: 'var(--ink-2)' }}>
        {t('revokeReviewHint', { spender: shortAddr(armed.spender) })}
      </div>
      <TxPreflight input={revokePreflightInput} />
      <div className="row gap-6">
        <button
          className="btn btn-xs btn-o"
          onClick={() => revoke(armed)}
          disabled={isRevoking}
          data-testid="revoke-confirm"
        >
          {pendingKey === armedKey && isRevoking ? t('revoking') : t('revokeConfirm')}
        </button>
        <button className="btn btn-xs" onClick={() => setArmed(null)} data-testid="revoke-cancel">
          {tc('cancel')}
        </button>
      </div>
    </div>
  );

  if (!address) {
    return (
      <div className="col gap-24">
        <PageHeader
          eyebrow={t('headerEyebrow')}
          title={t('connectTitle')}
          kicker={t('connectKicker')}
        />
        <div className="block" style={{ padding: 60, textAlign: 'center' }}>
          <LogoCube size={56} />
          <div className="h-display" style={{ fontSize: 24, marginTop: 16 }}>{tc('watchModeActive')}</div>
          <button className="btn btn-y mt-22" onClick={app.openConnect}>{tc('connectWallet')}</button>
        </div>
      </div>
    );
  }

  return (
    <div className="col gap-24">
      <PageHeader
        eyebrow={t('headerEyebrow')}
        title={t('headerTitle')}
        kicker={t('headerKicker')}
      />

      <section className="grid-4">
        <MetricCard label={t('metricApprovals')} value={String(approvals.length)} sub={t('metricApprovalsSub', { count: unlimitedCount })} tone="y" />
        <MetricCard label={t('metricAlerts')} value={String(highRiskCount)} sub={t('metricAlertsSub', { count: alerts.length })} tone="o" />
        <MetricCard label={t('metricSites')} value={String(sites.length)} sub={sites[0]?.domain ?? '—'} tone="c" />
        <MetricCard label={t('metricChain')} value={String(chainId)} sub={t('metricChainSub')} tone="p" />
      </section>

      <TabBar
        tabs={[
          { key: 'approvals' as SecurityTab, label: `${t('tabApprovals')} · ${approvals.length}` },
          { key: 'sites' as SecurityTab, label: `${t('tabSites')} · ${sites.length}` },
          { key: 'alerts' as SecurityTab, label: `${t('tabAlerts')} · ${alerts.length}` },
          { key: 'review' as SecurityTab, label: t('tabReview') },
        ]}
        value={tab}
        onChange={setTab}
      />

      <SourceMeta
        endpoint="GET /api/security/approvals · alerts · sites"
        chainId={chainId}
        wallet={address}
        updatedAt={Math.max(approvalsQ.dataUpdatedAt, alertsQ.dataUpdatedAt, sitesQ.dataUpdatedAt)}
        refetchIntervalMs={30_000}
        rowCount={approvals.length + alerts.length + sites.length}
        state={deriveDataPanelState({
          isLoading: approvalsQ.isLoading || alertsQ.isLoading || sitesQ.isLoading,
          isError: approvalsQ.isError && alertsQ.isError && sitesQ.isError,
          rowCount: approvals.length + alerts.length + sites.length,
        })}
        isFetching={approvalsQ.isFetching || alertsQ.isFetching || sitesQ.isFetching}
        onRefresh={() => {
          approvalsQ.refetch();
          alertsQ.refetch();
          sitesQ.refetch();
        }}
      />

      {tab === 'approvals' && (
        <section className="block tight">
          <div className="tbl" style={{ boxShadow: 'none', border: '2px solid var(--ink)', borderRadius: 14 }}>
            <div className="tbl-head" style={{ gridTemplateColumns: '1fr 1.4fr 1fr 0.9fr auto' }}>
              <span>{t('colToken')}</span>
              <span>{t('colSpender')}</span>
              <span>{t('colAllowance')}</span>
              <span>{t('colLastSeen')}</span>
              <span />
            </div>
            {approvalsQ.isLoading && (
              <div className="tbl-row" style={{ gridTemplateColumns: '1fr' }}>
                <div className="skel lg" />
              </div>
            )}
            {!approvalsQ.isLoading &&
              approvals.map((a) => {
                const allowanceLabel = formatAllowance(a.allowance);
                const isUnlimited = allowanceLabel === 'Unlimited';
                const rowKey = revokeKeyFor({ tokenAddress: a.tokenAddress, spender: a.spender });
                return (
                  <Fragment key={`${a.tokenAddress}-${a.spender}`}>
                  <div
                    className="tbl-row"
                    style={{ gridTemplateColumns: '1fr 1.4fr 1fr 0.9fr auto' }}
                  >
                    <div className="sym">
                      <span className="b">{(a.tokenAddress || '?').slice(2, 3).toUpperCase()}</span>
                      <div className="mono" style={{ fontSize: 12 }}>{shortAddr(a.tokenAddress)}</div>
                    </div>
                    <div className="mono" style={{ fontSize: 12 }}>{shortAddr(a.spender)}</div>
                    <div className="mono" style={{ color: isUnlimited ? 'var(--neg)' : 'var(--ink)' }}>
                      {allowanceLabel}
                    </div>
                    <div className="mono" style={{ color: 'var(--ink-2)', fontSize: 12 }}>
                      {a.lastUpdatedAt ? new Date(a.lastUpdatedAt).toLocaleDateString() : '—'}
                    </div>
                    <button
                      className="btn btn-xs btn-o"
                      onClick={() => setArmed(toRevokeTarget(a))}
                      disabled={isRevoking}
                      data-testid={`revoke-${a.tokenAddress}-${a.spender}`.toLowerCase()}
                    >
                      {pendingKey === rowKey && isRevoking ? t('revoking') : t('revoke')}
                    </button>
                  </div>
                  {armedKey === rowKey && (
                    <div className="tbl-row" style={{ gridTemplateColumns: '1fr' }}>
                      {revokeReview}
                    </div>
                  )}
                  </Fragment>
                );
              })}
            {!approvalsQ.isLoading && !approvals.length && (
              <DataStatePanel
                state={approvalsQ.isError ? 'error' : 'empty'}
                endpoint="GET /api/security/approvals"
                emptyTitle={t('emptyApprovalsTitle')}
                emptyBody={t('emptyApprovalsBody')}
                errorDetail={(approvalsQ.error as Error | null)?.message}
                icon="security"
                onRetry={() => approvalsQ.refetch()}
              />
            )}
          </div>
        </section>
      )}

      {tab === 'sites' && (
        <section className="grid-3">
          {sitesQ.isLoading && <div className="block"><div className="skel lg" /></div>}
          {!sitesQ.isLoading &&
            sites.map((s) => {
              const swatch = riskColor(s.riskLevel);
              const inverseInk = ['r', 'o', 'p', 'c'].includes(swatch);
              return (
                <div key={s.id} className="block">
                  <div className="row gap-14">
                    <span
                      style={{
                        width: 48,
                        height: 48,
                        borderRadius: 14,
                        background: `var(--${swatch})`,
                        color: inverseInk ? '#fff' : 'var(--ink)',
                        border: '3px solid var(--ink)',
                        boxShadow: '0 3px 0 0 var(--ink)',
                        display: 'grid',
                        placeItems: 'center',
                        fontFamily: 'var(--df)',
                        fontWeight: 900,
                        fontSize: 22,
                      }}
                    >
                      {s.siteName[0]}
                    </span>
                    <div>
                      <div className="h-display" style={{ fontSize: 18 }}>{s.siteName}</div>
                      <div className="mono" style={{ fontSize: 12, color: 'var(--ink-2)', marginTop: 4 }}>
                        {s.domain} · {s.riskLevel}
                      </div>
                      <div className="mono" style={{ fontSize: 11, color: 'var(--ink-2)', marginTop: 4 }}>
                        {t('connectedOn', { date: new Date(s.firstConnectedAt).toLocaleDateString() })}
                      </div>
                    </div>
                  </div>
                  <div className="row gap-6 mt-14">
                    <a href={s.origin} target="_blank" rel="noreferrer" className="btn btn-xs">
                      <Icon name="ext" size={12} /> {t('visit')}
                    </a>
                    <button
                      className="btn btn-xs btn-o"
                      onClick={async () => {
                        try {
                          await removeConnectedSite(s.id, address);
                          app.toast(t('toastDisconnected', { site: s.siteName }), 'warn');
                          sitesQ.refetch();
                        } catch (e: any) {
                          app.toast(e?.message || t('toastDisconnectFailed'), 'err');
                        }
                      }}
                    >
                      {t('disconnect')}
                    </button>
                  </div>
                </div>
              );
            })}
          {!sitesQ.isLoading && !sites.length && (
            <DataStatePanel
              state={sitesQ.isError ? 'error' : 'empty'}
              endpoint="GET /api/security/sites"
              emptyTitle={t('emptySitesTitle')}
              emptyBody={t('emptySitesBody')}
              errorDetail={(sitesQ.error as Error | null)?.message}
              icon="globe"
              onRetry={() => sitesQ.refetch()}
            />
          )}
        </section>
      )}

      {tab === 'alerts' && (
        <section className="col gap-14">
          {alertsQ.isLoading && <div className="skel lg" />}
          {!alertsQ.isLoading &&
            alerts.map((a) => {
              const swatch = SEVERITY_COLOR[a.severity] || 'y';
              const isCritical = a.severity === 'critical' || a.severity === 'high';
              return (
                <div key={a.id} className={'block ' + (isCritical ? 'bg-o' : 'bg-y')}>
                  <div className="row between">
                    <div className="row gap-14">
                      <Icon name="warn" size={24} />
                      <div>
                        <div className="h-display" style={{ fontSize: 18 }}>{a.title}</div>
                        <p style={{ marginTop: 4, color: isCritical ? 'rgba(255,255,255,0.85)' : 'var(--ink-2)' }}>
                          {a.summary}
                        </p>
                        {a.spender && (
                          <div
                            className="mono"
                            style={{
                              fontSize: 11,
                              marginTop: 6,
                              color: isCritical ? 'rgba(255,255,255,0.8)' : 'var(--ink-2)',
                            }}
                          >
                            spender {shortAddr(a.spender)}
                            {a.tokenAddress ? ` · token ${shortAddr(a.tokenAddress)}` : ''}
                          </div>
                        )}
                      </div>
                    </div>
                    {a.actionType === 'revoke-approval' && a.tokenAddress && a.spender && (
                      <button
                        className="btn btn-sm btn-d"
                        disabled={isRevoking}
                        onClick={() =>
                          setArmed(
                            toRevokeTarget({
                              tokenAddress: a.tokenAddress!,
                              spender: a.spender!,
                              chainId: a.chainId,
                            }),
                          )
                        }
                      >
                        {a.actionLabel || t('revokeNow')}
                      </button>
                    )}
                  </div>
                  {a.tokenAddress &&
                    a.spender &&
                    armedKey === revokeKeyFor({ tokenAddress: a.tokenAddress, spender: a.spender }) && (
                      <div className="mt-14">{revokeReview}</div>
                    )}
                </div>
              );
            })}
          {!alertsQ.isLoading && !alerts.length && (
            <DataStatePanel
              state={alertsQ.isError ? 'error' : 'empty'}
              endpoint="GET /api/security/alerts"
              emptyTitle={t('allClearTitle')}
              emptyBody={t('allClearBody')}
              errorDetail={(alertsQ.error as Error | null)?.message}
              icon="check"
              onRetry={() => alertsQ.refetch()}
            />
          )}
        </section>
      )}

      {tab === 'review' && address && <TxReviewPanel fromAddress={address} chainId={chainId} />}
    </div>
  );
}
