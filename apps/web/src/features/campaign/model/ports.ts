import type {
  AdminCommandResponse, CancelInteractionInput, CommandResponse, CreateCommandInput, EventFeedResponse,
  PositiveInteractionInput, PublicationCommandInput, QueryDetailResponse,
  QueryHistoryResponse, QueryListResponse, QueryMeResponse, QueryPreviewResponse,
  RevisionCommandInput,
} from './contract.generated';

export type CampaignSession = Readonly<{ owner: string; generation: number; token: string }>;
export type InteractionOperation = 'join' | 'leave' | 'set-reminder' | 'remove-reminder';
export type InteractionCommand =
  | { operation: 'join' | 'set-reminder'; input: PositiveInteractionInput }
  | { operation: 'leave' | 'remove-reminder'; input: CancelInteractionInput };
export type CampaignListQuery = { phase?: 'upcoming' | 'active' | 'ended' | 'unsupported'; cursor?: string; limit?: number };
export type CampaignPageQuery = { cursor?: string; limit?: number };
export type CampaignPushKey = Readonly<{ campaignId: string; family: 'publication' | 'interaction' | 'personal' }>;
export type CampaignPushReset = 'unauthorized' | 'session_unavailable' | 'not_found' | 'unavailable';
/** Every callback only asks the owning read to repair through REST. */
export type CampaignPushListener = Readonly<{ invalidate(head: bigint): void; subscribed(): void; reset(reason: CampaignPushReset): void }>;
/** Invalidation hints for one key; the returned function ends this watch. A
 * personal key needs the session whose credential authenticates the socket. */
export interface CampaignPush {
  watch(key: CampaignPushKey, session: CampaignSession | null, listener: CampaignPushListener): () => void;
}

/** Credentials are captured per request. Public reads never inherit a global
 * token, and a returning wallet receives a distinct session generation. */
export interface CampaignAPI {
  // Snapshot-only host adapters remain valid; the HTTP adapter supplies both feeds.
  events?(id: string, family: 'publication' | 'interaction', query: CampaignPageQuery, signal?: AbortSignal): Promise<EventFeedResponse>;
  meEvents?(session: CampaignSession, id: string, query: CampaignPageQuery, signal?: AbortSignal): Promise<EventFeedResponse>;
  // Optional acceleration only: polling and REST repair stay authoritative.
  push?: CampaignPush;
  list(query: CampaignListQuery, signal?: AbortSignal): Promise<QueryListResponse>;
  detail(id: string, signal?: AbortSignal): Promise<QueryDetailResponse>;
  me(session: CampaignSession, ids: readonly string[], signal?: AbortSignal): Promise<QueryMeResponse>;
  history(session: CampaignSession, query: CampaignPageQuery, signal?: AbortSignal): Promise<QueryHistoryResponse>;
  command(session: CampaignSession, id: string, operation: InteractionOperation, commandId: string, signal?: AbortSignal): Promise<CommandResponse>;
  execute(session: CampaignSession, id: string, command: InteractionCommand, signal?: AbortSignal): Promise<CommandResponse>;
  create(session: CampaignSession, input: CreateCommandInput, signal?: AbortSignal): Promise<AdminCommandResponse>;
  revision(session: CampaignSession, id: string, input: RevisionCommandInput, signal?: AbortSignal): Promise<AdminCommandResponse>;
  publication(session: CampaignSession, id: string, input: PublicationCommandInput, signal?: AbortSignal): Promise<AdminCommandResponse>;
  preview(session: CampaignSession, id: string, revisionId: string, signal?: AbortSignal): Promise<QueryPreviewResponse>;
  adminCommand(session: CampaignSession, targetId: string, operation: 'create' | 'revision' | 'publish' | 'pause' | 'resume' | 'archive' | 'rollback', commandId: string, signal?: AbortSignal): Promise<AdminCommandResponse>;
}

export class CampaignAPIError extends Error {
  constructor(public readonly code: string, public readonly status: number, public readonly requestId: string | null = null) {
    super(code);
    this.name = 'CampaignAPIError';
  }
}
