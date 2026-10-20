'use client';

import { useLayoutEffect, useMemo, useRef, useState } from 'react';
import {
  explainSecurityTransaction,
  reviewSecurityTransaction,
  type AtlasTxReviewExplanation,
  type AtlasTxReviewInput,
  type AtlasTxReviewResult,
} from '@/lib/api/atlas';

type Action = 'review' | 'explain';
type Snapshot = {
  scope: object;
  pending: Action | null;
  review: AtlasTxReviewResult | null;
  explanation: AtlasTxReviewExplanation | null;
};

export function useTxReviewActions(
  input: AtlasTxReviewInput | null,
  locale: string,
  onError?: (error: unknown) => void,
  sessionGeneration = 0,
) {
  const key = JSON.stringify([input, locale, sessionGeneration]);
  // A new identity on A -> B -> A prevents an old A response from becoming
  // current again merely because the form has returned to the same values.
  const scope = useMemo(() => ({ key }), [key]);
  const active = useRef<{ scope: object; request: number } | null>(null);
  const sequence = useRef(0);
  const pending = useRef<AbortController | null>(null);
  const [snapshot, setSnapshot] = useState<Snapshot | null>(null);

  useLayoutEffect(() => {
    active.current = { scope, request: 0 };
    return () => { active.current = null; pending.current?.abort(); };
  }, [scope]);

  const run = async (action: Action) => {
    if (!input) return;
    pending.current?.abort();
    const controller = new AbortController();
    pending.current = controller;
    const request = ++sequence.current;
    active.current = { scope, request };
    // Optional prose must not remove an already usable deterministic review
    // while the model is pending or when its request fails.
    setSnapshot((previous) => ({
      scope, pending: action, explanation: null,
      review: action === 'explain' && previous?.scope === scope ? previous.review : null,
    }));
    const isCurrent = () => active.current?.scope === scope && active.current.request === request;

    try {
      const result = action === 'explain'
        ? await explainSecurityTransaction({ ...input, locale }, controller.signal)
        : { review: await reviewSecurityTransaction(input, controller.signal), explanation: null, source: 'static' as const };
      // Review and explanation share one sequence: a slower explanation must
      // not replace a newer deterministic review, even for the same input.
      if (!isCurrent()) return;
      setSnapshot({
        scope,
        pending: null,
        review: result.review,
        explanation: result.source === 'ai' ? result.explanation : null,
      });
    } catch (error) {
      if (!isCurrent()) return;
      setSnapshot((previous) => ({
        scope, pending: null, explanation: null,
        review: action === 'explain' && previous?.scope === scope ? previous.review : null,
      }));
      onError?.(error);
    }
  };

  // Filtering at render time also hides previous results before effects run.
  const current = snapshot?.scope === scope ? snapshot : null;
  return {
    review: current?.review ?? null,
    explanation: current?.explanation ?? null,
    pending: current?.pending ?? null,
    reviewTransaction: () => run('review'),
    explainTransaction: () => run('explain'),
  };
}
