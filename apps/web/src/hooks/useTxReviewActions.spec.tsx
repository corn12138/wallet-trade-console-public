import { act, renderHook } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { AtlasTxReviewExplainResult, AtlasTxReviewInput, AtlasTxReviewResult } from '@/lib/api/atlas';
import { useTxReviewActions } from './useTxReviewActions';

const { explain, review } = vi.hoisted(() => ({ explain: vi.fn(), review: vi.fn() }));
vi.mock('@/lib/api/atlas', () => ({
  explainSecurityTransaction: explain,
  reviewSecurityTransaction: review,
}));

const input: AtlasTxReviewInput = {
  operationType: 'custom',
  fromAddress: `0x${'1'.repeat(40)}`,
  chainId: 11155111,
  tx: { to: `0x${'2'.repeat(40)}`, value: '0', data: '0x' },
};
const reviewed: AtlasTxReviewResult = {
  reviewStatus: 'warning', operationType: 'custom', chainId: 11155111, riskScore: 10,
  simulation: {
    mode: 'fallback', gasEstimate: null, gasLimitSuggestion: null,
    estimatedFeeWei: null, callSucceeded: null, errorMessage: null,
  },
  generatedTx: null, allowanceChange: null, connectedSite: null,
  recommendedActions: [], checks: [],
};
const explained: AtlasTxReviewExplainResult = {
  review: reviewed, source: 'ai',
  explanation: { headline: 'Old explanation', whatHappens: ['Old step'], watchOut: [], locale: 'en' },
};

function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}

function setup() {
  const onError = vi.fn();
  return {
    onError,
    ...renderHook(
      (props: { input: AtlasTxReviewInput | null; locale: string }) =>
        useTxReviewActions(props.input, props.locale, onError),
      { initialProps: { input: input as AtlasTxReviewInput | null, locale: 'en' } },
    ),
  };
}

describe('transaction review response ownership', () => {
  beforeEach(() => { explain.mockReset(); review.mockReset(); });

  it.each([
    ['amount', { ...input, amount: '2000' }, 'en'],
    ['owner', { ...input, fromAddress: `0x${'3'.repeat(40)}` }, 'en'],
    ['chain', { ...input, chainId: 84532 }, 'en'],
    ['locale', input, 'zh'],
    ['cleared form', null, 'en'],
  ] as const)('ignores late explanations after changing %s', async (_name, nextInput, locale) => {
    const pending = deferred<AtlasTxReviewExplainResult>();
    explain.mockReturnValue(pending.promise);
    const { result, rerender } = setup();
    let request!: Promise<void>;
    act(() => { request = result.current.explainTransaction(); });
    expect(result.current.pending).toBe('explain');
    rerender({ input: nextInput, locale });
    expect(result.current.pending).toBeNull();
    await act(async () => { pending.resolve(explained); await request; });
    expect(result.current.explanation).toBeNull();
    expect(result.current.review).toBeNull();
  });

  it('does not revive either completed or pending results on A -> B -> A', async () => {
    explain.mockResolvedValueOnce(explained);
    const { result, rerender } = setup();
    await act(async () => { await result.current.explainTransaction(); });
    expect(result.current.explanation?.headline).toBe('Old explanation');
    rerender({ input: { ...input, amount: '2000' }, locale: 'en' });
    rerender({ input, locale: 'en' });
    expect(result.current.review).toBeNull();

    const pending = deferred<AtlasTxReviewExplainResult>();
    explain.mockReturnValueOnce(pending.promise);
    let request!: Promise<void>;
    act(() => { request = result.current.explainTransaction(); });
    rerender({ input: { ...input, amount: '3000' }, locale: 'en' });
    rerender({ input, locale: 'en' });
    await act(async () => { pending.resolve(explained); await request; });
    expect(result.current.review).toBeNull();
  });

  it('keeps the newest review when an earlier explanation finishes last', async () => {
    const pending = deferred<AtlasTxReviewExplainResult>();
    explain.mockReturnValue(pending.promise);
    const newer = { ...reviewed, reviewStatus: 'blocked' as const };
    review.mockResolvedValue(newer);
    const { result } = setup();
    let request!: Promise<void>;
    act(() => { request = result.current.explainTransaction(); });
    await act(async () => { await result.current.reviewTransaction(); });
    await act(async () => { pending.resolve(explained); await request; });
    expect(result.current.review).toEqual(newer);
    expect(result.current.explanation).toBeNull();
  });

  it('keeps the newest explanation when an earlier review finishes last', async () => {
    const pending = deferred<AtlasTxReviewResult>();
    review.mockReturnValue(pending.promise);
    explain.mockResolvedValue(explained);
    const { result } = setup();
    let request!: Promise<void>;
    act(() => { request = result.current.reviewTransaction(); });
    await act(async () => { await result.current.explainTransaction(); });
    await act(async () => { pending.resolve({ ...reviewed, reviewStatus: 'approved' }); await request; });
    expect(result.current.review).toEqual(reviewed);
    expect(result.current.explanation).toEqual(explained.explanation);
  });

  it('ignores late errors after identity changes and after unmount', async () => {
    const pending = deferred<AtlasTxReviewExplainResult>();
    explain.mockReturnValue(pending.promise);
    const { result, rerender, unmount, onError } = setup();
    let request!: Promise<void>;
    act(() => { request = result.current.explainTransaction(); });
    rerender({ input, locale: 'zh' });
    await act(async () => { pending.reject(new Error('old identity')); await request; });
    expect(onError).not.toHaveBeenCalled();

    const afterUnmount = deferred<AtlasTxReviewResult>();
    review.mockReturnValue(afterUnmount.promise);
    act(() => { request = result.current.reviewTransaction(); });
    unmount();
    await act(async () => { afterUnmount.reject(new Error('unmounted')); await request; });
    expect(onError).not.toHaveBeenCalled();
  });

  it('reports an error only for the current request and clears pending', async () => {
    const error = new Error('current failure');
    review.mockRejectedValue(error);
    const { result, onError } = setup();
    await act(async () => { await result.current.reviewTransaction(); });
    expect(onError).toHaveBeenCalledExactlyOnceWith(error);
    expect(result.current.pending).toBeNull();
  });

  it('keeps deterministic checks visible while explanation is pending or fails', async () => {
    review.mockResolvedValue(reviewed);
    const pending = deferred<AtlasTxReviewExplainResult>();
    explain.mockReturnValue(pending.promise);
    const { result, onError } = setup();
    await act(async () => { await result.current.reviewTransaction(); });
    let request!: Promise<void>;
    act(() => { request = result.current.explainTransaction(); });
    expect(result.current.pending).toBe('explain');
    expect(result.current.review).toEqual(reviewed);
    await act(async () => { pending.reject(new Error('model unavailable')); await request; });
    expect(result.current.review).toEqual(reviewed);
    expect(result.current.pending).toBeNull();
    expect(onError).toHaveBeenCalledOnce();
  });

  it('renders static review without model prose and preserves request precision', async () => {
    explain.mockResolvedValue({ ...explained, source: 'static' });
    const { result, rerender } = setup();
    const precise = { ...input, amount: '90071992547409931234567890' };
    rerender({ input: precise, locale: 'zh' });
    await act(async () => { await result.current.explainTransaction(); });
    expect(explain).toHaveBeenCalledExactlyOnceWith({ ...precise, locale: 'zh' }, expect.any(AbortSignal));
    expect(result.current.review).toEqual(reviewed);
    expect(result.current.explanation).toBeNull();
  });
});


it('discards completed and pending reviews when the session changes for the same wallet', async () => {
  const onError = vi.fn();
  const hook = renderHook(({ generation }) => useTxReviewActions(input, 'en', onError, generation),
    { initialProps: { generation: 1 } });
  review.mockResolvedValueOnce(reviewed);
  await act(async () => { await hook.result.current.reviewTransaction(); });
  expect(hook.result.current.review).toEqual(reviewed);
  const late = deferred<AtlasTxReviewResult>();
  review.mockReturnValueOnce(late.promise);
  let request!: Promise<void>;
  act(() => { request = hook.result.current.reviewTransaction(); });
  const signal = review.mock.calls.at(-1)?.[1] as AbortSignal;
  hook.rerender({ generation: 2 });
  expect(signal.aborted).toBe(true);
  expect(hook.result.current.review).toBeNull();
  await act(async () => { late.reject(new Error('stale session')); await request; });
  expect(onError).not.toHaveBeenCalled();
  expect(hook.result.current.review).toBeNull();
});
