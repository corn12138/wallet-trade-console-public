import type { CommandResponse, QueryDetailResponse, QueryMeResponse } from './contract.generated';
import type { CampaignAPI, CampaignSession, InteractionCommand } from './ports';

export type CampaignCommandIntent =
  | { operation: 'join' | 'set-reminder'; expectedStateVersion: string; expectedPublicationVersion: string }
  | { operation: 'leave' | 'remove-reminder'; expectedStateVersion: string };
export type CommandError = Readonly<{ code: string; status: number | null }>;
export type CampaignCommandState = Readonly<{
  status: 'idle' | 'submitting' | 'unknown' | 'reconciling' | 'settled' | 'requiresReview';
  command: InteractionCommand | null;
  response: CommandResponse | null;
  me: QueryMeResponse | null;
  detail: QueryDetailResponse | null;
  error: CommandError | null;
  canRetry: boolean;
  reviewReady: boolean;
  storageAvailable: boolean;
}>;
export const EMPTY_COMMAND_STATE: CampaignCommandState = Object.freeze({
  status: 'idle', command: null, response: null, me: null, detail: null,
  error: null, canRetry: false, reviewReady: false, storageAvailable: false,
});
export type CommandRecord = Readonly<{
  version: 1;
  owner: string;
  campaignId: string;
  command: InteractionCommand;
  createdAt: number;
  disposition: 'pending' | 'review';
}>;
export type CommandOptions = {
  api: CampaignAPI;
  session: CampaignSession;
  campaignId: string;
  isCurrent: () => boolean;
  onCurrent?: () => void;
  onUnauthorized?: () => void;
};
