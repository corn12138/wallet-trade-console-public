'use client';
import { useEffect, useMemo, useState } from 'react';
import { useAccount } from 'wagmi';
import { useQuery } from '@tanstack/react-query';
import { parseUnits } from 'viem';
import { useTranslations, useLocale } from 'next-intl';
import { TOKENS, type ChainTokenConfig } from '@wallet-trade/shared';
import { getSwapQuote } from '@/lib/api/atlas';
import { useApp } from '../AppContext';
import { Icon } from '../Icon';
import { PageHeader } from '../Common';
import { formatDiagnosticMessage, formatDiagnosticValue } from '../diagnostics';
import { useNow } from '../DataState';
import { fmt, fmtPct } from '../data';
import { paletteFor } from './assetUtils';
import { getOptionalContractAddress, routerAbi } from '@/lib/web3/contracts';
import { useDisplayChainId } from '@/hooks/useDisplayChainId';
import { useTokenApproval } from '@/hooks/web3/useTokenApproval';
import type { AtlasTxReviewInput } from '@/lib/api/atlas';
import { TxPreflight } from '../TxPreflight';
import { useTxFlow } from '@/hooks/web3/useTxFlow';
import { useTradingDefaults } from '@/lib/trading-defaults';
import { DEFAULT_TRADING_CHAIN_ID } from '@/lib/web3/trading-chain';

function SwapTokenChip({
  token,
  idx,
  onPick,
  tokens,
  excludeAddress,
}: {
  token: ChainTokenConfig | null;
  idx: number;
  onPick: (t: ChainTokenConfig) => void;
  tokens: ChainTokenConfig[];
  excludeAddress?: string;
}) {
  const app = useApp();
  if (!token) return null;
  const color = paletteFor(idx);
  return (
    <button
      className="btn btn-sm"
      onClick={() =>
        app.setModal({
          kind: 'tokenPicker',
          props: { onPick, exclude: excludeAddress, tokens },
        })
      }
      style={{
        background: `var(--${color})`,
        color: ['c', 'b', 'p', 'o'].includes(color) ? '#fff' : 'var(--ink)',
        padding: '6px 12px 6px 6px',
        gap: 8,
      }}
    >
      <span
        style={{
          width: 26,
          height: 26,
          borderRadius: 999,
          background: 'var(--paper)',
          color: 'var(--ink)',
          display: 'grid',
          placeItems: 'center',
          fontFamily: 'var(--df)',
          fontWeight: 900,
          fontSize: 12,
          border: '2px solid var(--ink)',
        }}
      >
        {token.symbol[0]}
      </span>
      {token.symbol}
      <Icon name="chevDown" size={12} />
    </button>
  );
}

const ZERO_ADDRESS = '0x0000000000000000000000000000000000000000' as const;
const DEADLINE_SECONDS = 20 * 60;
const QUOTE_REFRESH_MS = 12_000;

/**
 * Machine-readable quote panel state. Every branch is derived from real
 * inputs (router config, query status, quote provenance) — never invented.
 */
type SwapQuoteView =
  | 'no-router'
  | 'idle'
  | 'quoting'
  | 'error-pair'
  | 'error-timeout'
  | 'error-service'
  | 'fallback'
  | 'live';

function classifyQuoteError(message: string | undefined): 'error-pair' | 'error-timeout' | 'error-service' {
  const text = (message || '').toLowerCase();
  if (/http 4\d\d/.test(text) || text.includes('invalid') || text.includes('unsupported')) {
    return 'error-pair';
  }
  if (text.includes('timeout') || text.includes('timed out') || text.includes('abort')) {
    return 'error-timeout';
  }
  return 'error-service';
}

export function SwapPage() {
  const app = useApp();
  const t = useTranslations('swap');
  const tCommon = useTranslations('commonAtlas');
  const tDiag = useTranslations('diagnostics');
  const locale = useLocale();
  const { chainId, isFallback: isChainFallback } = useDisplayChainId();
  const { address: userAddress, isConnected } = useAccount();

  const tokens: ChainTokenConfig[] = useMemo(() => TOKENS[chainId] || [], [chainId]);
  const [inTok, setInTok] = useState<ChainTokenConfig | null>(null);
  const [outTok, setOutTok] = useState<ChainTokenConfig | null>(null);
  const [amtIn, setAmtIn] = useState('100');
  const { slippagePercent: defaultSlippage } = useTradingDefaults();
  const [slip, setSlip] = useState(0.5);
  const [slipTouched, setSlipTouched] = useState(false);
  const [flipped, setFlipped] = useState(false);

  // Seed the page's slippage from the persisted trading defaults until the
  // user picks a value here (settings changes then flow through for real).
  useEffect(() => {
    if (slipTouched) return;
    const parsed = Number(defaultSlippage);
    if (Number.isFinite(parsed) && parsed >= 0 && parsed <= 5) {
      setSlip(parsed);
    }
  }, [defaultSlippage, slipTouched]);

  const routerAddress = useMemo(
    () => (getOptionalContractAddress(chainId, 'router') as `0x${string}` | undefined) ?? null,
    [chainId],
  );

  useEffect(() => {
    if (!tokens.length) {
      setInTok(null);
      setOutTok(null);
      return;
    }
    if (!inTok || !tokens.find((t) => t.address === inTok.address)) setInTok(tokens[0]);
    if (!outTok || !tokens.find((t) => t.address === outTok.address)) setOutTok(tokens[1] || tokens[0]);
  }, [tokens, inTok, outTok]);

  const slippageBps = Math.round(slip * 100);
  const quoteEnabled = Boolean(inTok && outTok && Number(amtIn) > 0 && inTok!.address !== outTok!.address);

  const {
    data: quote,
    isFetching: quoteLoading,
    isError: quoteError,
    error: quoteErrorValue,
    dataUpdatedAt: quoteUpdatedAt,
    refetch: refetchQuote,
  } = useQuery({
    queryKey: ['atlas-design-swap-quote', chainId, inTok?.address, outTok?.address, amtIn, slippageBps],
    queryFn: () =>
      getSwapQuote({
        chainId,
        tokenIn: inTok!.address,
        tokenOut: outTok!.address,
        amountIn: amtIn,
        tokenInDecimals: inTok!.decimals,
        tokenOutDecimals: outTok!.decimals,
        slippageBps,
      }),
    enabled: quoteEnabled,
    staleTime: 8_000,
    refetchInterval: QUOTE_REFRESH_MS,
  });

  // Executable gating: a swap may only be submitted from a LIVE router
  // quote (`executable: true` from the API; older API responses without
  // the field are treated by provenance). Fallback estimates are
  // display-only — a wallet tx built from a made-up 0.997× number is a
  // fake success path. NEXT_PUBLIC_ALLOW_FALLBACK_SWAP_EXECUTION=1 is a
  // deliberately loud escape hatch for local demos without RPC, and it is
  // HARD-DISABLED in production builds: the NODE_ENV check compiles to a
  // constant `false` under `next build`, so no production env var can
  // resurrect fallback execution.
  const allowFallbackExecution =
    process.env.NODE_ENV !== 'production' &&
    process.env.NEXT_PUBLIC_ALLOW_FALLBACK_SWAP_EXECUTION === '1';
  const quoteExecutable = Boolean(
    quote && (quote.executable ?? quote.routeSource === 'router'),
  ) || (Boolean(quote) && allowFallbackExecution);

  // Typed diagnostics state for the quote panel + recovery UI.
  const quoteView: SwapQuoteView = !routerAddress
    ? 'no-router'
    : !quoteEnabled
      ? 'idle'
      : quoteError
        ? classifyQuoteError((quoteErrorValue as { message?: string } | null)?.message)
        : quote
          ? quoteExecutable
            ? 'live'
            : 'fallback'
          : 'quoting';

  const now = useNow(1000);
  const nextRefreshSec = quoteUpdatedAt
    ? Math.max(0, Math.ceil((quoteUpdatedAt + QUOTE_REFRESH_MS - now) / 1000))
    : null;
  const clockFormatter = useMemo(
    () => new Intl.DateTimeFormat(locale, { hour: '2-digit', minute: '2-digit', second: '2-digit' }),
    [locale],
  );

  const amountInParsed = useMemo(() => {
    if (!inTok || !amtIn || Number(amtIn) <= 0) return 0n;
    try {
      return parseUnits(amtIn, inTok.decimals);
    } catch {
      return 0n;
    }
  }, [amtIn, inTok]);

  const approval = useTokenApproval(
    (inTok?.address as `0x${string}` | undefined) ?? ZERO_ADDRESS,
    routerAddress ?? ZERO_ADDRESS,
  );
  const needsApproval = amountInParsed > 0n && !approval.isApproved(amountInParsed);

  // The review must describe the transaction the CTA will actually send, so it
  // follows the CTA: approve while an allowance is missing, swap once it isn't.
  // Reviewing the swap while the button says "Approve" would be a verdict on a
  // transaction the user isn't about to sign.
  const preflightInput: AtlasTxReviewInput | null = useMemo(() => {
    if (!userAddress || !inTok || !outTok || amountInParsed <= 0n) return null;
    if (needsApproval) {
      if (!routerAddress) return null;
      return {
        operationType: 'approve',
        fromAddress: userAddress,
        chainId,
        tokenAddress: inTok.address,
        spender: routerAddress,
        amount: amtIn,
        tokenDecimals: inTok.decimals,
      };
    }
    if (!quoteExecutable) return null;
    return {
      operationType: 'swap',
      fromAddress: userAddress,
      chainId,
      tokenIn: inTok.address,
      tokenOut: outTok.address,
      amount: amtIn,
      tokenInDecimals: inTok.decimals,
      tokenOutDecimals: outTok.decimals,
      slippageBps,
      recipient: userAddress,
    };
  }, [
    userAddress, inTok, outTok, amountInParsed, needsApproval,
    routerAddress, amtIn, chainId, slippageBps, quoteExecutable,
  ]);

  const swap = useTxFlow({
    txType: 'swap',
    title: t('txTitle'),
    buildSummary: () =>
      inTok && outTok && quote
        ? `${amtIn} ${inTok.symbol} → ${fmt(Number(quote.amountOut), 4)} ${outTok.symbol}\nMin out: ${fmt(Number(quote.minimumReceived), 6)} ${outTok.symbol}`
        : '',
    buildMetadata: () =>
      inTok && outTok && quote
        ? {
            tokenIn: inTok.address,
            tokenOut: outTok.address,
            tokenInSymbol: inTok.symbol,
            tokenOutSymbol: outTok.symbol,
            amountIn: amtIn,
            estimatedOut: quote.amountOut,
            minimumReceived: quote.minimumReceived,
            slippageBps,
            path: quote.path,
          }
        : null,
    toastOnSuccess: t('toastSwapConfirmed'),
  });

  useEffect(() => {
    if (swap.stage === 'confirmed') setAmtIn('');
  }, [swap.stage]);

  const flip = () => {
    setFlipped((f) => !f);
    if (inTok && outTok) {
      const a = inTok;
      setInTok(outTok);
      setOutTok(a);
    }
  };

  const handleApprove = () => {
    if (amountInParsed === 0n) return;
    approval.approve(amountInParsed);
    app.toast(t('toastApproving', { symbol: inTok!.symbol }), 'warn');
  };

  const handleSwap = () => {
    if (!quote || !quoteExecutable || !inTok || !outTok || !routerAddress || !userAddress) return;
    if (amountInParsed === 0n) return;

    const amountOutMin = parseUnits(quote.minimumReceived, outTok.decimals);
    const deadline = BigInt(Math.floor(Date.now() / 1000) + DEADLINE_SECONDS);

    swap.execute({
      address: routerAddress,
      abi: routerAbi,
      functionName: 'swapExactTokensForTokens',
      args: [
        amountInParsed,
        amountOutMin,
        [inTok.address as `0x${string}`, outTok.address as `0x${string}`],
        userAddress,
        deadline,
      ],
    });
  };

  const handlePrimary = () => {
    if (!isConnected || app.walletState !== 'connected') {
      app.openConnect();
      return;
    }
    if (!quote || !quoteExecutable || !routerAddress) return;
    if (needsApproval) handleApprove();
    else handleSwap();
  };

  const approveBusy = approval.isPending || approval.isConfirming;

  if (!tokens.length) {
    return (
      <div className="col gap-24">
        <PageHeader
          eyebrow={t('headerEyebrow')}
          title={t('headerTitle')}
          kicker={t('headerKicker')}
        />
        <div className="block" style={{ padding: 36, textAlign: 'center' }}>
          <div className="h-display" style={{ fontSize: 22 }}>{t('noTokensTitle', { chainId })}</div>
          <p style={{ marginTop: 8, color: 'var(--ink-2)' }}>{t('noTokensBody')}</p>
          <button
            className="btn btn-y mt-14"
            onClick={() => app.switchChain({ id: DEFAULT_TRADING_CHAIN_ID, name: 'Sepolia' })}
          >
            {tCommon('switchToSepolia')}
          </button>
        </div>
      </div>
    );
  }

  // API warnings localized through the diagnostics layer: known messages
  // render translated; unknown ones keep the raw string only as a labelled
  // technical detail (never as primary zh copy).
  const renderWarnings = (warnings: string[]) =>
    warnings.map((w, i) => {
      const display = formatDiagnosticMessage(tDiag, locale, w);
      return (
        <div key={i} className="mono" style={{ fontSize: 11, color: 'var(--ink-2)', marginTop: 4 }}>
          · {display.text}
          {display.detail && (
            <div style={{ marginTop: 2, opacity: 0.75 }}>
              {tDiag('apiDetailLabel')}: {display.detail}
            </div>
          )}
        </div>
      );
    });

  const statusLabel: Record<SwapQuoteView, string> = {
    'no-router': t('quotePanel.statusNoRouter'),
    idle: t('quotePanel.statusIdle'),
    quoting: t('quotePanel.statusQuoting'),
    'error-pair': t('quotePanel.statusErrorPair'),
    'error-timeout': t('quotePanel.statusErrorTimeout'),
    'error-service': t('quotePanel.statusErrorService'),
    fallback: t('quotePanel.statusFallback'),
    live: t('quotePanel.statusLive'),
  };

  const errorMessage = (quoteErrorValue as { message?: string } | null)?.message?.slice(0, 120) ?? '';

  return (
    <div className="col gap-24">
      <PageHeader
        eyebrow={t('headerEyebrow')}
        title={t('headerTitle')}
        kicker={t('headerKicker')}
      />

      <section className="page-split" style={{ '--split-l': '1.3fr', '--split-r': '0.7fr' } as React.CSSProperties}>
        {/* SWAP CARD */}
        <div className="block" style={{ padding: 22 }}>
          <div className="row between">
            <span className="eyebrow">{t('fromLabel')}</span>
            <span className="mono" style={{ color: 'var(--ink-2)', fontSize: 12 }}>{inTok?.name}</span>
          </div>
          <div className="field mt-6" style={{ display: 'flex', alignItems: 'center', gap: 14 }}>
            <input
              type="number"
              value={amtIn}
              onChange={(e) => setAmtIn(e.target.value)}
              placeholder="0.0"
              style={{ flex: 1, fontSize: 32 }}
              min="0"
              step="0.0001"
            />
            <SwapTokenChip
              token={inTok}
              idx={inTok ? tokens.findIndex((t) => t.address === inTok.address) : 0}
              onPick={(t) => setInTok(t)}
              tokens={tokens}
              excludeAddress={outTok?.address}
            />
          </div>

          <div className="row" style={{ justifyContent: 'center', margin: '-8px 0', position: 'relative', zIndex: 2 }}>
            <button
              className="btn btn-xs"
              onClick={flip}
              style={{ width: 40, height: 40, padding: 0, borderRadius: 999, transition: 'transform .3s ease', transform: flipped ? 'rotate(180deg)' : '' }}
              data-tip={t('flipAction')}
              aria-label={t('flipAction')}
            >
              <Icon name="swap" size={16} />
            </button>
          </div>

          <div className="row between">
            <span className="eyebrow">{t('toEstimated')}</span>
            <span className="mono" style={{ color: 'var(--ink-2)', fontSize: 12 }}>{outTok?.name}</span>
          </div>
          <div className="field mt-6" style={{ display: 'flex', alignItems: 'center', gap: 14 }}>
            {quoteLoading ? (
              <div className="skel lg" style={{ flex: 1 }} />
            ) : (
              <input
                value={quote ? fmt(Number(quote.amountOut), 4) : ''}
                readOnly
                placeholder="0.0"
                style={{ flex: 1, fontSize: 32, color: 'var(--ink-2)' }}
              />
            )}
            <SwapTokenChip
              token={outTok}
              idx={outTok ? tokens.findIndex((t) => t.address === outTok.address) : 1}
              onPick={(t) => setOutTok(t)}
              tokens={tokens}
              excludeAddress={inTok?.address}
            />
          </div>

          <div className="row gap-6 mt-14">
            <span className="eyebrow">{t('slippageLabel')}</span>
            <div style={{ display: 'flex', gap: 4, background: 'var(--bg-2)', border: '2px solid var(--ink)', borderRadius: 10, padding: 3 }}>
              {[0.1, 0.5, 1.0].map((s) => (
                <button
                  key={s}
                  onClick={() => {
                    setSlipTouched(true);
                    setSlip(s);
                  }}
                  style={{
                    padding: '6px 10px',
                    borderRadius: 8,
                    fontFamily: 'var(--df)',
                    fontWeight: 700,
                    fontSize: 11,
                    background: slip === s ? 'var(--y)' : 'transparent',
                    boxShadow: slip === s ? '0 2px 0 0 var(--ink)' : 'none',
                  }}
                >
                  {s}%
                </button>
              ))}
            </div>
          </div>

          {/* receipt */}
          <div className="block tight mt-14" style={{ background: 'var(--bg-2)', padding: 14, border: '2px dashed var(--ink)', boxShadow: 'none' }}>
            <div className="meta-row">
              <span>{t('rate')}</span>
              <b>
                {quote
                  ? `1 ${inTok!.symbol} ≈ ${fmt(Number(quote.amountOut) / Number(amtIn || 1), 6)} ${outTok!.symbol}`
                  : '—'}
              </b>
            </div>
            <div className="meta-row" style={{ marginTop: 6 }}>
              <span>{t('minReceived')}</span>
              <b>{quote ? `${fmt(Number(quote.minimumReceived), 4)} ${outTok!.symbol}` : '—'}</b>
            </div>
            <div className="meta-row" style={{ marginTop: 6 }}>
              <span>{t('priceImpact')}</span>
              <b className={quote && Math.abs(quote.priceImpactPct) > 1 ? 'tone-warn' : ''}>
                {quote ? fmtPct(quote.priceImpactPct) : '—'}
              </b>
            </div>
            <div className="meta-row" style={{ marginTop: 6 }}>
              <span>{t('sourceLabel')}</span>
              <b>{formatDiagnosticValue(tDiag, quote?.routeSource)}</b>
            </div>
            <div className="meta-row" style={{ marginTop: 6 }}>
              <span>{t('routeLabel')}</span>
              <b style={{ fontFamily: 'var(--mf)', fontSize: 11, wordBreak: 'break-all', textAlign: 'right' }}>
                {quote?.path?.length
                  ? quote.path.map((a) => `${a.slice(0, 6)}…${a.slice(-4)}`).join(' → ')
                  : '—'}
              </b>
            </div>
            {quote?.warnings && quote.warnings.length > 0 && (
              <div className="mt-14" style={{ borderTop: '2px solid var(--ink)', paddingTop: 10 }}>
                <div className="eyebrow tone-warn">{t('warningsLabel')}</div>
                {renderWarnings(quote.warnings)}
              </div>
            )}
          </div>

          {/* Fallback recovery panel: the CTA below stays disabled; this
              explains why and what to try, from real API warnings. */}
          {quoteView === 'fallback' && (
            <div
              className="block tight mt-14"
              style={{ background: 'var(--y)', padding: 14, border: '2px solid var(--ink)', boxShadow: 'none' }}
              data-testid="swap-fallback-panel"
            >
              <div className="row between">
                <div style={{ fontFamily: 'var(--df)', fontWeight: 800, fontSize: 13 }}>
                  {t('fallbackPanel.title')}
                </div>
                <button
                  type="button"
                  className="btn btn-xs"
                  onClick={() => refetchQuote()}
                  disabled={quoteLoading}
                  data-testid="swap-retry-quote"
                >
                  <Icon name="refresh" size={12} /> {t('fallbackPanel.retryCta')}
                </button>
              </div>
              <p style={{ marginTop: 6, fontSize: 12.5, lineHeight: 1.55 }}>{t('fallbackPanel.body')}</p>
              {quote?.warnings && quote.warnings.length > 0 && (
                <div style={{ marginTop: 8 }}>
                  <div className="eyebrow">{t('fallbackPanel.apiReasons')}</div>
                  {renderWarnings(quote.warnings)}
                </div>
              )}
              <div style={{ marginTop: 8 }}>
                <div className="eyebrow">{t('fallbackPanel.recoveryTitle')}</div>
                <ul style={{ margin: '4px 0 0', paddingLeft: 18, fontSize: 12, lineHeight: 1.6 }}>
                  <li>{t('fallbackPanel.recoveryRetry')}</li>
                  <li>{t('fallbackPanel.recoveryPair')}</li>
                  <li>{t('fallbackPanel.recoveryNetwork')}</li>
                  <li>{t('fallbackPanel.recoveryRpc')}</li>
                </ul>
              </div>
            </div>
          )}

          {/* Advisory pre-sign review, above the CTA it describes. The CTA's
              gate stays routerAddress + quoteExecutable + allowance; this only
              says what is about to happen. */}
          <TxPreflight input={preflightInput} className="mt-14" />

          {/* CTA chain — provenance-gated, unchanged semantics */}
          {!routerAddress ? (
            <button className="btn btn-y mt-14" style={{ width: '100%' }} disabled>
              {t('cta.routerMissing', { chainId })}
            </button>
          ) : !quote && !quoteLoading && !quoteError ? (
            <button className="btn btn-y mt-14" style={{ width: '100%' }} disabled>
              {t('cta.enterAmount')}
            </button>
          ) : quoteError ? (
            <div
              className="block tight mt-14"
              style={{ background: 'var(--o)', color: '#fff', padding: 12, border: '2px solid var(--ink)' }}
              data-testid="swap-quote-error"
            >
              <div style={{ fontFamily: 'var(--df)', fontWeight: 800, fontSize: 13 }}>
                {quoteView === 'error-pair'
                  ? t('errorPanel.pairTitle')
                  : quoteView === 'error-timeout'
                    ? t('errorPanel.timeoutTitle')
                    : t('errorPanel.serviceTitle')}
              </div>
              <p style={{ marginTop: 4, fontSize: 12, lineHeight: 1.5 }}>
                {quoteView === 'error-pair'
                  ? t('errorPanel.pairBody', { message: errorMessage })
                  : quoteView === 'error-timeout'
                    ? t('errorPanel.timeoutBody')
                    : t('errorPanel.serviceBody', { message: errorMessage })}
              </p>
              <button
                type="button"
                className="btn btn-xs mt-6"
                onClick={() => refetchQuote()}
                data-testid="swap-retry-quote"
              >
                <Icon name="refresh" size={12} /> {t('errorPanel.retryCta')}
              </button>
            </div>
          ) : quote && !quoteExecutable ? (
            // Fallback estimate: display-only. No approval, no swap — the
            // number is a 0.997× heuristic, not a router quote.
            <button className="btn btn-y mt-14" style={{ width: '100%' }} disabled data-testid="swap-cta-fallback-disabled">
              {t('cta.fallbackDisabled')}
            </button>
          ) : swap.isWorking ? (
            <button className="btn btn-c mt-14" style={{ width: '100%' }} disabled>
              <span className="spinner" /> {swap.stage === 'submitting' ? t('cta.confirmWallet') : t('cta.swapping')}
            </button>
          ) : approveBusy ? (
            <button className="btn btn-c mt-14" style={{ width: '100%' }} disabled>
              <span className="spinner" /> {t('cta.approvingBusy', { symbol: inTok!.symbol })}
            </button>
          ) : needsApproval ? (
            <button className="btn btn-o mt-14" style={{ width: '100%' }} onClick={handlePrimary} data-testid="swap-cta-approve">
              {t('cta.approve', { symbol: inTok!.symbol })}
            </button>
          ) : quote ? (
            <button className="btn btn-g mt-14" style={{ width: '100%' }} onClick={handlePrimary} data-testid="swap-cta-submit">
              {t('cta.swapSubmit', {
                amountIn: amtIn,
                tokenIn: inTok!.symbol,
                amountOut: fmt(Number(quote.amountOut), 4),
                tokenOut: outTok!.symbol,
              })}
            </button>
          ) : (
            <button className="btn btn-y mt-14" style={{ width: '100%' }} disabled>
              {t('cta.review')}
            </button>
          )}
        </div>

        {/* RIGHT side info */}
        <div className="col">
          <div className="block bg-d" style={{ padding: 20 }} data-testid="swap-quote-panel">
            <div className="eyebrow" style={{ color: 'var(--y)' }}>{t('quotePanel.title')}</div>
            <div style={{ fontFamily: 'var(--mf)', fontSize: 12, color: 'var(--bg)', marginTop: 8, lineHeight: 1.7 }}>
              {(
                [
                  [t('quotePanel.status'), statusLabel[quoteView]],
                  [t('quotePanel.source'), formatDiagnosticValue(tDiag, quote?.routeSource)],
                  [t('quotePanel.quoteStatus'), formatDiagnosticValue(tDiag, quote?.quoteStatus)],
                  [
                    t('quotePanel.executable'),
                    quote ? (quoteExecutable ? t('quotePanel.executableYes') : t('quotePanel.executableNo')) : '—',
                  ],
                  [
                    t('quotePanel.chain'),
                    isChainFallback ? t('quotePanel.chainDefault', { chainId }) : String(chainId),
                  ],
                  [
                    t('quotePanel.pair'),
                    inTok && outTok ? `${inTok.symbol} → ${outTok.symbol}` : '—',
                  ],
                  [
                    t('quotePanel.router'),
                    quote?.routerAddress
                      ? `${quote.routerAddress.slice(0, 6)}…${quote.routerAddress.slice(-4)}`
                      : routerAddress
                        ? `${routerAddress.slice(0, 6)}…${routerAddress.slice(-4)}`
                        : '—',
                  ],
                  [t('quotePanel.slippage'), `${slip}%`],
                  [
                    t('quotePanel.lastQuote'),
                    quoteUpdatedAt ? clockFormatter.format(quoteUpdatedAt) : t('quotePanel.never'),
                  ],
                  [
                    t('quotePanel.nextRefresh'),
                    nextRefreshSec !== null ? t('quotePanel.inSeconds', { seconds: nextRefreshSec }) : '—',
                  ],
                ] as [string, string][]
              ).map(([k, v]) => (
                <div key={k} style={{ display: 'flex', justifyContent: 'space-between', gap: 10 }}>
                  <span style={{ opacity: 0.6 }}>{k}</span>
                  <b style={{ color: 'var(--bg)', textAlign: 'right' }} data-testid={k === t('quotePanel.status') ? 'swap-quote-status' : undefined}>
                    {v}
                  </b>
                </div>
              ))}
            </div>
          </div>
          <div className="block bg-y" style={{ padding: 18 }}>
            <Icon name="warn" size={20} />
            <div className="h-display" style={{ fontSize: 18, marginTop: 10 }}>{t('headsUpTitle')}</div>
            <p style={{ marginTop: 6, color: 'var(--ink)', lineHeight: 1.5, fontSize: 13 }}>
              {t('headsUpBody', { slip })}
            </p>
          </div>
        </div>
      </section>
    </div>
  );
}
