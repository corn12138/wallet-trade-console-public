import { describe, expect, it } from 'vitest';
import { CampaignAPIError } from './ports';
import { COMMAND_STORAGE_KEY } from './command-storage';
import { CampaignCommandController } from './command-controller';
import { CampaignCommandStore } from './command-store';
import { campaignId, deferred, detail, fixture, joinIntent, me, response, sessionA, type MemoryCommandStorage } from './command-test-fixtures';
import type { CommandResponse } from './contract.generated';

function records(storage: MemoryCommandStorage) { return JSON.parse(storage.getItem(COMMAND_STORAGE_KEY) ?? '{"records":[]}').records; }
describe('campaign command uncertainty and serialized intent', () => {
  it('freezes versions before dispatch and serializes reminder/cancel with participation', async () => {
    const f = fixture(); const pending = deferred<CommandResponse>(); f.api.execute.mockReturnValueOnce(pending.promise);
    const intent = { ...joinIntent }; const work = f.controller.confirm(intent); intent.expectedStateVersion = '9';
    await f.controller.confirm({ operation: 'remove-reminder', expectedStateVersion: '0' });
    expect(f.api.execute).toHaveBeenCalledTimes(1); const command = f.api.execute.mock.calls[0][2];
    expect(command.input.expectedStateVersion).toBe('0'); expect(Object.isFrozen(command.input)).toBe(true);
    expect(records(f.storage)[0].command).toEqual(command); expect(f.controller.getSnapshot().status).toBe('submitting');
    pending.resolve(response(command)); await work;
    expect(f.controller.getSnapshot().status).toBe('settled'); expect(records(f.storage)).toEqual([]);
  });
  it('keeps a missing lookup unknown and retries only its frozen key after both reads', async () => {
    const f = fixture(); f.api.execute.mockRejectedValueOnce(new TypeError('offline'));
    await f.controller.confirm(joinIntent); const original = f.api.execute.mock.calls[0][2];
    expect(f.controller.getSnapshot().status).toBe('unknown'); await f.controller.retry();
    await f.controller.confirm({ ...joinIntent, expectedPublicationVersion: '2' }); expect(f.api.execute).toHaveBeenCalledTimes(1);
    await f.controller.reconcile(); expect(f.api.command).toHaveBeenCalledWith(sessionA, campaignId, 'join', original.input.clientCommandId, expect.any(AbortSignal));
    expect(f.api.me).toHaveBeenCalledTimes(1); expect(f.controller.getSnapshot()).toMatchObject({ status: 'unknown', canRetry: true });
    await f.controller.retry(); expect(f.api.execute).toHaveBeenCalledTimes(2); expect(f.api.execute.mock.calls[1][2]).toBe(original);
  });
  it('shows current left after finding a late original join and never reexecutes it', async () => {
    const f = fixture(); f.api.execute.mockRejectedValueOnce(new CampaignAPIError('TIMEOUT', 0)); await f.controller.confirm(joinIntent);
    const command = f.api.execute.mock.calls[0][2]; f.api.command.mockResolvedValueOnce({ ...response(command, 'left', '2'), replayed: true }); f.api.me.mockResolvedValueOnce(me('2', 'left'));
    await f.controller.reconcile(); const state = f.controller.getSnapshot();
    expect(state.response?.original.state?.status).toBe('joined'); expect(state.response?.current.state?.status).toBe('left');
    expect(state.status).toBe('settled'); expect(f.api.execute).toHaveBeenCalledTimes(1); expect(records(f.storage)).toEqual([]);
  });
  it.each([409, 422, 410])('requires fresh review and explicit confirmation after status %s', async (status) => {
    const f = fixture(); f.api.execute.mockRejectedValueOnce(new CampaignAPIError('CAMPAIGN_VERSION_CONFLICT', status));
    f.api.detail.mockResolvedValue(detail('2')); f.api.me.mockResolvedValue(me('0', 'joined', '2'));
    await f.controller.confirm(joinIntent); const first = f.api.execute.mock.calls[0][2];
    expect(f.controller.getSnapshot()).toMatchObject({ status: 'requiresReview', reviewReady: true }); expect(f.api.execute).toHaveBeenCalledTimes(1);
    await f.controller.retry(); await f.controller.confirm(joinIntent); expect(f.api.execute).toHaveBeenCalledTimes(1);
    await f.controller.confirm({ ...joinIntent, expectedPublicationVersion: '2' }); expect(f.api.execute).toHaveBeenCalledTimes(2);
    expect(f.api.execute.mock.calls[1][2].input.clientCommandId).not.toBe(first.input.clientCommandId);
  });
  it('keeps a 401 uncertain, clears private results, and only invalidates its current session', async () => {
    const f = fixture(); f.api.execute.mockRejectedValueOnce(new CampaignAPIError('UNAUTHORIZED', 401)); await f.controller.confirm(joinIntent);
    expect(f.controller.getSnapshot()).toMatchObject({ status: 'unknown', response: null, me: null, canRetry: false });
    expect(f.onUnauthorized).toHaveBeenCalledTimes(1); expect(records(f.storage)).toHaveLength(1);
  });
  it('treats an invalid success envelope as unknown and cannot infer a result from Me alone', async () => {
    const f = fixture(); f.api.execute.mockResolvedValueOnce({ schemaVersion: 1 } as CommandResponse); f.api.me.mockResolvedValue(me('1'));
    await f.controller.confirm(joinIntent); expect(f.controller.getSnapshot()).toMatchObject({ status: 'unknown', error: { code: 'CAMPAIGN_RESPONSE_INVALID' } });
    await f.controller.reconcile(); expect(f.controller.getSnapshot().status).toBe('unknown'); expect(f.api.execute).toHaveBeenCalledTimes(1);
  });
  it('cancels archived membership without putting a publication version into its payload', async () => {
    const f = fixture(); f.api.execute.mockImplementationOnce(async (_session, _id, command) => response(command, 'left', '3'));
    await f.controller.confirm({ operation: 'leave', expectedStateVersion: '2' });
    expect(f.api.execute.mock.calls[0][2].input).not.toHaveProperty('expectedPublicationVersion'); expect(f.controller.getSnapshot().status).toBe('settled');
  });
  it('reloads only bounded nonsecret payload and reads before retry with a fresh session', async () => {
    const f = fixture(); f.api.execute.mockRejectedValueOnce(new Error('offline')); await f.controller.confirm(joinIntent); const first = f.api.execute.mock.calls[0][2];
    expect(f.storage.getItem(COMMAND_STORAGE_KEY)).not.toContain(sessionA.token);
    const restored = new CampaignCommandController({ api: f.api, session: { ...sessionA, generation: 3, token: 'new-auth' }, campaignId, isCurrent: () => true }, new CampaignCommandStore(() => f.storage));
    const unsubscribe = restored.subscribe(() => undefined);
    expect(restored.getSnapshot()).toMatchObject({ status: 'unknown', canRetry: false });
    await restored.confirm(joinIntent); expect(f.api.execute).toHaveBeenCalledTimes(1);
    await restored.reconcile(); await restored.retry(); expect(f.api.execute.mock.calls[1][2]).toEqual(first); unsubscribe();
  });
});
