import { describe, expect, it } from 'vitest';
import { CampaignCommandController } from './command-controller';
import { freezeCommand, newCommandID } from './command-input';
import { CampaignCommandStore } from './command-store';
import { COMMAND_STORAGE_KEY, commandKey, readCommandRecords, writeCommandRecords } from './command-storage';
import type { CommandRecord } from './command-types';
import { apiFixture, campaignId, fixture, joinIntent, MemoryCommandStorage, sessionA } from './command-test-fixtures';

function record(id = campaignId): CommandRecord { return { version: 1, owner: sessionA.owner, campaignId: id, command: freezeCommand(joinIntent, newCommandID()), createdAt: Date.now(), disposition: 'pending' }; }
describe('bounded campaign command recovery storage', () => {
  it('uses cryptographic UUIDv7 with current timestamp and immutable input', () => {
    const before = Date.now(); const id = newCommandID(); const after = Date.now();
    expect(id).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
    const timestamp = Number.parseInt(id.replaceAll('-', '').slice(0, 12), 16); expect(timestamp).toBeGreaterThanOrEqual(before); expect(timestamp).toBeLessThanOrEqual(after);
    expect(newCommandID()).not.toBe(id);
  });
  it('round trips only fixed command payload and rejects tokens, duplicate keys, or wrong fields', () => {
    const storage = new MemoryCommandStorage(); const item = record(); const entries = new Map([[commandKey(item.owner, item.campaignId), item]]);
    writeCommandRecords(storage, entries); expect([...readCommandRecords(storage).values()]).toEqual([item]);
    for (const raw of [
      JSON.stringify({ version: 1, records: [{ ...item, token: 'forbidden' }] }),
      JSON.stringify({ version: 1, records: [{ ...item, command: { ...item.command, input: { ...item.command.input, owner: sessionA.owner } } }] }),
      JSON.stringify({ version: 1, records: [item, item] }),
      '{"version":1,"version":1,"records":[]}',
      ' '.repeat(32769),
    ]) { storage.setItem(COMMAND_STORAGE_KEY, raw); expect(() => readCommandRecords(storage)).toThrow(); }
  });
  it.each(['missing', 'read-denied', 'write-denied', 'corrupt'])('refuses a new dispatch when recovery storage is %s', async (kind) => {
    const storage = new MemoryCommandStorage(); if (kind === 'corrupt') storage.setItem(COMMAND_STORAGE_KEY, '{invalid');
    if (kind === 'read-denied') storage.getItem = () => { throw new DOMException('denied'); };
    if (kind === 'write-denied') storage.setItem = () => { throw new DOMException('quota'); };
    const api = apiFixture(); const controller = new CampaignCommandController({ api, session: sessionA, campaignId, isCurrent: () => true }, new CampaignCommandStore(() => kind === 'missing' ? null : storage));
    await controller.confirm(joinIntent); expect(api.execute).not.toHaveBeenCalled(); expect(controller.getSnapshot().storageAvailable).toBe(false);
    expect(controller.getSnapshot().error?.code).toMatch(/^CAMPAIGN_RECOVERY_STORAGE_/);
  });
  it('does not evict aged unresolved keys to make room for a new campaign', async () => {
    const storage = new MemoryCommandStorage(); const entries = new Map<string, CommandRecord>();
    for (let index = 0; index < 20; index++) { const item = { ...record(`campaign-${index}`), createdAt: 1 }; entries.set(commandKey(item.owner, item.campaignId), item); }
    writeCommandRecords(storage, entries);
    const api = apiFixture(); const controller = new CampaignCommandController({ api, session: sessionA, campaignId, isCurrent: () => true }, new CampaignCommandStore(() => storage));
    await controller.confirm(joinIntent); expect(api.execute).not.toHaveBeenCalled(); expect(controller.getSnapshot().error?.code).toBe('CAMPAIGN_COMMAND_CAPACITY'); expect(readCommandRecords(storage).size).toBe(20);
  });
  it('retains read-only recovery when cleanup storage writes become unavailable', async () => {
    const f = fixture(); f.api.execute.mockRejectedValueOnce(new Error('offline')); await f.controller.confirm(joinIntent);
    const { response } = await import('./command-test-fixtures'); const command = f.api.execute.mock.calls[0][2];
    f.storage.setItem = () => { throw new DOMException('quota'); }; f.api.command.mockResolvedValueOnce(response(command));
    await f.controller.reconcile(); expect(f.controller.getSnapshot()).toMatchObject({ status: 'settled', storageAvailable: false }); expect(f.api.command).toHaveBeenCalledTimes(1);
    await f.controller.confirm(joinIntent); expect(f.api.execute).toHaveBeenCalledTimes(1);
  });
});
