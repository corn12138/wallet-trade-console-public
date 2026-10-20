import { StrictMode, type ReactNode } from 'react';
import { act, renderHook, waitFor } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { useCampaignCommand } from './use-campaign-command';
import { apiFixture, campaignId, deferred, joinIntent, response } from '../model/command-test-fixtures';
import { CampaignAPIError, type CampaignSession } from '../model/ports';
import type { CommandResponse } from '../model/contract.generated';

const session = (digit: string, generation = 1): CampaignSession => ({ owner: `0x${digit.repeat(40)}`, generation, token: `credential-${digit}-${generation}` });
describe('campaign command hook scope and remount', () => {
  it('retains an aborted navigation command and reconciles before the same-key retry', async () => {
    const api = apiFixture(); const pending = deferred<CommandResponse>(); api.execute.mockReturnValueOnce(pending.promise);
    const onOldCurrent = vi.fn(); const owner = session('c');
    const first = renderHook(() => useCampaignCommand({ api, session: owner, campaignId, onCurrent: onOldCurrent }));
    let executing!: Promise<void>;
    act(() => { executing = first.result.current.confirm(joinIntent); });
    expect(api.execute).toHaveBeenCalledTimes(1); const sent = api.execute.mock.calls[0]; first.unmount(); expect(sent[3]?.aborted).toBe(true);
    const next = renderHook(() => useCampaignCommand({ api, session: session('c', 2), campaignId }));
    await waitFor(() => expect(next.result.current.state).toMatchObject({ status: 'unknown', canRetry: true }));
    await act(async () => { pending.resolve(response(sent[2])); await executing; });
    expect(next.result.current.state.status).toBe('unknown'); expect(onOldCurrent).not.toHaveBeenCalled();
    await act(async () => { await next.result.current.retry(); });
    expect(api.execute).toHaveBeenCalledTimes(2); expect(api.execute.mock.calls[1][2]).toBe(sent[2]);
    expect(next.result.current.state.status).toBe('settled'); next.unmount();
  });
  it('clears A immediately on switch and does not let A late 401 invalidate B', async () => {
    const api = apiFixture(); const pending = deferred<CommandResponse>(); api.execute.mockReturnValueOnce(pending.promise); const onUnauthorized = vi.fn();
    const view = renderHook(({ current }: { current: CampaignSession | null }) => useCampaignCommand({ api, session: current, campaignId, onUnauthorized }), { initialProps: { current: session('d') as CampaignSession | null } });
    let executing!: Promise<void>; act(() => { executing = view.result.current.confirm(joinIntent); });
    view.rerender({ current: session('e', 2) }); expect(view.result.current.state).toMatchObject({ status: 'idle', command: null, response: null, me: null });
    await act(async () => { pending.reject(new CampaignAPIError('UNAUTHORIZED', 401)); await executing; });
    expect(onUnauthorized).not.toHaveBeenCalled(); expect(view.result.current.state.command).toBeNull();
    view.rerender({ current: null }); expect(view.result.current.state).toMatchObject({ status: 'idle', command: null }); view.unmount();
  });
  it('keeps one campaign lock across StrictMode resubscriptions and two views', async () => {
    const api = apiFixture(); const pending = deferred<CommandResponse>(); api.execute.mockReturnValueOnce(pending.promise); const current = session('f');
    const wrapper = ({ children }: { children: ReactNode }) => <StrictMode>{children}</StrictMode>;
    const first = renderHook(() => useCampaignCommand({ api, session: current, campaignId }), { wrapper });
    const second = renderHook(() => useCampaignCommand({ api, session: current, campaignId }), { wrapper });
    let executing!: Promise<void>; act(() => { executing = first.result.current.confirm(joinIntent); });
    await act(async () => { await second.result.current.confirm({ operation: 'remove-reminder', expectedStateVersion: '0' }); });
    expect(api.execute).toHaveBeenCalledTimes(1); const sent = api.execute.mock.calls[0][2];
    await act(async () => { pending.resolve(response(sent)); await executing; });
    expect(first.result.current.state.status).toBe('settled'); first.unmount(); second.unmount();
  });
});
