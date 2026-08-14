'use client';
import { useState } from 'react';
import { useMutation } from '@tanstack/react-query';
import { useTranslations, useLocale } from 'next-intl';
import {
  explainSecurityTransaction,
  reviewSecurityTransaction,
  type AtlasTxReviewExplanation,
  type AtlasTxReviewInput,
  type AtlasTxReviewResult,
} from '@/lib/api/atlas';
import { useProductStatus } from '../ProductStatus';
import { useApp } from '../AppContext';

type ReviewMode = 'custom' | 'bridge-deposit';

export function TxReviewPanel({ fromAddress, chainId }: { fromAddress: string; chainId: number }) {
  const app = useApp();
  const t = useTranslations('security.txReview');
  const locale = useLocale();
  const [mode, setMode] = useState<ReviewMode>('custom');
  const [to, setTo] = useState('');
  const [data, setData] = useState('0x');
  const [value, setValue] = useState('0');
  const [bridgeToken, setBridgeToken] = useState('');
  const [bridgeAmount, setBridgeAmount] = useState('');
  const [bridgeDstChain, setBridgeDstChain] = useState('');
  const [bridgeRecipient, setBridgeRecipient] = useState('');
  const [result, setResult] = useState<AtlasTxReviewResult | null>(null);
  const [explanation, setExplanation] = useState<AtlasTxReviewExplanation | null>(null);

  // The explanation layer ships dark. Asking the backend whether it is on —
  // rather than offering a button that would only ever answer "static" — keeps
  // this from becoming an affordance that does nothing.
  const productStatus = useProductStatus();
  const explainAvailable = productStatus.data?.aiExplain?.status === 'healthy';

  const buildInput = (): AtlasTxReviewInput =>
    mode === 'bridge-deposit'
      ? {
          operationType: 'bridge-deposit',
          fromAddress,
          chainId,
          tokenAddress: bridgeToken,
          // Base units on purpose: the gateway's route minimum and the
          // destination liquidity are both read in base units, so scaling
          // here would only add a rounding seam between UI and chain.
          amount: bridgeAmount,
          bridgeDstChainId: Number(bridgeDstChain),
          recipient: bridgeRecipient || undefined,
        }
      : {
          operationType: 'custom',
          fromAddress,
          chainId,
          tx: { to, value, data },
        };

  const reviewMutation = useMutation({
    mutationFn: () => reviewSecurityTransaction(buildInput()),
    onSuccess: (r) => {
      setResult(r);
      setExplanation(null);
    },
    onError: (e: any) => app.toast(e?.message || t('reviewFailed'), 'err'),
  });

  // Explaining re-sends the input, not the review: the server derives its own
  // verdict, so the prose can never describe a review the server didn't make.
  // The returned review replaces the displayed one for the same reason.
  const explainMutation = useMutation({
    mutationFn: () => explainSecurityTransaction({ ...buildInput(), locale }),
    onSuccess: (r) => {
      setResult(r.review);
      setExplanation(r.explanation);
    },
    onError: (e: any) => app.toast(e?.message || t('reviewFailed'), 'err'),
  });

  const canSubmit =
    mode === 'bridge-deposit'
      ? Boolean(bridgeToken && bridgeAmount && Number(bridgeDstChain) > 0)
      : Boolean(to);

  const switchMode = (next: ReviewMode) => {
    if (next === mode) return;
    setMode(next);
    // A result belongs to the operation that produced it; carrying it across
    // modes would show bridge checks next to a raw-calldata form.
    setResult(null);
    setExplanation(null);
  };

  return (
    <section className="page-split">
      <div className="block">
        <h3 className="h-display" style={{ fontSize: 20, marginBottom: 14 }}>{t('title')}</h3>
        <div className="row gap-6">
          {(['custom', 'bridge-deposit'] as const).map((m) => (
            <button
              key={m}
              type="button"
              className={mode === m ? 'btn btn-y' : 'btn'}
              style={{ flex: 1 }}
              onClick={() => switchMode(m)}
            >
              {m === 'custom' ? t('modeCustom') : t('modeBridge')}
            </button>
          ))}
        </div>
        {mode === 'bridge-deposit' ? (
          <>
            <div className="field mt-14">
              <div className="l"><span>{t('bridgeTokenLabel')}</span></div>
              <input
                value={bridgeToken}
                onChange={(e) => setBridgeToken(e.target.value)}
                style={{ fontFamily: 'var(--mf)', fontSize: 14 }}
                placeholder="0x…"
              />
            </div>
            <div className="field mt-14">
              <div className="l"><span>{t('bridgeAmountLabel')}</span></div>
              <input
                value={bridgeAmount}
                onChange={(e) => setBridgeAmount(e.target.value)}
                style={{ fontFamily: 'var(--mf)', fontSize: 14 }}
                inputMode="numeric"
              />
            </div>
            <div className="field mt-14">
              <div className="l"><span>{t('bridgeDstChainLabel')}</span></div>
              <input
                value={bridgeDstChain}
                onChange={(e) => setBridgeDstChain(e.target.value)}
                style={{ fontFamily: 'var(--mf)', fontSize: 14 }}
                inputMode="numeric"
              />
            </div>
            <div className="field mt-14">
              <div className="l"><span>{t('bridgeRecipientLabel')}</span></div>
              <input
                value={bridgeRecipient}
                onChange={(e) => setBridgeRecipient(e.target.value)}
                style={{ fontFamily: 'var(--mf)', fontSize: 14 }}
                placeholder={fromAddress}
              />
            </div>
          </>
        ) : (
          <>
            <div className="field mt-14">
              <div className="l"><span>{t('toLabel')}</span></div>
              <input
                value={to}
                onChange={(e) => setTo(e.target.value)}
                style={{ fontFamily: 'var(--mf)', fontSize: 14 }}
                placeholder="0x…"
              />
            </div>
            <div className="field mt-14">
              <div className="l"><span>{t('dataLabel')}</span></div>
              <input
                value={data}
                onChange={(e) => setData(e.target.value)}
                style={{ fontFamily: 'var(--mf)', fontSize: 13 }}
                placeholder="0x…"
              />
            </div>
            <div className="field mt-14">
              <div className="l"><span>{t('valueLabel')}</span></div>
              <input value={value} onChange={(e) => setValue(e.target.value)} />
            </div>
          </>
        )}
        <button
          className="btn btn-y mt-14"
          style={{ width: '100%' }}
          onClick={() => reviewMutation.mutate()}
          disabled={reviewMutation.isPending || !canSubmit}
        >
          {reviewMutation.isPending ? (
            <>
              <span className="spinner" /> {t('simulating')}
            </>
          ) : (
            t('simulate')
          )}
        </button>
        {explainAvailable && (
          <button
            className="btn mt-6"
            style={{ width: '100%' }}
            onClick={() => explainMutation.mutate()}
            disabled={explainMutation.isPending || !canSubmit}
          >
            {explainMutation.isPending ? (
              <>
                <span className="spinner" /> {t('explaining')}
              </>
            ) : (
              t('explain')
            )}
          </button>
        )}
      </div>
      <div className="block bg-paper2">
        <div className="eyebrow">{t('decoded')}</div>
        {!result && (
          <div className="mt-14 mono" style={{ fontSize: 12, color: 'var(--ink-2)' }}>
            {mode === 'bridge-deposit' ? t('bridgePrompt') : t('prompt')}
          </div>
        )}
        {/*
          The explanation renders ABOVE the checks but is explicitly labelled as
          generated prose, because the checks below it are the actual verdict.
          There is deliberately no "AI unavailable" state: when the layer is off
          or fails, this block simply isn't here and the checks — which already
          carry human-readable text — stand alone.
        */}
        {result && explanation && (
          <div
            className="mt-14"
            style={{ borderBottom: '2px solid var(--ink)', paddingBottom: 12, fontSize: 12, lineHeight: 1.6 }}
          >
            <div className="eyebrow">{t('explanationTitle')}</div>
            <div style={{ fontFamily: 'var(--df)', fontWeight: 700, marginTop: 6 }}>{explanation.headline}</div>
            {explanation.whatHappens.length > 0 && (
              <ul style={{ marginTop: 8, paddingLeft: 18 }}>
                {explanation.whatHappens.map((step, i) => (
                  <li key={i} style={{ marginTop: 2 }}>{step}</li>
                ))}
              </ul>
            )}
            {explanation.watchOut.length > 0 && (
              <ul style={{ marginTop: 8, paddingLeft: 18, color: 'var(--warn)' }}>
                {explanation.watchOut.map((item, i) => (
                  <li key={i} style={{ marginTop: 2 }}>{item}</li>
                ))}
              </ul>
            )}
            <div className="mt-6" style={{ fontSize: 11, color: 'var(--ink-2)' }}>
              {t('explanationDisclaimer')}
            </div>
          </div>
        )}
        {result && (
          <div className="mt-14" style={{ fontFamily: 'var(--mf)', fontSize: 12, lineHeight: 1.7 }}>
            <div style={{ display: 'flex', justifyContent: 'space-between' }}>
              <span style={{ color: 'var(--ink-2)' }}>{t('status')}</span>
              <b
                style={{
                  color:
                    result.reviewStatus === 'blocked'
                      ? 'var(--neg)'
                      : result.reviewStatus === 'warning'
                        ? 'var(--warn)'
                        : 'var(--pos)',
                }}
              >
                {result.reviewStatus}
              </b>
            </div>
            <div style={{ display: 'flex', justifyContent: 'space-between' }}>
              <span style={{ color: 'var(--ink-2)' }}>{t('riskScore')}</span>
              <b>{result.riskScore}/100</b>
            </div>
            <div style={{ display: 'flex', justifyContent: 'space-between' }}>
              <span style={{ color: 'var(--ink-2)' }}>{t('gasEstimate')}</span>
              <b>{result.simulation.gasEstimate || '—'}</b>
            </div>
            <div style={{ display: 'flex', justifyContent: 'space-between' }}>
              <span style={{ color: 'var(--ink-2)' }}>{t('simulatedCall')}</span>
              <b>
                {result.simulation.callSucceeded === null
                  ? '—'
                  : result.simulation.callSucceeded
                    ? t('callOk')
                    : t('callRevert')}
              </b>
            </div>
            {result.simulation.errorMessage && (
              <div style={{ marginTop: 8, color: 'var(--neg)', fontSize: 11, lineHeight: 1.4 }}>
                {result.simulation.errorMessage}
              </div>
            )}
            {result.checks.length > 0 && (
              <div className="mt-14" style={{ borderTop: '2px solid var(--ink)', paddingTop: 10 }}>
                <div className="eyebrow">{t('checks')}</div>
                {result.checks.map((c) => (
                  <div key={c.id} className="row gap-6 mt-6" style={{ alignItems: 'flex-start' }}>
                    <span
                      style={{
                        width: 16,
                        height: 16,
                        borderRadius: 999,
                        background:
                          c.status === 'pass' ? 'var(--g)' : c.status === 'warn' ? 'var(--y)' : 'var(--o)',
                        border: '2px solid var(--ink)',
                        flex: 'none',
                        marginTop: 2,
                      }}
                    />
                    <div style={{ flex: 1 }}>
                      <div style={{ fontFamily: 'var(--df)', fontWeight: 700, fontSize: 12 }}>{c.title}</div>
                      <div style={{ fontSize: 11, color: 'var(--ink-2)', marginTop: 2 }}>{c.summary}</div>
                    </div>
                  </div>
                ))}
              </div>
            )}
          </div>
        )}
      </div>
    </section>
  );
}
