'use client';
import { useEffect, useMemo, useState } from 'react';
import { useTranslations } from 'next-intl';
import { useAccount, useReadContract } from 'wagmi';
import { useQuery } from '@tanstack/react-query';
import { parseUnits } from 'viem';
import { getEarnProducts, type AtlasEarnProduct, type AtlasTxReviewInput } from '@/lib/api/atlas';
import { TxPreflight } from '../TxPreflight';
import { CONTRACT_ABIS, erc20Abi } from '@/lib/web3/contracts';
import { useDisplayChainId } from '@/hooks/useDisplayChainId';
import { useTokenApproval } from '@/hooks/web3/useTokenApproval';
import { useTxFlow } from '@/hooks/web3/useTxFlow';
import { useApp } from '../AppContext';
import { Icon } from '../Icon';
import { Empty, MetricCard, PageHeader, TabBar } from '../Common';
import { fmt, fmtCompact } from '../data';
import { useEnumLabel } from '../enums';

const ZERO_ADDRESS = '0x0000000000000000000000000000000000000000' as const;

const EARN_PALETTE = ['y', 'c', 'o', 'p', 'g', 'b', 'r'];
// Machine risk tiers — display copy resolves through t('riskLow'|'riskMed'|'riskHigh').
const RISK_BY_TYPE: Record<string, 'low' | 'med' | 'high'> = {
  stablecoin: 'low',
  staking: 'low',
  liquidity: 'med',
  yield: 'med',
  perpetual: 'high',
  options: 'high',
};
function riskFor(p: AtlasEarnProduct): 'low' | 'med' | 'high' {
  const t = (p.productType || '').toLowerCase();
  if (RISK_BY_TYPE[t]) return RISK_BY_TYPE[t];
  if (p.apy >= 30) return 'high';
  if (p.apy >= 15) return 'med';
  return 'low';
}

export function EarnPage() {
  const app = useApp();
  const t = useTranslations('earnAtlas');
  const productTypeLabel = useEnumLabel('productType');
  const { chainId, isFallback: isChainFallback } = useDisplayChainId();
  const [filter, setFilter] = useState<'all' | 'low' | 'med' | 'high'>('all');
  const [selected, setSelected] = useState<AtlasEarnProduct | null>(null);

  const { data: products = [], isLoading, isError } = useQuery({
    queryKey: ['atlas-design-earn-products', chainId],
    queryFn: () => getEarnProducts(chainId),
    refetchInterval: 60_000,
  });

  const enriched = useMemo(
    () => products.map((p) => ({ ...p, _risk: riskFor(p) })),
    [products],
  );

  const list = useMemo(() => {
    if (filter === 'all') return enriched;
    return enriched.filter((p) => p._risk === filter);
  }, [enriched, filter]);

  const tvlTotal = enriched.reduce((s, p) => s + (p.tvl || 0), 0);
  const apySum = enriched.reduce((s, p) => s + (p.apy || 0), 0);
  const avgApy = enriched.length > 0 ? apySum / enriched.length : 0;

  return (
    <div className="col gap-24">
      <PageHeader
        eyebrow={t('headerEyebrow')}
        title={t('headerTitle')}
        kicker={t('headerKicker')}
      />

      <section className="grid-4">
        <MetricCard label={t('metricTvl')} value={`$${fmtCompact(tvlTotal)}`} sub={t('metricTvlSub', { count: enriched.length })} tone="y" />
        <MetricCard label={t('metricApy')} value={`${avgApy.toFixed(1)}%`} sub={t('metricApySub')} tone="g" />
        <MetricCard
          label={t('metricChain')}
          value={String(chainId)}
          sub={isChainFallback ? t('metricChainSubFallback') : t('metricChainSub')}
          tone="c"
        />
        <MetricCard label={t('metricRefresh')} value="60s" sub={t('metricRefreshSub')} tone="p" />
      </section>

      <section className="row between">
        <TabBar
          tabs={[
            { key: 'all' as const, label: t('tabAll') },
            { key: 'low' as const, label: t('tabLow') },
            { key: 'med' as const, label: t('tabMed') },
            { key: 'high' as const, label: t('tabHigh') },
          ]}
          value={filter}
          onChange={setFilter}
        />
        <span className="pill live"><span className="dot" /> {t('livePill')}</span>
      </section>

      <section className="grid-2" style={{ gridTemplateColumns: 'repeat(auto-fill, minmax(360px, 1fr))' }}>
        {isLoading && (
          <div className="block" style={{ padding: 24 }}>
            <div className="skel lg" style={{ marginBottom: 14 }} />
            <div className="skel" />
          </div>
        )}
        {!isLoading &&
          list.map((p, idx) => {
            const color = EARN_PALETTE[idx % EARN_PALETTE.length];
            const inverseInk = ['c', 'p', 'o'].includes(color);
            return (
              <div key={p.id} className="block lift" onClick={() => setSelected(p)}>
                <div className="row between">
                  <span className="pill">{p._risk === 'low' ? t('riskLow') : p._risk === 'med' ? t('riskMed') : t('riskHigh')}</span>
                  <span className="eyebrow">{productTypeLabel(p.productType)}</span>
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
                      fontSize: 22,
                    }}
                  >
                    {p.name[0]}
                  </span>
                  <div>
                    <div className="h-display" style={{ fontSize: 20 }}>{p.name}</div>
                    <div className="mono" style={{ fontSize: 12, color: 'var(--ink-2)', marginTop: 4 }}>
                      {t('tvlChain', { tvl: fmtCompact(p.tvl || 0), chainId: p.chainId })}
                    </div>
                  </div>
                </div>
                <div className="row between mt-14">
                  <div>
                    <div className="eyebrow">{t('currentApy')}</div>
                    <div className="h-display tone-pos" style={{ fontSize: 28, marginTop: 4 }}>
                      {p.apy.toFixed(1)}%
                    </div>
                  </div>
                  <button
                    className="btn btn-sm btn-y"
                    onClick={(e) => {
                      e.stopPropagation();
                      setSelected(p);
                    }}
                  >
                    {t('deposit')}
                  </button>
                </div>
              </div>
            );
          })}
        {!isLoading && !list.length && (
          <div>
            <Empty
              body={
                isError
                  ? t('emptyError')
                  : filter === 'all'
                    ? t('emptyNone', { chainId })
                    : t('emptyFilter')
              }
            />
            {!isError && filter === 'all' && chainId !== 11155111 && (
              <div style={{ textAlign: 'center', marginTop: 12 }}>
                <button
                  className="btn btn-y"
                  onClick={() => app.switchChain({ id: 11155111, name: 'Sepolia' })}
                >
                  {t('switchCta')}
                </button>
              </div>
            )}
          </div>
        )}
      </section>

      {selected && <VaultDepositModal product={selected} close={() => setSelected(null)} />}
    </div>
  );
}

function VaultDepositModal({ product, close }: { product: AtlasEarnProduct; close: () => void }) {
  const app = useApp();
  const t = useTranslations('earnAtlas');
  const { address: userAddress, isConnected } = useAccount();
  const [amt, setAmt] = useState('1000');

  const stakingPoolAddress = (product.contractAddress as `0x${string}` | null) ?? null;
  const tokenAddress = (product.tokenAddress as `0x${string}` | undefined) || undefined;

  const { data: decimalsRaw } = useReadContract({
    address: tokenAddress,
    abi: erc20Abi,
    functionName: 'decimals',
    query: { enabled: Boolean(tokenAddress) },
  });
  const decimals = typeof decimalsRaw === 'number' ? decimalsRaw : 18;

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
  const needsApproval = amountParsed > 0n && !approval.isApproved(amountParsed);
  const approveBusy = approval.isPending || approval.isConfirming;

  // Follows the CTA, same as swap: approve while the allowance is missing,
  // earn-deposit once it isn't.
  const preflightInput: AtlasTxReviewInput | null = useMemo(() => {
    if (!userAddress || amountParsed <= 0n) return null;
    if (needsApproval) {
      if (!tokenAddress || !stakingPoolAddress) return null;
      return {
        operationType: 'approve',
        fromAddress: userAddress,
        chainId: product.chainId,
        tokenAddress,
        spender: stakingPoolAddress,
        amount: amt,
        tokenDecimals: decimals,
      };
    }
    return {
      operationType: 'earn-deposit',
      fromAddress: userAddress,
      chainId: product.chainId,
      productId: product.id,
      amount: amt,
      tokenDecimals: decimals,
    };
  }, [
    userAddress, amountParsed, needsApproval, tokenAddress,
    stakingPoolAddress, amt, decimals, product.chainId, product.id,
  ]);

  const deposit = useTxFlow({
    txType: 'earn-deposit',
    title: t('txTitle', { name: product.name }),
    buildSummary: () =>
      `${amt} into ${product.name}\nAPY ${product.apy.toFixed(2)}% · type ${product.productType}`,
    buildMetadata: () => ({
      productId: product.id,
      productName: product.name,
      productType: product.productType,
      tokenAddress: product.tokenAddress,
      contractAddress: product.contractAddress,
      amount: amt,
      apy: product.apy,
    }),
    toastOnSuccess: t('toastDeposited', { name: product.name }),
    onConfirmed: () => {
      close();
    },
  });

  useEffect(() => {
    if (deposit.stage === 'confirmed') setAmt('');
  }, [deposit.stage]);

  const handleApprove = () => {
    if (amountParsed === 0n) return;
    approval.approve(amountParsed);
    app.toast(t('toastApproving'), 'warn');
  };

  const handleDeposit = () => {
    if (!stakingPoolAddress || amountParsed === 0n || !userAddress) return;
    deposit.execute({
      address: stakingPoolAddress,
      abi: CONTRACT_ABIS.StakingPool,
      functionName: 'stake',
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
      app.toast(t('toastMissingContract'), 'err');
      return;
    }
    if (needsApproval) handleApprove();
    else handleDeposit();
  };

  const ctaLabel = (() => {
    if (!isConnected || app.walletState !== 'connected') return t('ctaConnect');
    if (!stakingPoolAddress) return t('ctaUnavailable');
    if (approveBusy) return t('ctaApproving');
    if (needsApproval) return t('ctaApprove');
    if (deposit.isWorking) return deposit.stage === 'pending' ? t('ctaConfirming') : t('ctaSubmitting');
    return t('ctaDeposit', { amount: amt, name: product.name });
  })();
  const ctaDisabled =
    deposit.isWorking ||
    approveBusy ||
    (isConnected && (amountParsed === 0n || !stakingPoolAddress));

  return (
    <div className="modal-bg" onClick={(e) => e.target === e.currentTarget && close()}>
      <div className="modal-box">
        <div className="modal-head">
          <h3>{t('modalTitle', { name: product.name })}</h3>
          <button className="modal-x" onClick={close}>
            <Icon name="close" size={16} />
          </button>
        </div>
        <div className="field">
          <div className="l">
            <span>{t('amount')}</span>
            <span>{product.productType}</span>
          </div>
          <input value={amt} onChange={(e) => setAmt(e.target.value)} />
        </div>
        <div className="block tight bg-paper2 mt-14">
          <div className="meta-row">
            <span>{t('estMonthly')}</span>
            <b className="tone-pos">+{fmt((Number(amt) * product.apy) / 100 / 12)}</b>
          </div>
          <div className="meta-row mt-6">
            <span>{t('apy')}</span>
            <b>{product.apy.toFixed(2)}%</b>
          </div>
          <div className="meta-row mt-6">
            <span>{t('tvl')}</span>
            <b>${fmtCompact(product.tvl)}</b>
          </div>
          <div className="meta-row mt-6">
            <span>{t('contract')}</span>
            <b style={{ fontFamily: 'var(--mf)' }}>
              {product.contractAddress
                ? `${product.contractAddress.slice(0, 6)}…${product.contractAddress.slice(-4)}`
                : '—'}
            </b>
          </div>
          <div className="meta-row mt-6">
            <span>{t('status')}</span>
            <b style={{ textTransform: 'capitalize' }}>{product.status}</b>
          </div>
        </div>
        {/* Advisory pre-sign review; the CTA's own gates are unchanged. */}
        <TxPreflight input={preflightInput} className="mt-14" />
        <button
          className="btn btn-g mt-14"
          style={{ width: '100%' }}
          disabled={ctaDisabled}
          onClick={handlePrimary}
        >
          {(deposit.isWorking || approveBusy) && <span className="spinner" />}
          {ctaLabel}
        </button>
        <p className="mono" style={{ marginTop: 10, fontSize: 11, color: 'var(--ink-2)' }}>
          {t('walletNote')}
        </p>
      </div>
    </div>
  );
}
