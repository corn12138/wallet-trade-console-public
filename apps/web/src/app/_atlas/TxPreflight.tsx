'use client';
import { useEffect, useState } from 'react';
import { useMutation, useQuery } from '@tanstack/react-query';
import { useLocale, useTranslations } from 'next-intl';
import {
  explainSecurityTransaction,
  reviewSecurityTransaction,
  type AtlasTxReviewExplanation,
  type AtlasTxReviewInput,
  type AtlasTxReviewResult,
} from '@/lib/api/atlas';
import { useProductStatus } from './ProductStatus';

/**
 * A pre-sign review strip: the deterministic tx-review, rendered where the user
 * is about to sign, with an optional plain-language summary.
 *
 * Deliberately ADVISORY. It never disables a CTA. What can and cannot execute
 * is decided by chain state — the bridge's `executable`, an allowance, a
 * revert — and a second, softer gate here would only teach users that a warning
 * is something to click past. The review's job is to say what is about to
 * happen, in time to change your mind.
 *
 * Renders nothing until `input` is non-null: a partly-filled form has no
 * transaction to review, and a placeholder verdict on an incomplete form would
 * be a claim about something that doesn't exist yet.
 */
export function TxPreflight({
  input,
  className,
}: {
  input: AtlasTxReviewInput | null;
  className?: string;
}) {
  const t = useTranslations('security.txReview');
  const locale = useLocale();
  const [explanation, setExplanation] = useState<AtlasTxReviewExplanation | null>(null);
  const [explainedReview, setExplainedReview] = useState<AtlasTxReviewResult | null>(null);

  const productStatus = useProductStatus();
  const explainAvailable = productStatus.data?.aiExplain?.status === 'healthy';

  // The serialized input is the cache key AND the staleness signal: any edit to
  // the form produces a different key, so a verdict can never outlive the
  // transaction it described.
  const key = input ? JSON.stringify(input) : null;

  const review = useQuery<AtlasTxReviewResult>({
    queryKey: ['tx-preflight', key],
    queryFn: () => reviewSecurityTransaction(input as AtlasTxReviewInput),
    enabled: Boolean(input),
    staleTime: 15_000,
    retry: false,
  });

  const explain = useMutation({
    mutationFn: () => explainSecurityTransaction({ ...(input as AtlasTxReviewInput), locale }),
    onSuccess: (r) => {
      setExplanation(r.explanation);
      setExplainedReview(r.review);
    },
  });

  useEffect(() => {
    // Drop prose about the previous transaction the moment the input changes.
    setExplanation(null);
    setExplainedReview(null);
  }, [key]);

  if (!input) return null;

  // A review that could not be fetched is silent rather than alarming: the
  // deterministic gates each flow already has are unaffected, and an error
  // banner here would read as a problem with the user's transaction.
  if (review.isError) return null;

  if (review.isPending) {
    return (
      <div className={className} style={{ fontSize: 12, color: 'var(--ink-2)' }}>
        <span className="spinner" /> {t('simulating')}
      </div>
    );
  }

  const result = explainedReview ?? review.data;
  if (!result) return null;

  // Passing checks are omitted: this strip exists to surface what deserves a
  // second look. The full set stays available in the Security console.
  const notable = result.checks.filter((c) => c.status !== 'pass');

  return (
    <div
      className={className}
      data-testid="tx-preflight"
      style={{ border: '2px solid var(--ink)', borderRadius: 10, padding: 10, background: 'var(--paper-2)' }}
    >
      <div className="row" style={{ justifyContent: 'space-between', alignItems: 'center' }}>
        <span className="eyebrow">{t('preflightTitle')}</span>
        <b
          data-testid="tx-preflight-status"
          style={{
            fontSize: 12,
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

      {notable.length > 0 && (
        <ul style={{ marginTop: 8, paddingLeft: 18, fontSize: 11, lineHeight: 1.5 }}>
          {notable.map((c) => (
            <li key={c.id} style={{ marginTop: 3, color: c.status === 'fail' ? 'var(--neg)' : 'var(--ink)' }}>
              <b>{c.title}</b> — {c.summary}
            </li>
          ))}
        </ul>
      )}

      {explanation && (
        <div style={{ marginTop: 10, borderTop: '2px solid var(--ink)', paddingTop: 8, fontSize: 11, lineHeight: 1.5 }}>
          <div style={{ fontFamily: 'var(--df)', fontWeight: 700 }}>{explanation.headline}</div>
          {explanation.whatHappens.length > 0 && (
            <ul style={{ marginTop: 6, paddingLeft: 18 }}>
              {explanation.whatHappens.map((step, i) => (
                <li key={i} style={{ marginTop: 2 }}>{step}</li>
              ))}
            </ul>
          )}
          {explanation.watchOut.length > 0 && (
            <ul style={{ marginTop: 6, paddingLeft: 18, color: 'var(--warn)' }}>
              {explanation.watchOut.map((item, i) => (
                <li key={i} style={{ marginTop: 2 }}>{item}</li>
              ))}
            </ul>
          )}
          <div style={{ marginTop: 6, color: 'var(--ink-2)' }}>{t('explanationDisclaimer')}</div>
        </div>
      )}

      {/*
        Offered only when the backend reports the layer live, and hidden once an
        explanation is in hand. A static answer leaves the strip exactly as it
        was — the checks above are already the complete review.
      */}
      {explainAvailable && !explanation && (
        <button
          className="btn mt-6"
          style={{ width: '100%', fontSize: 11 }}
          onClick={() => explain.mutate()}
          disabled={explain.isPending}
          data-testid="tx-preflight-explain"
        >
          {explain.isPending ? (
            <>
              <span className="spinner" /> {t('explaining')}
            </>
          ) : (
            t('explain')
          )}
        </button>
      )}
    </div>
  );
}
