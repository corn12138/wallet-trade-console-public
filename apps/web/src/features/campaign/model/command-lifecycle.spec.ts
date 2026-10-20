import { describe, expect, it, vi } from 'vitest';
import { CampaignCommandController } from './command-controller';
import { CampaignAPIError } from './ports';
import { CampaignCommandStore } from './command-store';
import { COMMAND_STORAGE_KEY } from './command-storage';
import { apiFixture, campaignId, deferred, fixture, joinIntent, MemoryCommandStorage, response, sessionA, sessionB } from './command-test-fixtures';
import type { CommandResponse } from './contract.generated';

describe('campaign command session lifetime', () => {
  it('aborts on disposal but keeps the key and rejects the late transport success', async () => {
    const f = fixture(); const pending = deferred<CommandResponse>(); f.api.execute.mockReturnValueOnce(pending.promise);
    const executing = f.controller.confirm(joinIntent); const sent = f.api.execute.mock.calls[0]; f.controller.dispose();
    expect(sent[3]?.aborted).toBe(true); expect(f.controller.getSnapshot().status).toBe('unknown');
    pending.resolve(response(sent[2])); await executing;
    expect(f.onCurrent).not.toHaveBeenCalled(); expect(f.controller.getSnapshot().response).toBeNull();
    expect(JSON.parse(f.storage.getItem(COMMAND_STORAGE_KEY)!).records).toHaveLength(1);
    const remount = new CampaignCommandController({ api: f.api, session: sessionA, campaignId, isCurrent: () => true }, f.store);
    expect(remount.getSnapshot().status).toBe('unknown'); await remount.confirm(joinIntent); expect(f.api.execute).toHaveBeenCalledTimes(1);
  });
  it('does not deliver A success or A unauthorized cleanup after the visible identity changes', async () => {
    for (const denied of [false, true]) {
      const f = fixture(); const pending = deferred<CommandResponse>(); f.api.execute.mockReturnValueOnce(pending.promise);
      const executing = f.controller.confirm(joinIntent); const command = f.api.execute.mock.calls[0][2]; f.setActive(false);
      const nextCurrent = vi.fn(); const b = new CampaignCommandController({ api: f.api, session: sessionB, campaignId, isCurrent: () => true, onCurrent: nextCurrent }, f.store);
      if (denied) pending.reject(new CampaignAPIError('UNAUTHORIZED', 401)); else pending.resolve(response(command));
      await executing;
      expect(f.onCurrent).not.toHaveBeenCalled(); expect(f.onUnauthorized).not.toHaveBeenCalled(); expect(nextCurrent).not.toHaveBeenCalled();
      expect(b.getSnapshot()).toMatchObject({ status: 'idle', response: null, command: null });
      expect(f.controller.getSnapshot().status).toBe('unknown');
    }
  });
  it('serializes the same owner campaign across two mounted controllers', async () => {
    const f = fixture(); const pending = deferred<CommandResponse>(); f.api.execute.mockReturnValueOnce(pending.promise);
    const other = new CampaignCommandController({ api: f.api, session: sessionA, campaignId, isCurrent: () => true }, f.store);
    const executing = f.controller.confirm(joinIntent); await other.confirm({ operation: 'leave', expectedStateVersion: '0' });
    expect(f.api.execute).toHaveBeenCalledTimes(1); pending.resolve(response(f.api.execute.mock.calls[0][2])); await executing;
  });
  it('keeps callback exceptions outside the command outcome', async () => {
    const api = apiFixture(); const store = new CampaignCommandStore(() => new MemoryCommandStorage());
    const controller = new CampaignCommandController({ api, session: sessionA, campaignId, isCurrent: () => true, onCurrent: () => { throw new Error('unmounted display'); } }, store);
    await controller.confirm(joinIntent); expect(controller.getSnapshot().status).toBe('settled'); expect(controller.getSnapshot().error).toBeNull();
  });
  it('does not turn an unobservable ledger or unknown personal state into permission to retry', async () => {
    const f = fixture(); f.api.execute.mockRejectedValueOnce(new Error('offline')); await f.controller.confirm(joinIntent);
    f.api.command.mockRejectedValueOnce(new CampaignAPIError('CAMPAIGN_DATA_UNAVAILABLE', 503)); await f.controller.reconcile(); await f.controller.retry();
    expect(f.controller.getSnapshot().canRetry).toBe(false); expect(f.api.execute).toHaveBeenCalledTimes(1);
  });
});
