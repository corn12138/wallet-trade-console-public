'use client';
import { useCallback, useEffect, useMemo, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { useTranslations } from 'next-intl';
import { useAccount, useChainId } from 'wagmi';
import { formatUnits, parseUnits } from 'viem';
import { TOKENS, type ChainTokenConfig } from '@wallet-trade/shared';
import {
  BridgeRouteUnavailableError,
  buildBridgeDeposit,
  getBridgeRoutes,
  getBridgeTransfers,
  type BridgeBlocker,
  type BridgeRoute,
  type BridgeRoutesResponse,
  type BridgeTransfer,
} from '@/lib/api/bridge';
import { supportedChains } from '@/lib/web3';
import { bridgeGatewayAbi } from '@/lib/web3/contracts';
import { useDisplayChainId } from '@/hooks/useDisplayChainId';
import { useTxFlow } from '@/hooks/web3/useTxFlow';
import { useTokenApproval } from '@/hooks/web3/useTokenApproval';
import type { AtlasTxReviewInput } from '@/lib/api/atlas';
import { Icon } from '../Icon';
import { PageHeader } from '../Common';
import { SourceMeta } from '../DataState';
import { TxPreflight } from '../TxPreflight';
import { useApp } from '../AppContext';
import { chainSwatch } from './assetUtils';

const ZERO_ADDRESS = '0x0000000000000000000000000000000000000000' as const;

/**
 * localStorage key recording that the user has read the trust-model disclosure.
 * Scoped per gateway address: a redeployed gateway is a different operator
 * setup and deserves to be acknowledged again.
 */
const TRUST_ACK_PREFIX = 'wallet-trade:bridge-trust-ack:';

/**
 * Production `/bridge`.
 *
 * Everything rendered here comes from the deployed BridgeGateway contracts via
 * /api/bridge — route support, pause state, destination liquidity — and from
 * the bridge_transfers projection, which is written only from observed on-chain
 * events. There is no capability flag and no simulated quote: whether execution
 * is offered is decided by `routes.executable`, which the Go service derives
 * from chain state alone.
 *
 * When a route cannot execute the page lists EVERY reason rather than a generic
 * "unavailable", because the reasons have different owners: a missing
 * destination gateway is a deployment task, insufficient liquidity is an
 * operator task, and an unreadable RPC is neither.
 */
export function BridgePage() {
  const t = useTranslations('bridge');
  const app = useApp();
  const { chainId: displayChainId } = useDisplayChainId();
  const { address: userAddress, isConnected } = useAccount();
  const walletChainId = useChainId();

  const [fromId, setFromId] = useState<number>(displayChainId);
  const [toId, setToId] = useState<number>(() => {
    const others = supportedChains.filter((c) => c.id !== displayChainId);
    return others[0]?.id ?? displayChainId;
  });
  const tokens: ChainTokenConfig[] = useMemo(() => TOKENS[fromId] || [], [fromId]);
  const [tok, setTok] = useState<ChainTokenConfig | null>(null);
  const [amt, setAmt] = useState('');
  const [trustAcked, setTrustAcked] = useState(false);
  const [staleRoute, setStaleRoute] = useState(false);
  const [watchTxHash, setWatchTxHash] = useState<string | null>(null);

  useEffect(() => {
    if (tokens.length && !tok) setTok(tokens[0]);
    if (tokens.length && tok && !tokens.find((x) => x.address === tok.address)) setTok(tokens[0]);
  }, [tokens, tok]);

  useEffect(() => {
    setFromId(displayChainId);
    const others = supportedChains.filter((c) => c.id !== displayChainId);
    if (others.length && !others.find((c) => c.id === toId)) setToId(others[0].id);
  }, [displayChainId, toId]);

  const amountParsed = useMemo(() => {
    if (!tok || !amt) return 0n;
    try {
      return parseUnits(amt, tok.decimals);
    } catch {
      return 0n;
    }
  }, [amt, tok]);

  // Routes are quoted in BASE UNITS — no float ever reaches a transfer amount.
  const routeEnabled = Boolean(tok && fromId !== toId && amountParsed > 0n);

  const {
    data: routes,
    isFetching: routesLoading,
    isError: routesError,
    dataUpdatedAt: routesUpdatedAt,
    refetch: refetchRoutes,
  } = useQuery<BridgeRoutesResponse>({
    queryKey: ['bridge-routes', fromId, toId, tok?.address, amountParsed.toString()],
    queryFn: () =>
      getBridgeRoutes({
        fromChainId: fromId,
        toChainId: toId,
        tokenSymbol: tok!.symbol,
        srcToken: tok!.address,
        amount: amountParsed.toString(),
      }),
    enabled: routeEnabled,
    staleTime: 15_000,
    refetchInterval: 30_000,
  });

  const route: BridgeRoute | undefined = routes?.routes?.[0];
  const gateway = (route?.srcGateway || ZERO_ADDRESS) as `0x${string}`;

  // Execution is gated on chain state ALONE. There is no repo capability flag.
  const executable = route?.executable === true;

  // Trust acknowledgement is scoped to the gateway the user would actually use.
  const trustAckKey = `${TRUST_ACK_PREFIX}${gateway.toLowerCase()}`;
  useEffect(() => {
    if (typeof window === 'undefined' || gateway === ZERO_ADDRESS) return;
    setTrustAcked(window.localStorage.getItem(trustAckKey) === '1');
  }, [trustAckKey, gateway]);

  const ackTrust = () => {
    if (typeof window !== 'undefined') window.localStorage.setItem(trustAckKey, '1');
    setTrustAcked(true);
  };

  const approval = useTokenApproval(
    (tok?.address as `0x${string}` | undefined) ?? ZERO_ADDRESS,
    gateway,
    { chainId: fromId as never },
  );
  const needsApproval = amountParsed > 0n && !approval.isApproved(amountParsed);
  const approveBusy = approval.isPending || approval.isConfirming;

  // The bridge-deposit review the deterministic engine produces (route state,
  // pause, destination liquidity, recipient, sender history). Null until the
  // form describes a real transfer — there is nothing to review before that.
  //
  // Keyed off the SERVER-confirmed route, not the local select state, for the
  // same reason handleDeposit sends route.toChainId: the two must describe one
  // transfer. Reviewing the chain the dropdown shows while depositing to the
  // chain the server resolved would be a verdict on a different transaction.
  const preflightInput: AtlasTxReviewInput | null = useMemo(() => {
    if (!userAddress || !route || amountParsed <= 0n) return null;
    return {
      operationType: 'bridge-deposit',
      fromAddress: userAddress,
      chainId: fromId,
      tokenAddress: route.srcToken,
      amount: amountParsed.toString(),
      bridgeDstChainId: route.toChainId,
      recipient: userAddress,
    };
  }, [userAddress, route, amountParsed, fromId]);

  const deposit = useTxFlow({
    txType: 'bridge-deposit',
    title: t('exec.depositCta', { amount: amt || '0', symbol: tok?.symbol ?? '' }),
    buildMetadata: () =>
      route && tok
        ? {
            fromChainId: route.fromChainId,
            toChainId: route.toChainId,
            srcToken: route.srcToken,
            dstToken: route.dstToken,
            amount: amountParsed.toString(),
            symbol: tok.symbol,
            trustModel: route.trustModel,
          }
        : null,
  });

  // Once the deposit is mined, find the projected transfer by its deposit tx.
  // deposit() returns bytes32 but a receipt carries no return value, and
  // decoding BridgeInitiated client-side would mean shipping event-decoding
  // just for this. Matching on the tx hash reuses the projection the relayer
  // already maintains; the cost is waiting one scan cycle.
  useEffect(() => {
    if (deposit.stage === 'confirmed' && deposit.hash) {
      setWatchTxHash(deposit.hash.toLowerCase());
      setAmt('');
    }
  }, [deposit.stage, deposit.hash]);

  const {
    data: transfers,
    isError: transfersError,
  } = useQuery<BridgeTransfer[]>({
    queryKey: ['bridge-transfers', userAddress],
    queryFn: () => getBridgeTransfers(userAddress!),
    enabled: Boolean(userAddress),
    // Poll faster while a just-sent deposit has not surfaced yet.
    refetchInterval: watchTxHash ? 5_000 : 30_000,
  });

  const watched = useMemo(
    () => (watchTxHash ? transfers?.find((x) => x.depositTxHash.toLowerCase() === watchTxHash) : undefined),
    [transfers, watchTxHash],
  );
  useEffect(() => {
    if (watched && watched.status !== 'INITIATED') setWatchTxHash(null);
  }, [watched]);

  const fromChain = supportedChains.find((c) => c.id === fromId);
  const toChain = supportedChains.find((c) => c.id === toId);
  const wrongChain = isConnected && walletChainId !== fromId;

  const handleDeposit = useCallback(async () => {
    if (!route || !tok || !userAddress || !executable) return;
    setStaleRoute(false);
    try {
      // Fresh server-side verdict at click time: the routes poll is up to 30s
      // stale and destination liquidity can drop inside that window. A 409 here
      // stops a transaction that would revert on-chain.
      await buildBridgeDeposit({
        fromChainId: fromId,
        toChainId: toId,
        srcToken: tok.address,
        amount: amountParsed.toString(),
        recipient: userAddress,
      });
    } catch (err) {
      if (err instanceof BridgeRouteUnavailableError) {
        setStaleRoute(true);
        void refetchRoutes();
        return;
      }
      app.toast((err as Error).message, 'warn');
      return;
    }

    // Send against the route the server just confirmed, not the local select
    // state — they agree today, but the validated route is the authority.
    deposit.execute({
      address: gateway,
      abi: bridgeGatewayAbi,
      functionName: 'deposit',
      args: [route.srcToken as `0x${string}`, amountParsed, BigInt(route.toChainId), userAddress],
    });
  }, [route, tok, userAddress, executable, fromId, toId, amountParsed, gateway, deposit, refetchRoutes, app]);

  const handlePrimary = () => {
    if (!isConnected) {
      app.openConnect();
      return;
    }
    if (needsApproval) {
      approval.approve(amountParsed);
      return;
    }
    void handleDeposit();
  };

  const fmtUnits = (raw: string, decimals: number) => {
    if (!raw) return '';
    try {
      return formatUnits(BigInt(raw), decimals);
    } catch {
      return raw;
    }
  };

  return (
    <div className="col gap-24" data-testid="bridge-page">
      <PageHeader eyebrow={t('headerEyebrow')} title={t('headerTitle')} kicker={t('headerKicker')} />

      {/* Trust model — rendered whenever a gateway is in play, and gating the
          first execution. This is a real property of the design, not a
          disclaimer template: the destination gateway verifies no proof. */}
      {gateway !== ZERO_ADDRESS && (
        <section className="block" style={{ padding: 18 }} data-testid="bridge-trust">
          <div className="row gap-10" style={{ alignItems: 'center' }}>
            <Icon name="warn" size={16} />
            <b style={{ fontFamily: 'var(--df)', fontSize: 14 }}>{t('trust.title')}</b>
            <span className="src-chip" style={{ marginLeft: 'auto' }}>{t('trust.pill')}</span>
          </div>
          <p style={{ marginTop: 10, lineHeight: 1.6, fontSize: 13 }}>{t('trust.body')}</p>
          <ul className="col mt-10" style={{ gap: 6, fontSize: 12, lineHeight: 1.6, paddingLeft: 18 }}>
            <li>{t('trust.pointRelayer')}</li>
            <li>{t('trust.pointOffline')}</li>
            <li>{t('trust.pointAdmin')}</li>
          </ul>
          {!trustAcked && (
            <button
              type="button"
              className="btn btn-sm mt-14"
              onClick={ackTrust}
              data-testid="bridge-trust-ack"
            >
              {t('trust.ack')}
            </button>
          )}
        </section>
      )}

      <section className="page-split" style={{ '--split-l': '1.1fr', '--split-r': '0.9fr' } as React.CSSProperties}>
        {/* ── Form ───────────────────────────────────────────────────── */}
        <div className="block" style={{ padding: 20 }}>
          <div className="col" style={{ gap: 12 }}>
            <label className="col" style={{ gap: 6 }}>
              <span className="eyebrow">{t('fromLabel')}</span>
              <select
                className="inp"
                value={fromId}
                onChange={(e) => setFromId(Number(e.target.value))}
                data-testid="bridge-from-chain"
              >
                {supportedChains.map((c) => (
                  <option key={c.id} value={c.id}>{c.name}</option>
                ))}
              </select>
            </label>

            <label className="col" style={{ gap: 6 }}>
              <span className="eyebrow">{t('toLabel')}</span>
              <select
                className="inp"
                value={toId}
                onChange={(e) => setToId(Number(e.target.value))}
                data-testid="bridge-to-chain"
              >
                {supportedChains.filter((c) => c.id !== fromId).map((c) => (
                  <option key={c.id} value={c.id}>{c.name}</option>
                ))}
              </select>
            </label>

            <label className="col" style={{ gap: 6 }}>
              <span className="eyebrow">{t('amountLabel')}</span>
              <div className="row gap-8">
                <input
                  className="inp"
                  inputMode="decimal"
                  value={amt}
                  onChange={(e) => setAmt(e.target.value)}
                  placeholder="0.0"
                  data-testid="bridge-amount"
                />
                <select
                  className="inp"
                  style={{ maxWidth: 130 }}
                  value={tok?.address ?? ''}
                  onChange={(e) => setTok(tokens.find((x) => x.address === e.target.value) ?? null)}
                  data-testid="bridge-token"
                >
                  {tokens.map((x) => (
                    <option key={x.address} value={x.address}>{x.symbol}</option>
                  ))}
                </select>
              </div>
            </label>
          </div>

          {/* Route facts — all read from chain */}
          {route && (
            <div className="block tight mt-16" style={{ background: 'var(--bg-2)', padding: 14, border: '2px solid var(--ink)', boxShadow: 'none' }}>
              <div className="meta-row">
                <span>{t('exec.receiveLabel')}</span>
                <b>{amt || '0'} {tok?.symbol} · {toChain?.name}</b>
              </div>
              <div className="meta-row mt-6">
                <span>{t('exec.liquidityLabel')}</span>
                <b data-testid="bridge-liquidity">
                  {route.dstLiquidity
                    ? `${fmtUnits(route.dstLiquidity, tok?.decimals ?? 18)} ${tok?.symbol}`
                    : t('exec.liquidityUnknown')}
                </b>
              </div>
              {route.minAmount && route.minAmount !== '0' && (
                <div className="meta-row mt-6">
                  <span>{t('exec.minAmountLabel')}</span>
                  <b>{fmtUnits(route.minAmount, tok?.decimals ?? 18)} {tok?.symbol}</b>
                </div>
              )}
              <p className="mono mt-10" style={{ fontSize: 11, color: 'var(--ink-2)', lineHeight: 1.6 }}>
                {t('exec.noFeeNote', { chain: fromChain?.name ?? '' })}
              </p>
            </div>
          )}

          {/* Blockers — every reason, each actionable by a different owner */}
          {route && !executable && route.blockers.length > 0 && (
            <div className="block tight mt-14" style={{ padding: 14, border: '2px dashed var(--ink)', boxShadow: 'none' }} data-testid="bridge-blockers">
              <div className="eyebrow">{t('blockers.title')}</div>
              <ul className="col mt-8" style={{ gap: 6, fontSize: 12, lineHeight: 1.6, paddingLeft: 18 }}>
                {route.blockers.map((code) => (
                  <li key={code} data-testid={`bridge-blocker-${code}`}>
                    {blockerText(t, code, route, tok)}
                  </li>
                ))}
              </ul>
            </div>
          )}

          {staleRoute && (
            <p className="mono mt-12" style={{ fontSize: 11, color: 'var(--o)', lineHeight: 1.6 }} data-testid="bridge-stale">
              {t('exec.routeStale')}
            </p>
          )}

          {/* Deterministic pre-sign review. Advisory — the CTA's gate is still
              route.executable, i.e. chain state. */}
          <TxPreflight input={preflightInput} className="mt-16" />

          {/* CTA */}
          <div className="mt-16">
            {!isConnected ? (
              <button className="btn btn-o" style={{ width: '100%' }} onClick={handlePrimary} data-testid="bridge-cta-connect">
                {t('exec.connectFirst')}
              </button>
            ) : wrongChain ? (
              <button className="btn" style={{ width: '100%' }} disabled data-testid="bridge-cta-switch">
                {t('exec.switchChain', { chain: fromChain?.name ?? '' })}
              </button>
            ) : !executable || !trustAcked ? (
              <button className="btn" style={{ width: '100%' }} disabled data-testid="bridge-cta-disabled">
                {t('exec.depositCta', { amount: amt || '0', symbol: tok?.symbol ?? '' })}
              </button>
            ) : needsApproval ? (
              <button
                className="btn btn-o"
                style={{ width: '100%' }}
                onClick={handlePrimary}
                disabled={approveBusy}
                data-testid="bridge-cta-approve"
              >
                {approveBusy ? t('exec.approving') : t('exec.approveCta', { symbol: tok?.symbol ?? '' })}
              </button>
            ) : (
              <button
                className="btn btn-o"
                style={{ width: '100%' }}
                onClick={handlePrimary}
                disabled={deposit.isWorking}
                data-testid="bridge-cta-deposit"
              >
                {deposit.isWorking
                  ? t('exec.depositing')
                  : t('exec.depositCta', { amount: amt || '0', symbol: tok?.symbol ?? '' })}
              </button>
            )}
          </div>

          <SourceMeta
            endpoint="POST /api/bridge/routes"
            state={routesError ? 'error' : routesLoading ? 'loading' : route ? 'data' : 'empty'}
            updatedAt={routesUpdatedAt}
            onRefresh={() => void refetchRoutes()}
          />
        </div>

        {/* ── Transfers ──────────────────────────────────────────────── */}
        <div className="col">
          <div className="block" style={{ padding: 18 }} data-testid="bridge-transfers">
            <div className="eyebrow">{t('transfers.title')}</div>

            {transfersError ? (
              <p className="mono mt-10" style={{ fontSize: 11, color: 'var(--o)', lineHeight: 1.6 }}>
                {t('transfers.unavailable')}
              </p>
            ) : watchTxHash && !watched ? (
              <p className="mono mt-10" style={{ fontSize: 11, color: 'var(--ink-2)', lineHeight: 1.6 }} data-testid="bridge-waiting">
                {t('transfers.waiting')}
              </p>
            ) : !transfers?.length ? (
              <p className="mono mt-10" style={{ fontSize: 11, color: 'var(--ink-2)' }}>
                {t('transfers.empty')}
              </p>
            ) : (
              <div className="col mt-10" style={{ gap: 10 }}>
                {transfers.map((x) => (
                  <div
                    key={x.transferId}
                    className="block tight"
                    style={{ padding: 12, background: 'var(--bg-2)', border: '2px solid var(--ink)', boxShadow: 'none' }}
                    data-testid={`bridge-transfer-${x.status}`}
                  >
                    <div className="meta-row">
                      <span className="mono" style={{ fontSize: 11 }}>
                        {fmtUnits(x.amount, tok?.decimals ?? 18)} · {x.srcChainId} → {x.dstChainId}
                      </span>
                      <b style={{ fontSize: 11 }}>{t(`transfers.status${x.status}`)}</b>
                    </div>
                    {x.lastError && (
                      <p className="mono mt-8" style={{ fontSize: 10, color: 'var(--o)', lineHeight: 1.5 }} data-testid="bridge-transfer-error">
                        {t('transfers.lastError')}: {x.lastError}
                      </p>
                    )}
                    {x.attempts > 0 && !x.lastError && (
                      <p className="mono mt-6" style={{ fontSize: 10, color: 'var(--ink-2)' }}>
                        {t('transfers.attempts', { count: x.attempts })}
                      </p>
                    )}
                  </div>
                ))}
              </div>
            )}
          </div>
        </div>
      </section>
    </div>
  );
}

/**
 * Render a blocker with its interpolated values. Two of the eleven carry
 * numbers the user needs in order to act (how much liquidity, what minimum);
 * the rest are plain sentences.
 */
function blockerText(
  t: ReturnType<typeof useTranslations<'bridge'>>,
  code: BridgeBlocker,
  route: BridgeRoute,
  tok: ChainTokenConfig | null,
): string {
  const decimals = tok?.decimals ?? 18;
  const safe = (raw: string) => {
    try {
      return formatUnits(BigInt(raw || '0'), decimals);
    } catch {
      return raw;
    }
  };
  switch (code) {
    case 'BELOW_MIN_AMOUNT':
      return t('blockers.BELOW_MIN_AMOUNT', { min: safe(route.minAmount), symbol: tok?.symbol ?? '' });
    case 'INSUFFICIENT_DESTINATION_LIQUIDITY':
      return t('blockers.INSUFFICIENT_DESTINATION_LIQUIDITY', {
        available: safe(route.dstLiquidity),
        needed: safe(route.amountIn),
      });
    default:
      return t(`blockers.${code}`);
  }
}
