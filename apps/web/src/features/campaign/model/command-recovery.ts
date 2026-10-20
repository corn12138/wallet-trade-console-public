import type { CampaignAPI, CampaignSession } from './ports';
import type { CommandResponse, QueryDetailResponse, QueryMeResponse } from './contract.generated';
import type { CommandError, CommandRecord } from './command-types';
import { commandError } from './command-input';
import { checkedCommand, checkedDetail, checkedMe, personalVersion } from './command-response';

export type CommandRecovery = {
  response: CommandResponse | null;
  me: QueryMeResponse | null;
  detail: QueryDetailResponse | null;
  error: CommandError | null;
  unauthorized: boolean;
  canRetry: boolean;
  reviewReady: boolean;
};
export async function readCommandRecovery(api: CampaignAPI, session: CampaignSession, record: CommandRecord, signal: AbortSignal): Promise<CommandRecovery> {
  const { campaignId, command } = record;
  const review = record.disposition === 'review';
  const reads = await Promise.allSettled([
    api.command(session, campaignId, command.operation, command.input.clientCommandId, signal).then((value) => checkedCommand(value, record)),
    api.me(session, [campaignId], signal).then((value) => checkedMe(value, campaignId)),
    review ? api.detail(campaignId, signal).then((value) => checkedDetail(value, campaignId)) : Promise.resolve(null),
  ] as const);
  const [ledger, personal, content] = reads;
  const errors = reads.flatMap((read) => read.status === 'rejected' ? [commandError(read.reason)] : []);
  const unauthorized = errors.some((error) => error.status === 401);
  const response = ledger.status === 'fulfilled' ? ledger.value : null;
  const me = personal.status === 'fulfilled' ? personal.value : null;
  const detail = content.status === 'fulfilled' ? content.value : null;
  const missing = ledger.status === 'rejected' && commandError(ledger.reason).status === 404;
  const kind = command.operation === 'join' || command.operation === 'leave' ? 'participation' : 'reminder';
  const knownState = !!me && personalVersion(me.items[0][kind]) !== null;
  const cancel = command.operation === 'leave' || command.operation === 'remove-reminder';
  // A successful Me read cannot identify which command produced its state.
  // Even after a ledger 404, recovery retains the exact key for late commits.
  return {
    response: unauthorized ? null : response, me: unauthorized ? null : me, detail,
    error: errors.find((error) => error.status === 401) ?? errors.find((error) => error.status !== 404) ?? null,
    unauthorized, canRetry: !review && !unauthorized && missing && knownState,
    reviewReady: review && !unauthorized && missing && knownState && (cancel || !!detail),
  };
}
