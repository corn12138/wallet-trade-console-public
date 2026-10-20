import { commandError } from './command-input';
import { CampaignAPIError } from './ports';
import { browserCommandStorage, commandKey, readCommandRecords, writeCommandRecords, type CommandStorage } from './command-storage';
import { EMPTY_COMMAND_STATE, type CampaignCommandState, type CommandRecord } from './command-types';

export type CommandRun = { id: symbol; owner: symbol; abort: AbortController };
export type CommandSlot = {
  key: string;
  record: CommandRecord | null;
  state: CampaignCommandState;
  presentation: symbol | null;
  run: CommandRun | null;
  listeners: Set<() => void>;
};
export class CampaignCommandStore {
  private records = new Map<string, CommandRecord>();
  private slots = new Map<string, CommandSlot>();
  private initialized = false;
  private storage: CommandStorage | null = null;
  private storageError: CampaignCommandState['error'] = null;
  constructor(private readonly storageFactory: () => CommandStorage | null = browserCommandStorage) {}
  initialize(): void {
    if (this.initialized) return;
    this.initialized = true;
    try {
      this.storage = this.storageFactory();
      if (!this.storage) throw new CampaignAPIError('CAMPAIGN_RECOVERY_STORAGE_UNAVAILABLE', 0);
      this.records = readCommandRecords(this.storage);
      for (const slot of this.slots.values()) this.restore(slot);
    } catch (error) { this.storageError = commandError(error instanceof CampaignAPIError ? error : new CampaignAPIError('CAMPAIGN_RECOVERY_STORAGE_UNAVAILABLE', 0)); }
    for (const slot of this.slots.values()) this.update(slot, { storageAvailable: !this.storageError, error: this.storageError });
  }
  slot(owner: string, campaignId: string): CommandSlot {
    const key = commandKey(owner, campaignId); const previous = this.slots.get(key);
    if (previous) return previous;
    const slot: CommandSlot = { key, record: null, state: EMPTY_COMMAND_STATE, presentation: null, run: null, listeners: new Set() };
    this.slots.set(key, slot);
    if (this.initialized) this.restore(slot);
    return slot;
  }
  private restore(slot: CommandSlot): void {
    slot.record = this.records.get(slot.key) ?? null;
    slot.state = { ...EMPTY_COMMAND_STATE, storageAvailable: !this.storageError && !!this.storage,
      error: this.storageError, command: slot.record?.command ?? null,
      status: slot.record ? slot.record.disposition === 'review' ? 'requiresReview' : 'unknown' : 'idle' };
  }
  update(slot: CommandSlot, patch: Partial<CampaignCommandState>, presentation?: symbol): void {
    slot.state = Object.freeze({ ...slot.state, ...patch });
    if (presentation) slot.presentation = presentation;
    for (const listener of slot.listeners) listener();
  }
  persist(slot: CommandSlot, record: CommandRecord | null): void {
    this.initialize();
    if (!this.storage || this.storageError) throw new CampaignAPIError(this.storageError?.code ?? 'CAMPAIGN_RECOVERY_STORAGE_UNAVAILABLE', 0);
    const records = new Map(this.records);
    if (record) records.set(slot.key, record); else records.delete(slot.key);
    try { writeCommandRecords(this.storage, records); }
    catch (error) {
      const failure = error instanceof CampaignAPIError ? error : new CampaignAPIError('CAMPAIGN_RECOVERY_STORAGE_UNAVAILABLE', 0);
      if (failure.code !== 'CAMPAIGN_COMMAND_CAPACITY') this.storageError = commandError(failure);
      this.update(slot, { storageAvailable: !this.storageError, error: commandError(failure) });
      throw failure;
    }
    this.records = records; slot.record = record;
  }
  // Pending intents are never evicted by age: a missing lookup does not prove
  // that an older request has stopped. Capacity pressure rejects a new write.
  release(slot: CommandSlot): void {
    // StrictMode can immediately resubscribe; eviction waits for that commit
    // while pending records and in-flight requests always retain their lock.
    queueMicrotask(() => {
      if (!slot.listeners.size && !slot.record && !slot.run && this.slots.get(slot.key) === slot) this.slots.delete(slot.key);
    });
  }
  subscribe(slot: CommandSlot, listener: () => void): () => void {
    slot.listeners.add(listener); this.initialize(); return () => {
      slot.listeners.delete(listener); this.release(slot);
    };
  }
}
export const campaignCommandStore = new CampaignCommandStore();
