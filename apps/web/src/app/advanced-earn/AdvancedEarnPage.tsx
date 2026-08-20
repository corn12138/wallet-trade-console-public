'use client';

/**
 * Atlas X · /advanced-earn
 * Staking pools (single-asset stake + LP farms). Distinct from /earn yield vaults.
 * Backed by the existing /staking/pools + /staking/pools/stats endpoints — the
 * legacy /advanced-earn route uses the same data; this is the Block Party port.
 */

import { useEffect, useMemo, useState } from 'react';
import { useTranslations } from 'next-intl';
import { useAccount, useChainId, useReadContract } from 'wagmi';
import { useQuery } from '@tanstack/react-query';
import { formatUnits, parseUnits } from 'viem';
import { buildApiUrl } from '@/lib/api/base-url';
import {
  CONTRACT_ABIS,
  erc20Abi,
  getOptionalContractAddress,
} from '@/lib/web3/contracts';
import { useDisplayChainId } from '@/hooks/useDisplayChainId';
import { useTokenApproval } from '@/hooks/web3/useTokenApproval';
import { useTxFlow } from '@/hooks/web3/useTxFlow';
import type { AtlasTxReviewInput } from '@/lib/api/atlas';
import { TxPreflight } from '@/app/_atlas/TxPreflight';
import { useApp } from '@/app/_atlas/AppContext';
import { Icon } from '@/app/_atlas/Icon';
import { Empty, MetricCard, PageHeader, TabBar } from '@/app/_atlas/Common';
import { fmt, fmtCompact } from '@/app/_atlas/data';
import { useEnumLabel } from '@/app/_atlas/enums';

const ZERO_ADDRESS = '0x0000000000000000000000000000000000000000' as const;

type StakingFilter = 'all' | 'staking' | 'farming';

interface StakingPool {
  id: string;
  name: string;
  poolType: 'staking' | 'farming' | string;
  tokenAddress: string;
  tokenSymbol?: string;
  rewardToken: string | null;
  apy: number | null;
  tvl: number | null;
  status: string;
  startsAt: string | null;
  endsAt: string | null;
  _count?: { stakes: number };
}

interface StakingStats {
  totalPools: number;
  totalTvl: number;
  highestApy: number;
}

const PALETTE = ['y', 'c', 'o', 'p', 'g', 'b', 'r'];

function poolColor(idx: number) {
  return PALETTE[idx % PALETTE.length];
}

// STK (the deployments-registry staking token) is 18 decimals; values render
// with 4 fractional digits.
function formatStakeAmount(raw: bigint | undefined): string {
  if (raw === undefined) return '—';
  const value = Number(formatUnits(raw, 18));
  return fmt(value, 4);
}

/**
 * Real connected-wallet staking state, read straight from the StakingPool
 * contract (`stakedBalance(user)` + `earned(user)`). These are the exact
 * numbers the contract would use for unstake/claim — no API projection in
 * the middle, no placeholder.
 */
function useUserStakingSummary() {
  const { address: userAddress, isConnected } = useAccount();
  const walletChainId = useChainId();
  const stakingPoolAddress = getOptionalContractAddress(walletChainId, 'StakingPool');
  const enabled = Boolean(isConnected && userAddress && stakingPoolAddress);

  const { data: stakedRaw, refetch: refetchStaked } = useReadContract({
    address: stakingPoolAddress as `0x${string}` | undefined,
    abi: CONTRACT_ABIS.StakingPool,
    functionName: 'stakedBalance',
    args: [(userAddress ?? ZERO_ADDRESS) as `0x${string}`],
    query: { enabled, refetchInterval: 30_000 },
  });
  const { data: earnedRaw, refetch: refetchEarned } = useReadContract({
    address: stakingPoolAddress as `0x${string}` | undefined,
    abi: CONTRACT_ABIS.StakingPool,
    functionName: 'earned',
    args: [(userAddress ?? ZERO_ADDRESS) as `0x${string}`],
    query: { enabled, refetchInterval: 30_000 },
  });

  return {
    isConnected,
    available: enabled,
    staked: stakedRaw as bigint | undefined,
    pendingRewards: earnedRaw as bigint | undefined,
    refetch: () => {
      void refetchStaked();
      void refetchEarned();
    },
  };
}

function formatTimeLeft(endsAt: string | null, endedLabel: string): string | null {
  if (!endsAt) return null;
  const ms = new Date(endsAt).getTime() - Date.now();
  if (!Number.isFinite(ms) || ms <= 0) return endedLabel;
  const d = Math.floor(ms / 86_400_000);
  const h = Math.floor((ms % 86_400_000) / 3_600_000);
  if (d >= 1) return `${d}d ${h.toString().padStart(2, '0')}h`;
  const m = Math.floor((ms % 3_600_000) / 60_000);
  return `${h}h ${m.toString().padStart(2, '0')}m`;
}

export default function AdvancedEarnPage() {
  const t = useTranslations('advancedEarn');
  const stakingStatusLabel = useEnumLabel('staking');
  const { chainId } = useDisplayChainId();
  const [filter, setFilter] = useState<StakingFilter>('all');
  const [selected, setSelected] = useState<StakingPool | null>(null);
  const userStaking = useUserStakingSummary();

  const { data: pools = [], isLoading, isError } = useQuery({
    queryKey: ['atlas-design-staking-pools', filter, chainId],
    queryFn: async (): Promise<StakingPool[]> => {
      const params = new URLSearchParams();
      if (filter !== 'all') params.set('poolType', filter);
      params.set('status', 'active');
      params.set('chainId', String(chainId));
      const res = await fetch(buildApiUrl(`/staking/pools?${params.toString()}`));
      if (!res.ok) return [];
      return res.json();
    },
    refetchInterval: 60_000,
  });

  const { data: stats } = useQuery({
    queryKey: ['atlas-design-staking-stats', chainId],
    queryFn: async (): Promise<StakingStats> => {
      const res = await fetch(buildApiUrl('/staking/pools/stats'));
      if (!res.ok) return { totalPools: 0, totalTvl: 0, highestApy: 0 };
      return res.json();
    },
    refetchInterval: 60_000,
  });

  const list = pools;

  return (
    <div className="col gap-24">
      <PageHeader
        eyebrow={t('headerEyebrow')}
        title={t('headerTitle')}
        kicker={t('headerKicker')}
      />

      <section className="grid-4">
        <MetricCard label={t('metricTotalTvl')} value={stats ? `$${fmtCompact(stats.totalTvl)}` : '—'} sub={stats ? t('activePoolsSub', { count: stats.totalPools }) : t('syncing')} tone="y" icon="trendUp" />
        <MetricCard label={t('metricHighestApy')} value={stats ? `${stats.highestApy.toFixed(1)}%` : '—'} sub={t('seeLeaderboard')} tone="g" icon="signal" />
        <MetricCard
          label={t('metricYourStaked')}
          value={userStaking.available ? formatStakeAmount(userStaking.staked) : '—'}
          sub={userStaking.available ? t('stakedContractSub') : userStaking.isConnected ? t('noPoolOnChain') : t('connectToView')}
          tone="c"
          icon="wallet"
        />
        <MetricCard
          label={t('metricPendingRewards')}
          value={userStaking.available ? formatStakeAmount(userStaking.pendingRewards) : '—'}
          sub={userStaking.available ? t('earnedContractSub') : t('claimAnyTime')}
          tone="p"
          icon="earn"
        />
      </section>

      <section className="row between">
        <TabBar
          tabs={[
            { key: 'all' as const, label: t('tabAll') },
            { key: 'staking' as const, label: t('tabStakingSingle') },
            { key: 'farming' as const, label: t('tabFarmingLp') },
          ]}
          value={filter}
          onChange={setFilter}
        />
        <span className="pill live"><span className="dot" /> {t('liveAprRefresh')}</span>
      </section>

      <section
        className="grid-2"
        style={{ gridTemplateColumns: 'repeat(auto-fill, minmax(360px, 1fr))' }}
      >
        {isLoading && (
          <div className="block" style={{ padding: 24 }}>
            <div className="skel lg" style={{ marginBottom: 14 }} />
            <div className="skel" />
          </div>
        )}
        {!isLoading &&
          list.map((p, idx) => {
            const color = poolColor(idx);
            const inverseInk = ['c', 'p', 'o', 'b', 'r'].includes(color);
            const token = p.tokenSymbol || p.name.split(' ')[0] || 'LP';
            const reward = p.rewardToken || 'ATLAS';
            const timeLeft = formatTimeLeft(p.endsAt, t('ended'));
            const stakers = p._count?.stakes ?? 0;
            return (
              <div key={p.id} className="block lift" onClick={() => setSelected(p)}>
                <div className="row between">
                  <span className="pill flat" style={{ padding: '3px 8px', fontSize: 10 }}>
                    {p.poolType === 'farming' ? t('poolFarmTag') : t('poolStakeTag')}
                  </span>
                  <span className={'pill ' + (p.status === 'active' ? 'live' : '')}>
                    {p.status === 'active' && <span className="dot" />}
                    {stakingStatusLabel(p.status)}
                  </span>
                </div>
                <div className="row gap-14 mt-14">
                  <span
                    style={{
                      width: 52,
                      height: 52,
                      borderRadius: 14,
                      background: `var(--${color})`,
                      color: inverseInk ? '#fff' : 'var(--ink)',
                      border: '3px solid var(--ink)',
                      boxShadow: '0 3px 0 0 var(--ink)',
                      display: 'grid',
                      placeItems: 'center',
                      fontFamily: 'var(--df)',
                      fontWeight: 900,
                      fontSize: 18,
                    }}
                  >
                    {token[0]}
                  </span>
                  <div>
                    <div className="h-display" style={{ fontSize: 18 }}>{p.name}</div>
                    <div className="mono" style={{ fontSize: 12, color: 'var(--ink-2)', marginTop: 4 }}>
                      {t('rewardPrefix', { reward })}
                    </div>
                  </div>
                </div>
                <div className="row between mt-14">
                  <div>
                    <div className="eyebrow">APY</div>
                    <div className="h-display tone-pos" style={{ fontSize: 26, marginTop: 4 }}>
                      {(p.apy || 0).toFixed(1)}%
                    </div>
                  </div>
                  <div style={{ textAlign: 'right' }}>
                    <div className="eyebrow">TVL</div>
                    <div className="mono" style={{ fontSize: 16, fontWeight: 700, marginTop: 4 }}>
                      ${fmtCompact(p.tvl || 0)}
                    </div>
                  </div>
                  <button
                    className="btn btn-sm btn-y"
                    onClick={(e) => {
                      e.stopPropagation();
                      setSelected(p);
                    }}
                  >
                    {p.poolType === 'farming' ? t('farm') : t('stake')} →
                  </button>
                </div>
                {(timeLeft || stakers > 0) && (
                  <div
                    className="meta-row mt-14"
                    style={{
                      background: 'var(--bg-2)',
                      border: '2px dashed var(--ink)',
                      borderRadius: 10,
                      padding: '6px 10px',
                      flexWrap: 'wrap',
                      gap: 6,
                      fontSize: 11,
                      lineHeight: 1.3,
                    }}
                  >
                    {timeLeft && (
                      <span style={{ minWidth: 0 }}>
                        <Icon name="activity" size={12} /> {t('endsIn', { time: timeLeft })}
                      </span>
                    )}
                    {stakers > 0 && (
                      <span className="mono" style={{ color: 'var(--ink-2)' }}>
                        {t('stakers', { count: stakers.toLocaleString() })}
                      </span>
                    )}
                  </div>
                )}
              </div>
            );
          })}
        {!isLoading && !list.length && (
          <Empty
            body={
              isError
                ? t('emptyApiError')
                : filter === 'all'
                  ? t('emptyNoPools')
                  : t('emptyNoMatch')
            }
          />
        )}
      </section>

      {selected && (
        <StakePoolModal
          pool={selected}
          close={() => setSelected(null)}
          onTxConfirmed={userStaking.refetch}
        />
      )}
    </div>
  );
}

type Mode = 'stake' | 'unstake' | 'claim';

function StakePoolModal({
  pool,
  close,
  onTxConfirmed,
}: {
  pool: StakingPool;
  close: () => void;
  onTxConfirmed?: () => void;
}) {
  const t = useTranslations('advancedEarn');
  const app = useApp();
  const chainId = useChainId();
  const { address: userAddress, isConnected } = useAccount();
  const [amt, setAmt] = useState('100');
  const [mode, setMode] = useState<Mode>('stake');

  const token = pool.tokenSymbol || pool.name.split(' ')[0] || 'LP';
  const reward = pool.rewardToken || 'ATLAS';

  const stakingPoolAddress = getOptionalContractAddress(chainId, 'StakingPool');
  const tokenAddress = (pool.tokenAddress as `0x${string}` | undefined) || undefined;

  const { data: decimalsRaw } = useReadContract({
    address: tokenAddress,
    abi: erc20Abi,
    functionName: 'decimals',
    query: { enabled: Boolean(tokenAddress) },
  });
  const decimals = typeof decimalsRaw === 'number' ? decimalsRaw : 18;

  // Live position for the connected wallet against THIS pool contract —
  // the same values the contract uses for unstake/claim.
  const userReadsEnabled = Boolean(isConnected && userAddress && stakingPoolAddress);
  const { data: stakedRaw, refetch: refetchStaked } = useReadContract({
    address: (stakingPoolAddress ?? ZERO_ADDRESS) as `0x${string}`,
    abi: CONTRACT_ABIS.StakingPool,
    functionName: 'stakedBalance',
    args: [(userAddress ?? ZERO_ADDRESS) as `0x${string}`],
    query: { enabled: userReadsEnabled },
  });
  const { data: earnedRaw, refetch: refetchEarned } = useReadContract({
    address: (stakingPoolAddress ?? ZERO_ADDRESS) as `0x${string}`,
    abi: CONTRACT_ABIS.StakingPool,
    functionName: 'earned',
    args: [(userAddress ?? ZERO_ADDRESS) as `0x${string}`],
    query: { enabled: userReadsEnabled },
  });

  const amountParsed = useMemo(() => {
    if (!amt || Number(amt) <= 0) return 0n;
    try {
      return parseUnits(amt, decimals);
    } catch {
      return 0n;
    }
  }, [amt, decimals]);

  const approval = useTokenApproval(
    (tokenAddress ?? ZERO_ADDRESS) as `0x${string}`,
    (stakingPoolAddress ?? ZERO_ADDRESS) as `0x${string}`,
  );
  const needsApproval = mode === 'stake' && amountParsed > 0n && !approval.isApproved(amountParsed);
  const approveBusy = approval.isPending || approval.isConfirming;

  // Only the approve step is reviewable here. stake / unstake / claim have no
  // matching tx-review operation — `earn-deposit` resolves its productId
  // against the yield-vault products, not staking pools — and reviewing them
  // would mean re-encoding the calldata this page never builds itself, so a
  // mismatch would produce a verdict on a transaction that isn't the one sent.
  // Approve is also where the real risk sits: an allowance to a pool contract.
  const preflightInput: AtlasTxReviewInput | null = useMemo(() => {
    if (!needsApproval || !userAddress || !tokenAddress || !stakingPoolAddress) return null;
    return {
      operationType: 'approve',
      fromAddress: userAddress,
      chainId,
      tokenAddress,
      spender: stakingPoolAddress,
      amount: amt,
      tokenDecimals: decimals,
    };
  }, [needsApproval, userAddress, tokenAddress, stakingPoolAddress, chainId, amt, decimals]);

  const txType =
    mode === 'stake' ? 'staking-stake' : mode === 'unstake' ? 'staking-unstake' : 'staking-claim';
  const titleVerb = mode === 'stake' ? t('stake') : mode === 'unstake' ? t('unstake') : t('claim');

  const flow = useTxFlow({
    txType,
    title: `${titleVerb} · ${pool.name}`,
    buildSummary: () =>
      mode === 'claim'
        ? `Claim pending rewards from ${pool.name}`
        : `${titleVerb} ${amt} ${token} · ${pool.name}`,
    buildMetadata: () => ({
      poolId: pool.id,
      poolName: pool.name,
      poolType: pool.poolType,
      tokenAddress: pool.tokenAddress,
      contractAddress: stakingPoolAddress,
      mode,
      amount: mode === 'claim' ? null : amt,
      apy: pool.apy,
    }),
    toastOnSuccess:
      mode === 'stake'
        ? `Staked into ${pool.name}`
        : mode === 'unstake'
          ? `Unstaked from ${pool.name}`
          : `Rewards claimed from ${pool.name}`,
    onConfirmed: () => {
      // Refresh the on-chain position (modal + page summary) so the UI
      // reflects the receipt-confirmed state, then close.
      void refetchStaked();
      void refetchEarned();
      onTxConfirmed?.();
      close();
    },
  });

  useEffect(() => {
    if (flow.stage === 'confirmed') setAmt('');
  }, [flow.stage]);

  const busy = flow.isWorking || approveBusy;

  const handleApprove = () => {
    if (amountParsed === 0n) return;
    approval.approve(amountParsed);
    app.toast(`Approving ${token}…`, 'warn');
  };

  const handleSubmit = () => {
    if (!stakingPoolAddress || !userAddress) return;
    if (mode === 'claim') {
      flow.execute({
        address: stakingPoolAddress,
        abi: CONTRACT_ABIS.StakingPool,
        functionName: 'claimReward',
        args: [],
      });
      return;
    }
    if (amountParsed === 0n) return;
    flow.execute({
      address: stakingPoolAddress,
      abi: CONTRACT_ABIS.StakingPool,
      functionName: mode === 'stake' ? 'stake' : 'unstake',
      args: [amountParsed],
    });
  };

  const handlePrimary = () => {
    if (!isConnected || app.walletState !== 'connected') {
      close();
      app.openConnect();
      return;
    }
    if (!stakingPoolAddress) {
      app.toast(t('toastNotDeployed'), 'err');
      return;
    }
    if (needsApproval) handleApprove();
    else handleSubmit();
  };

  const ctaLabel = (() => {
    if (!isConnected || app.walletState !== 'connected') return t('ctaConnect');
    if (!stakingPoolAddress) return t('ctaPoolUnavailable');
    if (approveBusy) return t('ctaApproving');
    if (needsApproval) return t('ctaApprove', { token });
    if (flow.isWorking) return flow.stage === 'pending' ? t('ctaConfirming') : t('ctaSubmitting');
    if (mode === 'claim') return t('ctaClaim');
    return t('ctaAction', { verb: titleVerb, amount: amt, token });
  })();

  const ctaDisabled =
    busy ||
    (isConnected &&
      (!stakingPoolAddress || (mode !== 'claim' && amountParsed === 0n)));

  return (
    <div className="modal-bg" onClick={(e) => e.target === e.currentTarget && close()}>
      <div className="modal-box">
        <div className="modal-head">
          <h3>{pool.name}</h3>
          <button className="modal-x" onClick={close}>
            <Icon name="close" size={16} />
          </button>
        </div>

        <div className="seg s3 mt-14">
          <button
            className={mode === 'stake' ? 'on' : ''}
            onClick={() => setMode('stake')}
            disabled={busy}
          >
            {t('stake')}
          </button>
          <button
            className={mode === 'unstake' ? 'on' : ''}
            onClick={() => setMode('unstake')}
            disabled={busy}
          >
            {t('unstake')}
          </button>
          <button
            className={mode === 'claim' ? 'on' : ''}
            onClick={() => setMode('claim')}
            disabled={busy}
          >
            {t('claim')}
          </button>
        </div>

        {mode !== 'claim' && (
          <div className="field mt-14">
            <div className="l">
              <span>{t('amount')}</span>
              <span>{token}</span>
            </div>
            <input value={amt} onChange={(e) => setAmt(e.target.value)} />
          </div>
        )}

        <div className="block tight bg-paper2 mt-14">
          <div className="meta-row">
            <span>APY</span>
            <b className="tone-pos">{(pool.apy || 0).toFixed(1)}%</b>
          </div>
          <div className="meta-row mt-6">
            <span>{t('rewardToken')}</span>
            <b>{reward}</b>
          </div>
          <div className="meta-row mt-6">
            <span>{t('cooldown')}</span>
            <b>{pool.poolType === 'farming' ? t('cooldownNone') : t('cooldownStake')}</b>
          </div>
          {mode === 'stake' && (
            <div className="meta-row mt-6">
              <span>{t('estMonthly')}</span>
              <b className="tone-pos">+{fmt(((Number(amt) || 0) * (pool.apy || 0)) / 100 / 12)} {reward}</b>
            </div>
          )}
          {userReadsEnabled && (
            <div className="meta-row mt-6">
              <span>{t('metricYourStaked')}</span>
              <b>{formatStakeAmount(stakedRaw as bigint | undefined)} {token}</b>
            </div>
          )}
          {(mode === 'claim' || userReadsEnabled) && (
            <div className="meta-row mt-6">
              <span>{t('metricPendingRewards')}</span>
              <b className="tone-pos">
                {userReadsEnabled
                  ? `${formatStakeAmount(earnedRaw as bigint | undefined)} ${reward}`
                  : `— ${reward}`}
              </b>
            </div>
          )}
          <div className="meta-row mt-6">
            <span>{t('poolContract')}</span>
            <b style={{ fontFamily: 'var(--mf)' }}>
              {stakingPoolAddress
                ? `${stakingPoolAddress.slice(0, 6)}…${stakingPoolAddress.slice(-4)}`
                : '—'}
            </b>
          </div>
          <div className="meta-row mt-6">
            <span>{t('tokenLabel')}</span>
            <b style={{ fontFamily: 'var(--mf)' }}>
              {pool.tokenAddress
                ? `${pool.tokenAddress.slice(0, 6)}…${pool.tokenAddress.slice(-4)}`
                : '—'}
            </b>
          </div>
        </div>

        {/* Advisory pre-sign review; the CTA's own gates are unchanged. */}
        <TxPreflight input={preflightInput} className="mt-14" />

        <button
          className={'btn mt-14 ' + (mode === 'unstake' ? 'btn-o' : 'btn-g')}
          style={{ width: '100%' }}
          disabled={ctaDisabled}
          onClick={handlePrimary}
        >
          {busy && <span className="spinner" />}
          {ctaLabel}
        </button>
        <p className="mono" style={{ marginTop: 10, fontSize: 11, color: 'var(--ink-2)' }}>
          Approval, stake, unstake and claim are signed by your wallet against the StakingPool contract; the API only sources pool metadata.
        </p>
      </div>
    </div>
  );
}
