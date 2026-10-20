// Generated from packages/shared/contracts/campaign-v2.schema.json. Do not edit.

export interface LocalizedCopy {
  title: string;
  description: string;
  rules: string;
  rewardDescription: string;
}

export interface Translations {
  en: LocalizedCopy;
  zh: LocalizedCopy;
}

export type EmptyParams = Record<string, never>;

export interface JoinRule {
  kind: "walletJoinV1";
  version: 1;
  params: EmptyParams;
}

export interface ReminderRule {
  kind: "reminderIntentV1";
  version: 1;
  params: EmptyParams;
}

export interface RuleSet {
  join: JoinRule;
  reminder: ReminderRule;
}

export interface MaterialTerms {
  copy: Translations;
  startAt: string;
  endAt: string;
  rules: RuleSet;
  rewardDelivery: "display-only";
}

export interface DisplayContent {
  layout: "compact" | "featured";
  sortOrder: number;
  mediaAssetId?: string;
}

export interface CTA {
  actionKind: "none" | "openTrade";
  chainId?: number;
  marketId?: string;
}

export interface RevisionInput {
  schemaVersion: 1;
  termsCanonicalVersion: 1;
  templateId: "campaignCardV1";
  displayContent: DisplayContent;
  materialTerms: MaterialTerms;
  cta: CTA;
}

export interface CreateCommandInput {
  clientCommandId: string;
  reason: string;
  revision: RevisionInput;
  slug?: string;
}

export interface RevisionCommandInput {
  clientCommandId: string;
  reason: string;
  expectedPublicationVersion: string;
  revision: RevisionInput;
}

export interface PublicationCommandInput {
  clientCommandId: string;
  reason: string;
  expectedPublicationVersion: string;
  operation: "publish" | "pause" | "resume" | "archive" | "rollback";
  candidateRevisionId?: string;
}

export interface PositiveInteractionInput {
  clientCommandId: string;
  expectedStateVersion: string;
  expectedPublicationVersion: string;
}

export interface CancelInteractionInput {
  clientCommandId: string;
  expectedStateVersion: string;
}

export interface QueryLegacyBaselineView {
  templateId: "legacyBaselineV1";
  title: string;
  description: (string) | null;
  reward: (string) | null;
  startAt: string;
  endAt: string;
  capturedAt: string;
}

export interface QueryPublicCampaignView {
  id: string;
  slug: (string) | null;
  revisionId: string;
  publicationVersion: string;
  interactionVersion: string;
  mode: "enabled" | "paused" | "archived";
  effectivePhase: "upcoming" | "active" | "ended" | "unsupported";
  contentState: "ready" | "unsupported" | "legacyBaseline";
  content: (RevisionInput) | null;
  baseline?: QueryLegacyBaselineView;
  materialTermsHash: string;
  contentHash: string;
  participantCount: (string) | null;
  statisticsState: "ready" | "unavailable";
  nextTransitionAt: (string) | null;
}

export interface QueryPersonalStateView {
  state: "absent" | "known" | "unknown";
  stateVersion: (string) | null;
  status: ("joined" | "left" | "set" | "removed") | null;
  acceptanceProvenance: ("verified" | "neverAccepted" | "legacyUnknown") | null;
  acceptedRevisionId: (string) | null;
  acceptedPublicationVersion: (string) | null;
  acceptedTermsHash: (string) | null;
  acceptedAt: (string) | null;
  changedAt: (string) | null;
}

export interface QueryMyCampaignView {
  id: string;
  publicationVersion: string;
  interactionVersion: string;
  participation: QueryPersonalStateView;
  reminder: QueryPersonalStateView;
}

export interface QueryHistoryImportView {
  importedAt: string;
  legacyRecordedAt: (string) | null;
}

export interface QueryHistoryView {
  entry?: "command" | "legacyImport";
  campaignId: string;
  kind: "join" | "reminder";
  state: QueryPersonalStateView;
  beforeStatus: ("joined" | "left" | "set" | "removed") | null;
  operation: ("join" | "leave" | "set-reminder" | "remove-reminder") | null;
  clientCommandId: (string) | null;
  interactionVersion: string;
  occurredAt: string;
  acceptedTerms: (MaterialTerms) | null;
  snapshotState: "ready" | "legacyUnknown" | "unsupported" | "none";
  legacyImport?: QueryHistoryImportView;
}

export interface QueryPreviewView {
  campaignId: string;
  revisionId: string;
  contentState: "ready" | "unsupported";
  content: (RevisionInput) | null;
  materialTermsHash: string;
  contentHash: string;
  isPublished: boolean;
}

export interface QueryListResponse {
  schemaVersion: 1;
  serverNow: string;
  asOf: string;
  items: (QueryPublicCampaignView)[];
  nextCursor: (string) | null;
}

export interface QueryMeResponse {
  schemaVersion: 1;
  serverNow: string;
  asOf: string;
  items: (QueryMyCampaignView)[];
}

export interface QueryHistoryResponse {
  schemaVersion: 1;
  serverNow: string;
  asOf: string;
  items: (QueryHistoryView)[];
  nextCursor: (string) | null;
}

export interface QueryDetailResponse {
  schemaVersion: 1;
  serverNow: string;
  asOf: string;
  item: QueryPublicCampaignView;
}

export interface QueryPreviewResponse {
  schemaVersion: 1;
  serverNow: string;
  asOf: string;
  item: QueryPreviewView;
}

export interface AdminRevisionSummary {
  id: string;
  createdAt: string;
  schemaVersion: number;
  termsCanonicalVersion: number;
  contentState: "ready" | "unsupported";
  materialTermsHash: string;
  contentHash: string;
  published: boolean;
  active: boolean;
}

export interface AdminCampaignView {
  campaignId: string;
  slug: (string) | null;
  source: "created" | "legacy-unversioned";
  publicationVersion: string;
  interactionVersion: string;
  mode: "enabled" | "paused" | "archived";
  activeRevisionId: (string) | null;
  effectivePhase: "unpublished" | "upcoming" | "active" | "ended" | "unsupported";
  materialTermsLock: "open" | "locked" | "legacyUnverified" | "unknown";
  hasAcceptedHistory: boolean;
  revisions: (AdminRevisionSummary)[];
  revisionsTruncated: boolean;
}

export interface AdminCampaignResponse {
  schemaVersion: 1;
  serverNow: string;
  asOf: string;
  item: AdminCampaignView;
}

export interface CommandState {
  kind: "join" | "reminder";
  status: "joined" | "left" | "set" | "removed";
  stateVersion: string;
  acceptanceProvenance: "verified" | "neverAccepted" | "legacyUnknown";
  acceptedRevisionId: (string) | null;
  acceptedPublicationVersion: (string) | null;
  acceptedTermsHash: (string) | null;
  acceptedAt: (string) | null;
}

export interface CommandResult {
  campaignId: string;
  revisionId: (string) | null;
  publicationVersion: string;
  interactionVersion: string;
  state: (CommandState) | null;
  changed: boolean;
}

export interface CommandResponse {
  schemaVersion: 1;
  serverNow: string;
  asOf: string;
  original: CommandResult;
  current: CommandResult;
  replayed: boolean;
}

export interface AdminCommandIdentity {
  clientCommandId: string;
  actor: string;
  operation: "create" | "revision" | "publish" | "rollback" | "pause" | "resume" | "archive";
  targetId: string;
  payloadHash: string;
  committedAt: string;
}

export interface AdminCommandResponse {
  schemaVersion: 1;
  serverNow: string;
  asOf: string;
  command: AdminCommandIdentity;
  original: CommandResult;
  current: CommandResult;
  replayed: boolean;
}

export interface ErrorResponse {
  schemaVersion: 1;
  code: string;
  requestId: string;
}

export interface LegacyRunView {
  runId: string;
  status: "capturing" | "captured" | "incomplete" | "abandoned";
  operator: string;
  capturedAt: (string) | null;
  sourceSha256: (string) | null;
  rows: number;
  retainUntil: string;
}

export interface LegacyFindingSummary {
  kind: string;
  severity: "blocker" | "review" | "info";
  count: number;
}

export interface LegacyFenceView {
  fencedBy: string;
  fencedAt: string;
  liftedBy: (string) | null;
  liftedAt: (string) | null;
}

export interface LegacyReviewView {
  reviewId: string;
  runId: string;
  campaignId: string;
  decision: "import-public" | "import-unpublished" | "exclude";
  acceptedDifferences: ("visibility" | "participantCount" | "walletState")[];
  acknowledgedFindings: (string)[];
  reason: string;
  reviewer: string;
  clientCommandId: string;
  decidedAt: string;
}

export interface LegacyActivationSummary {
  activationId: string;
  decision: "import-public" | "import-unpublished";
  activatedBy: string;
  activatedAt: string;
  classes: ("visibility" | "participantCount" | "walletState" | "field")[];
}

export interface LegacyPlanItemView {
  campaignId: string;
  decision: "import-public" | "import-unpublished" | "review-required" | "blocked" | "already-versioned";
  legacyRowSha256: string;
  interactionsSha256: string;
  participantRows: number;
  reminderRows: number;
  findings: (LegacyFindingSummary)[];
  fence: (LegacyFenceView) | null;
  review: (LegacyReviewView) | null;
  activation: (LegacyActivationSummary) | null;
}

export interface LegacyImportPlanResponse {
  schemaVersion: 1;
  serverNow: string;
  asOf: string;
  run: LegacyRunView;
  items: (LegacyPlanItemView)[];
  nextCursor: (string) | null;
  unlistedItems: number;
}

export interface LegacyDifferenceGroupView {
  class: "visibility" | "participantCount" | "walletState" | "field";
  field: string;
  before: string;
  after: string;
  count: number;
  walletRefs: (string)[];
}

export interface LegacyComparisonView {
  instant: string;
  wallets: number;
  groups: (LegacyDifferenceGroupView)[];
  groupsTruncated: boolean;
  classes: ("visibility" | "participantCount" | "walletState" | "field")[];
  accepted: ("visibility" | "participantCount" | "walletState")[];
}

export interface LegacyActivationView {
  activationId: string;
  campaignId: string;
  runId: string;
  decision: "import-public" | "import-unpublished";
  reviewId: (string) | null;
  revisionId: string;
  commandId: string;
  publishCommandId: (string) | null;
  clientCommandId: string;
  payloadHash: string;
  activatedBy: string;
  activatedAt: string;
  publicationVersion: string;
  interactionVersion: string;
  importedInteractions: number;
  comparison: LegacyComparisonView;
}

export interface LegacyReviewCommandInput {
  clientCommandId: string;
  decision: "import-public" | "import-unpublished" | "exclude";
  acceptedDifferences: ("visibility" | "participantCount" | "walletState")[];
  acknowledgedFindings: (string)[];
  reason: string;
}

export interface LegacyActivationCommandInput {
  clientCommandId: string;
  reason: string;
  legacyRowSha256: string;
  interactionsSha256: string;
}

export interface LegacyReviewResponse {
  schemaVersion: 1;
  serverNow: string;
  asOf: string;
  review: LegacyReviewView;
  replayed: boolean;
}

export interface LegacyActivationResponse {
  schemaVersion: 1;
  serverNow: string;
  asOf: string;
  activation: LegacyActivationView;
  replayed: boolean;
}

export interface LegacyDriftView {
  table: "launchpad_campaigns" | "campaign_participants" | "campaign_reminders";
  change: "added" | "changed" | "removed";
  rowRef: string;
  afterRef: (string) | null;
}

export interface LegacyImportRefusal {
  schemaVersion: 1;
  code: string;
  requestId: string;
  drift: (LegacyDriftView)[];
  driftTruncated: boolean;
  comparison: (LegacyComparisonView) | null;
}

export interface EventFeedHint {
  eventId: string;
  source: "campaign";
  streamVersion: string;
  publicationVersion: string;
  interactionVersion: string;
  eventSchema: 1;
  occurredAt: string;
}

export interface EventFeedResponse {
  schemaVersion: 1;
  serverNow: string;
  asOf: string;
  campaignId: string;
  family: "publication" | "interaction" | "personal";
  headVersion: string;
  cursor: string;
  hasMore: boolean;
  resetRequired: boolean;
  events: (EventFeedHint)[];
}

export interface PushSubscriptionInput {
  campaignId: string;
  family: "publication" | "interaction" | "personal";
}

export interface PushAck {
  success: boolean;
  campaignId?: string;
  family?: "publication" | "interaction" | "personal";
  code?: "invalid_request" | "unauthorized" | "session_unavailable" | "not_found" | "unavailable" | "too_many_subscriptions" | "capacity" | "rate_limited" | "cancelled";
  message?: string;
}

export interface PushInvalidation {
  schemaVersion: 1;
  campaignId: string;
  family: "publication" | "interaction" | "personal";
  headVersion: string;
}

export interface PushReset {
  schemaVersion: 1;
  campaignId: string;
  family: "publication" | "interaction" | "personal";
  reason: "unauthorized" | "session_unavailable" | "not_found";
}

export interface CampaignContractMap {
  LocalizedCopy: LocalizedCopy;
  Translations: Translations;
  EmptyParams: EmptyParams;
  JoinRule: JoinRule;
  ReminderRule: ReminderRule;
  RuleSet: RuleSet;
  MaterialTerms: MaterialTerms;
  DisplayContent: DisplayContent;
  CTA: CTA;
  RevisionInput: RevisionInput;
  CreateCommandInput: CreateCommandInput;
  RevisionCommandInput: RevisionCommandInput;
  PublicationCommandInput: PublicationCommandInput;
  PositiveInteractionInput: PositiveInteractionInput;
  CancelInteractionInput: CancelInteractionInput;
  QueryLegacyBaselineView: QueryLegacyBaselineView;
  QueryPublicCampaignView: QueryPublicCampaignView;
  QueryPersonalStateView: QueryPersonalStateView;
  QueryMyCampaignView: QueryMyCampaignView;
  QueryHistoryImportView: QueryHistoryImportView;
  QueryHistoryView: QueryHistoryView;
  QueryPreviewView: QueryPreviewView;
  QueryListResponse: QueryListResponse;
  QueryMeResponse: QueryMeResponse;
  QueryHistoryResponse: QueryHistoryResponse;
  QueryDetailResponse: QueryDetailResponse;
  QueryPreviewResponse: QueryPreviewResponse;
  AdminRevisionSummary: AdminRevisionSummary;
  AdminCampaignView: AdminCampaignView;
  AdminCampaignResponse: AdminCampaignResponse;
  CommandState: CommandState;
  CommandResult: CommandResult;
  CommandResponse: CommandResponse;
  AdminCommandIdentity: AdminCommandIdentity;
  AdminCommandResponse: AdminCommandResponse;
  ErrorResponse: ErrorResponse;
  LegacyRunView: LegacyRunView;
  LegacyFindingSummary: LegacyFindingSummary;
  LegacyFenceView: LegacyFenceView;
  LegacyReviewView: LegacyReviewView;
  LegacyActivationSummary: LegacyActivationSummary;
  LegacyPlanItemView: LegacyPlanItemView;
  LegacyImportPlanResponse: LegacyImportPlanResponse;
  LegacyDifferenceGroupView: LegacyDifferenceGroupView;
  LegacyComparisonView: LegacyComparisonView;
  LegacyActivationView: LegacyActivationView;
  LegacyReviewCommandInput: LegacyReviewCommandInput;
  LegacyActivationCommandInput: LegacyActivationCommandInput;
  LegacyReviewResponse: LegacyReviewResponse;
  LegacyActivationResponse: LegacyActivationResponse;
  LegacyDriftView: LegacyDriftView;
  LegacyImportRefusal: LegacyImportRefusal;
  EventFeedHint: EventFeedHint;
  EventFeedResponse: EventFeedResponse;
  PushSubscriptionInput: PushSubscriptionInput;
  PushAck: PushAck;
  PushInvalidation: PushInvalidation;
  PushReset: PushReset;
}
export type CampaignContractName = keyof CampaignContractMap;
