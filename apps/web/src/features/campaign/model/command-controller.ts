import type { CampaignSession } from './ports';
import { CampaignAPIError } from './ports';
import { commandError, freezeCommand, newCommandID, requiresReview } from './command-input';
import { checkedCommand, reviewMatches } from './command-response';
import { readCommandRecovery } from './command-recovery';
import { CampaignCommandStore, campaignCommandStore, type CommandRun, type CommandSlot } from './command-store';
import { EMPTY_COMMAND_STATE, type CampaignCommandIntent, type CampaignCommandState, type CommandOptions, type CommandRecord } from './command-types';

export class CampaignCommandController {
  private readonly identity = Symbol('campaign-command-view');
  private get slot(): CommandSlot { return this.store.slot(this.options.session.owner, this.options.campaignId); }
  private disposed = false;
  private source: CampaignCommandState | null = null;
  private snapshot: CampaignCommandState = EMPTY_COMMAND_STATE;
  constructor(private readonly options: CommandOptions, private readonly store: CampaignCommandStore = campaignCommandStore) {
  }
  getSnapshot = (): CampaignCommandState => {
    const source = this.slot.state;
    if (this.source === source) return this.snapshot;
    this.source = source;
    this.snapshot = this.slot.presentation === this.identity ? source : {
      ...EMPTY_COMMAND_STATE, command: this.slot.record?.command ?? null, error: source.error,
      storageAvailable: source.storageAvailable,
      status: this.slot.record ? this.slot.record.disposition === 'review' ? 'requiresReview' : 'unknown' : 'idle',
    };
    return this.snapshot;
  };
  subscribe = (listener: () => void): (() => void) => this.store.subscribe(this.slot, listener);
  private current(): boolean { return !this.disposed && this.options.isCurrent(); }
  private publish(patch: Partial<CampaignCommandState>): void { this.store.update(this.slot, patch, this.identity); }
  private notify(unauthorized = false): void {
    if (!this.current()) return;
    try { (unauthorized ? this.options.onUnauthorized : this.options.onCurrent)?.(); }
    catch { /* Presentation callbacks cannot change a verified command outcome. */ }
  }
  private start(status: 'submitting' | 'reconciling'): CommandRun | null {
    if (!this.current() || this.slot.run) return null;
    const run = { id: Symbol('campaign-request'), owner: this.identity, abort: new AbortController() };
    this.slot.run = run; this.publish({ status, error: null, response: null, me: null, detail: null, canRetry: false, reviewReady: false }); return run;
  }
  private accepts(run: CommandRun): boolean { return this.slot.run === run && this.current(); }
  private restoreUncertain(): void {
    this.publish({ status: this.slot.record?.disposition === 'review' ? 'requiresReview' : 'unknown',
      response: null, me: null, detail: null, canRetry: false, reviewReady: false });
  }
  private finish(run: CommandRun): void {
    if (this.slot.run !== run) return;
    this.slot.run = null;
    if (!this.current()) this.restoreUncertain();
  }
  confirm = async (intent: CampaignCommandIntent): Promise<void> => {
    this.store.initialize();
    if (!this.current() || this.slot.run) return;
    const state = this.getSnapshot();
    if (this.slot.record?.disposition === 'pending') {
      this.publish({ error: { code: 'CAMPAIGN_COMMAND_UNRESOLVED', status: null } }); return;
    }
    if (this.slot.record && (!state.reviewReady || !reviewMatches(intent, state.me, state.detail))) {
      this.publish({ error: { code: 'CAMPAIGN_REVIEW_REQUIRED', status: null } }); return;
    }
    let record: CommandRecord;
    try {
      if (!/^0x[0-9a-f]{40}$/.test(this.options.session.owner) || !/^[a-zA-Z0-9_-]{1,128}$/.test(this.options.campaignId)) throw new CampaignAPIError('CAMPAIGN_INVALID_INPUT', 400);
      const command = freezeCommand(intent, newCommandID());
      record = Object.freeze({ version: 1, owner: this.options.session.owner, campaignId: this.options.campaignId,
        command, createdAt: Date.now(), disposition: 'pending' });
      // Durability precedes dispatch. Storage failure cannot silently turn a
      // reload into permission to generate a second key for an unknown write.
      this.store.persist(this.slot, record);
    } catch (error) { this.publish({ error: commandError(error) }); return; }
    this.publish({ command: record.command, response: null, me: null, detail: null });
    await this.execute(record);
  };
  retry = async (): Promise<void> => {
    const record = this.slot.record;
    if (!record || record.disposition !== 'pending' || !this.getSnapshot().canRetry || this.slot.run) return;
    await this.execute(record);
  };
  private async execute(record: CommandRecord): Promise<void> {
    const run = this.start('submitting'); if (!run) return;
    let review = false;
    try {
      const response = checkedCommand(await this.options.api.execute(this.options.session, record.campaignId, record.command, run.abort.signal), record);
      if (!this.accepts(run)) return;
      try { this.store.persist(this.slot, null); } catch { /* The retained key still protects recovery after reload. */ }
      this.publish({ status: 'settled', response, canRetry: false }); this.notify();
    } catch (error) {
      if (!this.accepts(run)) return;
      const failure = commandError(error);
      review = requiresReview(failure);
      if (review) {
        try { this.store.persist(this.slot, Object.freeze({ ...record, disposition: 'review' })); }
        catch { review = false; }
      }
      this.publish({ status: review ? 'requiresReview' : 'unknown', error: failure,
        response: null, me: null, detail: null, canRetry: false, reviewReady: false });
      if (failure.status === 401) this.notify(true);
    } finally { this.finish(run); }
    if (review && this.current()) await this.reconcile();
  }
  reconcile = async (): Promise<void> => {
    this.store.initialize(); const record = this.slot.record;
    if (!record) return;
    const priorError = this.getSnapshot().error;
    const run = this.start('reconciling'); if (!run) return;
    try {
      const recovered = await readCommandRecovery(this.options.api, this.options.session, record, run.abort.signal);
      if (!this.accepts(run)) return;
      if (recovered.unauthorized) {
        this.publish({ status: 'unknown', response: null, me: null, detail: null, error: recovered.error, canRetry: false }); this.notify(true); return;
      }
      if (recovered.response) {
        try { this.store.persist(this.slot, null); } catch { /* A verified result may be displayed even when cleanup must wait. */ }
        this.publish({ status: 'settled', response: recovered.response, me: recovered.me, detail: recovered.detail, error: recovered.error ?? this.slot.state.error });
      } else {
        this.publish({ status: record.disposition === 'review' ? 'requiresReview' : 'unknown',
          me: recovered.me, detail: recovered.detail, error: recovered.error ?? priorError,
          canRetry: recovered.canRetry, reviewReady: recovered.reviewReady });
      }
      this.notify();
    } catch (error) {
      if (this.accepts(run)) this.publish({ status: record.disposition === 'review' ? 'requiresReview' : 'unknown', error: commandError(error), canRetry: false });
    } finally { this.finish(run); }
  };
  activate(): void { this.disposed = false; }
  dispose(): void {
    this.disposed = true;
    const run = this.slot.run;
    if (run?.owner === this.identity) {
      run.abort.abort(); this.slot.run = null; this.restoreUncertain();
    }
    this.store.release(this.slot);
  }
}
export function sameCampaignSession(left: CampaignSession | null, right: CampaignSession): boolean {
  return !!left && left.owner === right.owner && left.generation === right.generation && left.token === right.token;
}
export type { CampaignCommandIntent, CampaignCommandState } from './command-types';
