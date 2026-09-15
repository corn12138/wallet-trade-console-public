'use client';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useAccount } from 'wagmi';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { erc20Abi, formatUnits, parseUnits } from 'viem';
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
import { useTokenBalance } from '@/hooks/web3/useTokenBalance';
import type { AtlasTxReviewInput } from '@/lib/api/atlas';
import { TxPreflight } from '../TxPreflight';
import { useTxIntent } from '@/hooks/web3/txIntent/useTxIntent';
import { freezeStep } from '@/hooks/web3/txIntent/intent';
import { technicalHint } from '@/hooks/web3/txIntent/errors';
import type { TxStep } from '@/hooks/web3/txIntent/types';
import type { TxIntentModalProps } from '../Common';
import { buildTransactionExplorerUrl } from '@/lib/web3/explorer';
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
const QUOTE_REFRESH_MS = 12_000;
const HIGH_IMPACT_PCT = 5;

// Deadline comes from the shared trading defaults (Settings → "Trading
// defaults"), same store the slippage seed uses; out-of-range values fall
// back to the old hardcoded 20 minutes.
function resolveDeadlineSeconds(deadlineMinutes: string): number {
  const minutes = Number(deadlineMinutes);
  if (Number.isFinite(minutes) && minutes >= 1 && minutes <= 120) {
    return Math.round(minutes * 60);
  }
  return 20 * 60;
}

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
  const tModal = useTranslations('atlasShell.modals');
  const tDiag = useTranslations('diagnostics');
  const locale = useLocale();
  const { chainId, isFallback: isChainFallback } = useDisplayChainId();
  const { address: userAddress, isConnected } = useAccount();

  const tokens: ChainTokenConfig[] = useMemo(() => TOKENS[chainId] || [], [chainId]);
  const [inTok, setInTok] = useState<ChainTokenConfig | null>(null);
  const [outTok, setOutTok] = useState<ChainTokenConfig | null>(null);
  const [amtIn, setAmtIn] = useState('');
  const { slippagePercent: defaultSlippage, deadlineMinutes } = useTradingDefaults();
  const [slip, setSlip] = useState(0.5);
  const [slipTouched, setSlipTouched] = useState(false);
  const [slipCustom, setSlipCustom] = useState('');
  const [flipped, setFlipped] = useState(false);
  const [rateInverted, setRateInverted] = useState(false);
  // High price impact needs an explicit acknowledgement before the CTA arms;
  // it resets whenever the trade being acknowledged changes.
  const [impactAck, setImpactAck] = useState(false);
  useEffect(() => {
    setImpactAck(false);
  }, [inTok?.address, outTok?.address, amtIn]);

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

  // An older API may still send a synthetic fallback amount; never display it as a quote.
  const hasLiveAmounts = Boolean(quote && quote.routeSource === 'router' && quote.executable !== false);

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

  const highImpact = Boolean(quote && Math.abs(quote.priceImpactPct) > HIGH_IMPACT_PCT);

  // Route hops shown as symbols where the token list knows the address —
  // 0x1234…abcd is provenance, USDC → WETH is information.
  const symbolByAddress = useMemo(() => {
    const map = new Map<string, string>();
    for (const token of tokens) map.set(token.address.toLowerCase(), token.symbol);
    return map;
  }, [tokens]);

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

  // Balance awareness for the pay side: display, Max/Half fills, and an
  // insufficient gate so the wallet is never asked to sign an amount the
  // account can't cover. Only gate on a *loaded* balance — while it is still
  // fetching, the CTA chain proceeds as before instead of flashing a claim.
  const inBalance = useTokenBalance((inTok?.address as `0x${string}` | undefined) ?? undefined);
  const hasLoadedBalance = isConnected && inBalance.balance !== undefined;
  const insufficientBalance =
    hasLoadedBalance && amountInParsed > 0n && amountInParsed > inBalance.balance!;
  const balanceDisplay = hasLoadedBalance ? fmt(Number(inBalance.formatted), 4) : '—';

  const fillFromBalance = (fraction: number) => {
    if (!hasLoadedBalance || !inTok) return;
    const value = fraction >= 1 ? inBalance.balance! : inBalance.balance! / 2n;
    setAmtIn(formatUnits(value, inBalance.decimals));
  };


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

  // --- Shared transaction lifecycle (ADR 0008).
  //
  // The step is frozen at review from the quote the user was LOOKING AT, not
  // the one that refetches every 12 s underneath them: `reviewedQuote` is
  // captured when the CTA is clicked and is what `build` reads at both review
  // and send, so a quote tick cannot read as drift. Whether the live quote has
  // moved unsafely is a separate, value-based check (`liveMinAmountOut`).
  const reviewedQuote = useRef<typeof quote>(undefined);
  const queryClient = useQueryClient();

  const build = useCallback((now: number): TxStep[] | null => {
    const q = reviewedQuote.current;
    if (!userAddress || !inTok || !outTok || !routerAddress || amountInParsed <= 0n) return null;
    const steps: TxStep[] = [];
    const inAddress = inTok.address as `0x${string}`;
    const outAddress = outTok.address as `0x${string}`;
    if (needsApproval) {
      steps.push(freezeStep({
        kind: 'approve',
        actionType: 'approve',
        owner: userAddress,
        targetChainId: chainId,
        target: inAddress,
        abi: erc20Abi,
        functionName: 'approve',
        // Exactly the amount, never unlimited — the same bounded approval the
        // page has always sent.
        args: [routerAddress, amountInParsed],
        token: { address: inAddress, symbol: inTok.symbol, decimals: inTok.decimals, amount: amountInParsed },
        expectsIndexing: false,
        metadata: { spender: routerAddress, requestedAllowance: amountInParsed.toString() },
        now,
      }));
    }
    if (q && quoteExecutable) {
      const amountOutMin = parseUnits(q.minimumReceived, outTok.decimals);
      const deadline = BigInt(Math.floor(now / 1000) + resolveDeadlineSeconds(deadlineMinutes));
      steps.push(freezeStep({
        kind: 'action',
        actionType: 'swap',
        owner: userAddress,
        targetChainId: chainId,
        target: routerAddress,
        abi: routerAbi,
        functionName: 'swapExactTokensForTokens',
        args: [amountInParsed, amountOutMin, [inAddress, outAddress], userAddress, deadline],
        token: { address: inAddress, symbol: inTok.symbol, decimals: inTok.decimals, amount: amountInParsed },
        guard: {
          slippageBps,
          minAmountOut: amountOutMin,
          outToken: { symbol: outTok.symbol, decimals: outTok.decimals },
          deadline,
        },
        expectsIndexing: true,
        metadata: {
          tokenIn: inAddress,
          tokenOut: outAddress,
          tokenInSymbol: inTok.symbol,
          tokenOutSymbol: outTok.symbol,
          amountIn: amtIn,
          estimatedOut: q.amountOut,
          minimumReceived: q.minimumReceived,
          slippageBps,
          path: q.path,
        },
        now,
      }));
    }
    return steps.length ? steps : null;
  }, [userAddress, inTok, outTok, routerAddress, amountInParsed, needsApproval, chainId, quoteExecutable, deadlineMinutes, slippageBps, amtIn]);

  const liveMinAmountOut = useMemo(() => {
    if (!quote || !outTok) return undefined;
    try {
      return parseUnits(quote.minimumReceived, outTok.decimals);
    } catch {
      return undefined;
    }
  }, [quote, outTok]);

  const intent = useTxIntent({
    build,
    targetChainId: chainId,
    viewerKey: userAddress ?? null,
    liveAllowance: approval.allowance,
    liveMinAmountOut,
    onFinalized: (step) => {
      if (step.kind === 'approve') {
        app.toast(t('toastApproved', { symbol: inTok?.symbol ?? '' }), 'ok');
        // The allowance read is a wagmi readContract query on a 5 s poll; ask
        // for it now so the CTA flips to "Swap" as soon as the chain says so.
        void queryClient.invalidateQueries({
          predicate: (query) => Array.isArray(query.queryKey) && query.queryKey[0] === 'readContract',
        });
        return;
      }
      app.toast(t('toastSwapConfirmed'), 'ok');
      setAmtIn('');
      void queryClient.invalidateQueries({
        predicate: (query) => Array.isArray(query.queryKey) && (query.queryKey[0] === 'readContract' || query.queryKey[0] === 'balance'),
      });
    },
  });

  // The modal is a projection of the engine: every state change re-renders
  // it from codes, and the frozen facts it shows are the step's own review.
  const activeStep = intent.activeStep;
  const modalOpenFor = useRef<string | null>(null);
  useEffect(() => {
    if (intent.state === 'DRAFT' || !activeStep) return;
    const stepTitle = activeStep.kind === 'approve'
      ? t('intent.stepApprove', { symbol: activeStep.review.token?.symbol ?? '' })
      : t('intent.stepSwap', { tokenIn: inTok?.symbol ?? '', tokenOut: outTok?.symbol ?? '' });
    const props: TxIntentModalProps = {
      mode: 'intent',
      phase: intent.phase,
      state: intent.state,
      title: t('txTitle'),
      stepTitle,
      step: { current: intent.activeStepIndex + 1, total: intent.steps.length },
      review: activeStep.review,
      expiresAt: activeStep.expiresAt,
      expectsIndexing: activeStep.expectsIndexing,
      indexing: intent.indexing,
      hash: intent.hash,
      replacedByHash: intent.replacedByHash,
      explorerUrl: intent.hash ? buildTransactionExplorerUrl(intent.hash, chainId) : null,
      errorCode: intent.errorCode,
      invalidationReason: intent.invalidationReason,
      technicalHint: technicalHint(intent.rawError),
      onSign: () => { void intent.send(); },
      onRetry: () => { handlePrimaryRef.current?.(); },
    };
    modalOpenFor.current = activeStep.stepId;
    app.setModal({ kind: 'tx', props });
    // app/t are stable providers; the modal must track the engine, not them.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [intent.state, intent.phase, intent.hash, intent.replacedByHash, intent.indexing, intent.errorCode, intent.invalidationReason, activeStep]);

  const flip = () => {
    setFlipped((f) => !f);
    if (inTok && outTok) {
      const a = inTok;
      setInTok(outTok);
      setOutTok(a);
    }
  };

  // One click → review. The wallet is only asked from the modal's "Sign",
  // after the frozen facts have been shown (contract §WP1.1.3).
  const handlePrimary = () => {
    if (!isConnected || app.walletState !== 'connected') {
      app.openConnect();
      return;
    }
    if (!quote || !quoteExecutable || !routerAddress || insufficientBalance) return;
    if (highImpact && !impactAck) return;
    if (amountInParsed === 0n) return;
    reviewedQuote.current = quote;
    const blocked = intent.review();
    if (blocked) {
      app.toast(tModal(`intent.err.${blocked}`), 'err');
    }
  };
  const handlePrimaryRef = useRef(handlePrimary);
  handlePrimaryRef.current = handlePrimary;


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
            <span className="row" style={{ alignItems: 'center', gap: 8 }}>
              <span className="mono" style={{ color: 'var(--ink-2)', fontSize: 12 }}>{inTok?.name}</span>
              {isConnected && (
                <>
                  <span
                    className="mono"
                    data-testid="swap-balance"
                    style={{ color: insufficientBalance ? 'var(--neg)' : 'var(--ink-2)', fontSize: 12 }}
                  >
                    {t('balanceLabel', { amount: balanceDisplay })}
                  </span>
                  <button
                    type="button"
                    className="btn btn-xs"
                    onClick={() => fillFromBalance(0.5)}
                    disabled={!hasLoadedBalance}
                  >
                    {t('balanceHalf')}
                  </button>
                  <button
                    type="button"
                    className="btn btn-xs"
                    onClick={() => fillFromBalance(1)}
                    disabled={!hasLoadedBalance}
                    data-testid="swap-balance-max"
                  >
                    {t('balanceMax')}
                  </button>
                </>
              )}
            </span>
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
              data-testid="swap-amount-in"
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
                value={hasLiveAmounts && quote ? fmt(Number(quote.amountOut), 4) : ''}
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

          <div className="row gap-6 mt-14" style={{ alignItems: 'center', flexWrap: 'wrap' }}>
            <span className="eyebrow">{t('slippageLabel')}</span>
            <div style={{ display: 'flex', gap: 4, background: 'var(--bg-2)', border: '2px solid var(--ink)', borderRadius: 10, padding: 3 }}>
              {[0.1, 0.5, 1.0].map((s) => (
                <button
                  key={s}
                  onClick={() => {
                    setSlipTouched(true);
                    setSlipCustom('');
                    setSlip(s);
                  }}
                  style={{
                    padding: '6px 10px',
                    borderRadius: 8,
                    fontFamily: 'var(--df)',
                    fontWeight: 700,
                    fontSize: 11,
                    background: slip === s && slipCustom === '' ? 'var(--y)' : 'transparent',
                    boxShadow: slip === s && slipCustom === '' ? '0 2px 0 0 var(--ink)' : 'none',
                  }}
                >
                  {s}%
                </button>
              ))}
              <input
                type="number"
                value={slipCustom}
                onChange={(e) => {
                  const raw = e.target.value;
                  setSlipCustom(raw);
                  const parsed = Number(raw);
                  if (raw !== '' && Number.isFinite(parsed) && parsed >= 0 && parsed <= 5) {
                    setSlipTouched(true);
                    setSlip(parsed);
                  }
                }}
                placeholder={`${slip}%`}
                aria-label={t('slippageCustomAria')}
                min="0"
                max="5"
                step="0.1"
                style={{
                  width: 64,
                  padding: '6px 8px',
                  borderRadius: 8,
                  border: 0,
                  background: slipCustom !== '' ? 'var(--y)' : 'transparent',
                  fontFamily: 'var(--mf)',
                  fontWeight: 700,
                  fontSize: 11,
                  color: 'var(--ink)',
                }}
              />
            </div>
            {slipCustom !== '' &&
              !(Number.isFinite(Number(slipCustom)) && Number(slipCustom) >= 0 && Number(slipCustom) <= 5) && (
                <span className="mono tone-warn" style={{ fontSize: 11 }} data-testid="swap-slippage-range">
                  {t('slippageRangeHint', { slip })}
                </span>
              )}
            {slip >= 2 && (
              <span className="mono tone-warn" style={{ fontSize: 11 }} data-testid="swap-slippage-high">
                {t('slippageHighHint')}
              </span>
            )}
          </div>

          {/* receipt */}
          <div className="block tight mt-14" style={{ background: 'var(--bg-2)', padding: 14, border: '2px dashed var(--ink)', boxShadow: 'none' }}>
            <div className="meta-row">
              <span>{t('rate')}</span>
              {hasLiveAmounts && quote && Number(quote.amountOut) > 0 && Number(amtIn) > 0 ? (
                <button
                  type="button"
                  onClick={() => setRateInverted((v) => !v)}
                  title={t('rateInvert')}
                  aria-label={t('rateInvert')}
                  data-testid="swap-rate-toggle"
                  style={{
                    background: 'transparent',
                    border: 0,
                    padding: 0,
                    cursor: 'pointer',
                    font: 'inherit',
                    fontWeight: 700,
                    color: 'inherit',
                  }}
                >
                  {rateInverted
                    ? `1 ${outTok!.symbol} ≈ ${fmt(Number(amtIn) / Number(quote.amountOut), 6)} ${inTok!.symbol}`
                    : `1 ${inTok!.symbol} ≈ ${fmt(Number(quote.amountOut) / Number(amtIn), 6)} ${outTok!.symbol}`}
                  {' ⇄'}
                </button>
              ) : (
                <b>—</b>
              )}
            </div>
            <div className="meta-row" style={{ marginTop: 6 }}>
              <span>{t('minReceived')}</span>
              <b>{hasLiveAmounts && quote ? `${fmt(Number(quote.minimumReceived), 4)} ${outTok!.symbol}` : '—'}</b>
            </div>
            <div className="meta-row" style={{ marginTop: 6 }}>
              <span>{t('priceImpact')}</span>
              <b className={quote && Math.abs(quote.priceImpactPct) > 1 ? 'tone-warn' : ''}>
                {hasLiveAmounts && quote ? fmtPct(quote.priceImpactPct) : '—'}
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
                  ? quote.path
                      .map((a) => symbolByAddress.get(a.toLowerCase()) ?? `${a.slice(0, 6)}…${a.slice(-4)}`)
                      .join(' → ')
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

          {/* High price impact needs an explicit acknowledgement: the number is
              real (router quote), the user just has to own it before the CTA
              arms. Resets whenever the pair or amount changes. */}
          {quoteExecutable && highImpact && (
            <div
              className="block tight mt-14"
              style={{ background: 'var(--o)', color: '#fff', padding: 14, border: '2px solid var(--ink)', boxShadow: 'none' }}
              data-testid="swap-impact-panel"
            >
              <div style={{ fontFamily: 'var(--df)', fontWeight: 800, fontSize: 13 }}>
                {t('impactPanel.title')}
              </div>
              <p style={{ marginTop: 4, fontSize: 12.5, lineHeight: 1.5 }}>
                {t('impactPanel.body', { impact: fmt(Math.abs(quote!.priceImpactPct), 2) })}
              </p>
              <label style={{ display: 'flex', alignItems: 'center', gap: 8, marginTop: 8, fontSize: 12.5, cursor: 'pointer' }}>
                <input
                  type="checkbox"
                  checked={impactAck}
                  onChange={(e) => setImpactAck(e.target.checked)}
                  data-testid="swap-impact-ack"
                />
                {t('impactPanel.ack')}
              </label>
            </div>
          )}

          {/* Advisory pre-sign review, above the CTA it describes. The CTA's
              gate stays routerAddress + quoteExecutable + allowance; this only
              says what is about to happen. */}
          <TxPreflight input={preflightInput} className="mt-14" />

          {/* CTA chain — provenance-gated. New states (connect, insufficient
              balance, unacknowledged impact) slot in without weakening the
              executable-quote gate. */}
          {!routerAddress ? (
            <button className="btn btn-y mt-14" style={{ width: '100%' }} disabled>
              {t('cta.routerMissing', { chainId })}
            </button>
          ) : !isConnected || app.walletState !== 'connected' ? (
            <button
              className="btn btn-y mt-14"
              style={{ width: '100%' }}
              onClick={app.openConnect}
              data-testid="swap-cta-connect"
            >
              {t('cta.connect')}
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
          ) : intent.busy ? (
            <button className="btn btn-c mt-14" style={{ width: '100%' }} disabled data-testid="swap-cta-busy">
              <span className="spinner" />{' '}
              {intent.phase === 'wallet'
                ? t('cta.confirmWallet')
                : intent.phase === 'review'
                  ? t('intent.reviewCta')
                  : activeStep?.kind === 'approve'
                    ? t('cta.approvingBusy', { symbol: inTok!.symbol })
                    : t('cta.swapping')}
            </button>
          ) : insufficientBalance ? (
            <button className="btn btn-y mt-14" style={{ width: '100%' }} disabled data-testid="swap-cta-insufficient">
              {t('cta.insufficient', { symbol: inTok!.symbol })}
            </button>
          ) : needsApproval ? (
            <button className="btn btn-o mt-14" style={{ width: '100%' }} onClick={handlePrimary} data-testid="swap-cta-approve">
              {t('cta.approve', { symbol: inTok!.symbol })}
            </button>
          ) : quote && highImpact && !impactAck ? (
            <button className="btn btn-o mt-14" style={{ width: '100%' }} disabled data-testid="swap-cta-impact-blocked">
              {t('cta.swapSubmit', {
                amountIn: amtIn,
                tokenIn: inTok!.symbol,
                amountOut: fmt(Number(quote.amountOut), 4),
                tokenOut: outTok!.symbol,
              })}
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
