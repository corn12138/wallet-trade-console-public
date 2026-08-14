import { fetchApi } from './auth-fetch';

async function parseAtlasResponse<T>(response: Response): Promise<T> {
  if (!response.ok) {
    const payload = await response.json().catch(() => ({}));
    throw new Error(payload.message || `HTTP ${response.status}`);
  }

  const data = await response.json();
  return data.data || data;
}

function buildQuery(
  pathname: string,
  params: Record<string, string | number | undefined | null>,
) {
  const searchParams = new URLSearchParams();

  Object.entries(params).forEach(([key, value]) => {
    if (value !== undefined && value !== null && value !== '') {
      searchParams.set(key, String(value));
    }
  });

  const query = searchParams.toString();
  return query ? `${pathname}?${query}` : pathname;
}

export interface AtlasPortfolioSummary {
  address: string | null;
  portfolioValueUsd: number | null;
  dayChangePct: number;
  trackedWallets: number;
  recentActivityCount: number;
  pendingTxCount: number;
  securityAlertCount: number;
  dataCompleteness: string;
  assetMix: {
    earnPositions: number;
    perpPositions: number;
  };
  assetFilters: AtlasPortfolioAssetFilters;
  topMovers: Array<{
    id: string;
    symbol: string;
    name: string;
    address: string | null;
    priceChange24h: number;
  }>;
  marketSnapshot: {
    activeMarkets: number;
    totalVolume: string;
    totalOpenInterest: string;
  };
}

export interface AtlasPortfolioAsset {
  id: string;
  assetType: string;
  symbol: string;
  name: string;
  chainId: number;
  rawBalance: string;
  valueUsd: number;
  status: string;
  source: string;
  address: string | null;
  walletAddress: string | null;
  updatedAt: string;
}

export interface AtlasPortfolioAssetFilters {
  spamFilterLevel: string;
  totalCount: number;
  visibleCount: number;
  filteredCount: number;
  hiddenConfiguredCount: number;
  hiddenMatchedCount: number;
  spamFilteredCount: number;
}

export interface AtlasPortfolioAssetsResponse {
  items: AtlasPortfolioAsset[];
  filterSummary: AtlasPortfolioAssetFilters;
}

export interface AtlasWalletRecord {
  id: string;
  address: string;
  chainId: number;
  walletType: string;
  authState: string;
  linkedUserId: string | null;
  lastLoginAt: string | null;
  createdAt: string;
  updatedAt: string;
  profile?: AtlasWalletProfile | null;
}

export interface AtlasWalletProfile {
  id: string;
  ownerAddress: string;
  displayName: string | null;
  groupId: string | null;
  groupName: string | null;
  groupColor: string | null;
  isDefault: boolean;
  isImported: boolean;
  avatarSeed: string | null;
  notes: string | null;
  createdAt: string;
  updatedAt: string;
}

export interface AtlasWalletGroup {
  id: string;
  ownerAddress: string;
  name: string;
  color: string | null;
  sortOrder: number;
  walletCount: number;
  createdAt: string;
  updatedAt: string;
}

export interface AtlasHiddenAsset {
  id: string;
  ownerAddress: string;
  walletAddress: string | null;
  chainId: number;
  assetKey: string;
  assetAddress: string | null;
  symbol: string;
  reason: string;
  createdAt: string;
  updatedAt: string;
}

export interface AtlasWalletManager {
  ownerAddress: string | null;
  wallets: AtlasWalletRecord[];
  groups: AtlasWalletGroup[];
  hiddenAssets: AtlasHiddenAsset[];
  summary: {
    totalWallets: number;
    connectedWallets: number;
    watchOnlyWallets: number;
    importedWallets: number;
    groupedWallets: number;
    hiddenAssetCount: number;
    defaultWalletId: string | null;
  };
}

export interface AtlasUserPreferences {
  ownerAddress: string | null;
  theme: string;
  fiatCurrency: string;
  testnetEnabled: boolean;
  notificationsEnabled: boolean;
  spamFilterLevel: string;
  source: 'stored' | 'default';
  createdAt: string | null;
  updatedAt: string | null;
}

export interface AtlasNotificationPreferences {
  ownerAddress: string | null;
  securityAlerts: boolean;
  txUpdates: boolean;
  marketAlerts: boolean;
  productUpdates: boolean;
  source: 'stored' | 'default';
  createdAt: string | null;
  updatedAt: string | null;
}

export interface AtlasDiscoverHome {
  generatedAt: string;
  trendingTokens: Array<{
    id: string;
    symbol: string;
    name: string;
    address: string | null;
    priceChange24h: number;
    volume24h: number;
    marketCap: number;
    badges: string[];
  }>;
  curatedDapps: Array<{
    id: string;
    name: string;
    slug: string;
    category: string;
    summary: string;
    risk: string;
    /** "live" = shippable product surface; "roadmap" = not launchable yet. */
    status?: string;
  }>;
  earnProducts: Array<{
    id: string;
    name: string;
    type: string;
    apy: number;
    tvl: number;
    chainId: number;
    status: string;
  }>;
  marketPulse: Array<{
    symbol: string;
    chainId: number;
    volume24h: string;
    fundingRate: string;
    openInterest: {
      long: string;
      short: string;
    };
  }>;
  riskMarkers: Array<{
    id: string;
    label: string;
    value: string | number;
  }>;
}

export interface AtlasActivityFeed {
  items: Array<{
    id: string;
    source: string;
    type: string;
    title: string;
    subtitle: string;
    status: string;
    chainId: number;
    txHash: string | null;
    timestamp: string;
  }>;
  total: number;
}

export interface AtlasApprovalRecord {
  tokenAddress: string;
  spender: string;
  allowance: string;
  chainId: number;
  lastUpdatedAt: string;
  txHash: string;
}

export interface AtlasSecurityAlert {
  id: string;
  severity: 'critical' | 'high' | 'medium' | 'low';
  category: 'approval' | 'spender' | 'activity';
  title: string;
  summary: string;
  chainId: number;
  occurredAt: string;
  tokenAddress?: string;
  spender?: string;
  txHash?: string;
  actionLabel?: string;
  actionType?: 'revoke-approval' | 'review-spender';
}

export interface AtlasConnectedSite {
  id: string;
  ownerAddress: string;
  chainId: number | null;
  origin: string;
  domain: string;
  siteName: string;
  iconUrl: string | null;
  category: string | null;
  riskLevel: string;
  permissions: string[];
  firstConnectedAt: string;
  lastConnectedAt: string;
  createdAt: string;
  updatedAt: string;
}

export interface AtlasTxReviewCheck {
  id: string;
  severity: 'critical' | 'high' | 'medium' | 'low' | 'info';
  status: 'pass' | 'warn' | 'fail';
  title: string;
  summary: string;
}

export interface AtlasTxReviewInput {
  operationType:
    | 'approve'
    | 'revoke-approval'
    | 'swap'
    | 'earn-deposit'
    | 'earn-withdraw'
    | 'bridge-deposit'
    | 'custom';
  fromAddress: string;
  chainId?: number;
  siteOrigin?: string;
  nativeBalanceWei?: string;
  tokenAddress?: string;
  spender?: string;
  amount?: string;
  tokenDecimals?: number;
  tokenIn?: string;
  tokenOut?: string;
  tokenInDecimals?: number;
  tokenOutDecimals?: number;
  amountOutMin?: string;
  recipient?: string;
  /** bridge-deposit only: the destination chain of the transfer. */
  bridgeDstChainId?: number;
  deadlineSeconds?: number;
  slippageBps?: number;
  quoteExpiresAt?: string;
  productId?: string;
  tx?: {
    to: string;
    value?: string;
    data: string;
  };
}

export interface AtlasTxReviewResult {
  reviewStatus: 'approved' | 'warning' | 'blocked' | 'simulation-unavailable';
  operationType: AtlasTxReviewInput['operationType'];
  chainId: number;
  riskScore: number;
  simulation: {
    mode: 'estimate-gas' | 'fallback';
    gasEstimate: string | null;
    gasLimitSuggestion: string | null;
    estimatedFeeWei: string | null;
    callSucceeded: boolean | null;
    errorMessage: string | null;
  };
  generatedTx: {
    chainId: number;
    to: string;
    value: string;
    data: string;
  } | null;
  allowanceChange: {
    tokenAddress: string;
    spender: string;
    currentAllowance: string | null;
    requestedAllowance: string | null;
    isUnlimited: boolean;
  } | null;
  connectedSite: Pick<AtlasConnectedSite, 'id' | 'origin' | 'siteName' | 'riskLevel'> | null;
  recommendedActions: string[];
  checks: AtlasTxReviewCheck[];
}

export interface AtlasSwapQuote {
  chainId: number;
  routeSource: string;
  /** "live" | "fallback" — machine-readable quote provenance. */
  quoteStatus?: string;
  /** False for fallback estimates: the swap CTA must stay disabled. */
  executable?: boolean;
  routerAddress: string | null;
  amountIn: string;
  amountOut: string;
  amountOutRaw: string;
  minimumReceived: string;
  minimumReceivedRaw: string;
  priceImpactPct: number;
  slippageBps: number;
  path: string[];
  warnings: string[];
}

export interface AtlasEarnProduct {
  id: string;
  name: string;
  productType: string;
  chainId: number;
  tokenAddress: string;
  rewardToken: string | null;
  apy: number;
  tvl: number;
  status: string;
  contractAddress: string | null;
}

export interface AtlasMarketsSnapshot {
  generatedAt: string;
  summary: {
    activeMarkets: number;
    totalVolume: string;
    totalOpenInterest: string;
  };
  symbols: Array<{
    symbol: string;
    chainId: number;
    volume24h: string;
    longOpenInterest: string;
    shortOpenInterest: string;
    fundingRate: string;
  }>;
  topMovers: Array<{
    symbol: string;
    address: string | null;
    change24h: number;
  }>;
}

export async function getPortfolioSummary(address?: string, chainId?: number) {
  const response = await fetchApi(buildQuery('/portfolio/summary', { address, chainId }));
  return parseAtlasResponse<AtlasPortfolioSummary>(response);
}

export async function getPortfolioAssets(address?: string, chainId?: number) {
  const response = await fetchApi(buildQuery('/portfolio/assets', { address, chainId }));
  return parseAtlasResponse<AtlasPortfolioAssetsResponse>(response);
}

export async function getWallets(address?: string, chainId?: number) {
  const response = await fetchApi(buildQuery('/wallets', { address, chainId }));
  return parseAtlasResponse<AtlasWalletRecord[]>(response);
}

export async function getWalletManager(ownerAddress?: string, chainId?: number) {
  const response = await fetchApi(buildQuery('/wallets/manager', { ownerAddress, chainId }));
  return parseAtlasResponse<AtlasWalletManager>(response);
}

export async function createWatchOnlyWallet(input: {
  address: string;
  chainId?: number;
  ownerAddress?: string;
  displayName?: string;
  groupId?: string;
  isImported?: boolean;
  notes?: string | null;
  avatarSeed?: string | null;
}) {
  const response = await fetchApi('/wallets/watch-only', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(input),
  });

  return parseAtlasResponse<AtlasWalletRecord>(response);
}

export async function createWalletGroup(input: {
  ownerAddress: string;
  name: string;
  color?: string | null;
}) {
  const response = await fetchApi('/wallets/groups', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(input),
  });

  return parseAtlasResponse<AtlasWalletGroup>(response);
}

export async function updateWalletProfile(
  walletId: string,
  input: {
    ownerAddress: string;
    displayName?: string | null;
    groupId?: string | null;
    isDefault?: boolean;
    isImported?: boolean;
    notes?: string | null;
    avatarSeed?: string | null;
  },
) {
  const response = await fetchApi(`/wallets/${walletId}/profile`, {
    method: 'PUT',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(input),
  });

  return parseAtlasResponse<AtlasWalletRecord>(response);
}

export async function getDiscoverHome(chainId?: number) {
  const response = await fetchApi(buildQuery('/discover/home', { chainId }));
  return parseAtlasResponse<AtlasDiscoverHome>(response);
}

export async function getActivityFeed(params: {
  address?: string;
  chainId?: number;
  type?: string;
  limit?: number;
}) {
  const response = await fetchApi(buildQuery('/activity', params));
  return parseAtlasResponse<AtlasActivityFeed>(response);
}

export async function getSecurityApprovals(address?: string, chainId?: number) {
  const response = await fetchApi(buildQuery('/security/approvals', { address, chainId }));
  return parseAtlasResponse<AtlasApprovalRecord[]>(response);
}

export async function getSecurityAlerts(address?: string, chainId?: number) {
  const response = await fetchApi(buildQuery('/security/alerts', { address, chainId }));
  return parseAtlasResponse<AtlasSecurityAlert[]>(response);
}

export async function getConnectedSites(address?: string, chainId?: number) {
  const response = await fetchApi(
    buildQuery('/security/connected-sites', { address, chainId }),
  );
  return parseAtlasResponse<AtlasConnectedSite[]>(response);
}

export async function upsertConnectedSite(input: {
  ownerAddress: string;
  chainId?: number;
  origin: string;
  siteName: string;
  iconUrl?: string | null;
  category?: string | null;
  riskLevel?: string | null;
  permissions?: string[];
}) {
  const response = await fetchApi('/security/connected-sites', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(input),
  });

  return parseAtlasResponse<AtlasConnectedSite>(response);
}

export async function removeConnectedSite(siteId: string, ownerAddress: string) {
  const response = await fetchApi(
    buildQuery(`/security/connected-sites/${siteId}`, { ownerAddress }),
    {
      method: 'DELETE',
    },
  );

  return parseAtlasResponse<{ id: string; removed: boolean }>(response);
}

export async function buildRevokeApprovalTx(input: {
  tokenAddress: string;
  spender: string;
  chainId?: number;
}) {
  const response = await fetchApi('/security/revoke/build-tx', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(input),
  });

  return parseAtlasResponse<{
    chainId: number;
    to: string;
    value: string;
    data: string;
  }>(response);
}

export async function reviewSecurityTransaction(input: AtlasTxReviewInput) {
  const response = await fetchApi('/security/tx-review', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(input),
  });

  return parseAtlasResponse<AtlasTxReviewResult>(response);
}

/**
 * The plain-language layer on top of a review. `source: 'static'` is a normal,
 * complete answer — the deterministic checks already carry displayable text —
 * so the UI must not render it as a failure.
 */
export interface AtlasTxReviewExplanation {
  headline: string;
  whatHappens: string[];
  watchOut: string[];
  locale: string;
}

export interface AtlasTxReviewExplainResult {
  /** The server's own review. It is the verdict; the explanation is caption. */
  review: AtlasTxReviewResult;
  explanation: AtlasTxReviewExplanation | null;
  source: 'ai' | 'static';
  reason?: 'AI_DISABLED' | 'AI_UNAVAILABLE';
  model?: string;
}

/**
 * Sends the SAME input as `reviewSecurityTransaction`, not a finished review:
 * the server re-derives the verdict it explains, so nothing a client writes
 * reaches the model or the response.
 */
export async function explainSecurityTransaction(
  input: AtlasTxReviewInput & { locale?: string },
) {
  const response = await fetchApi('/security/tx-review/explain', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(input),
  });

  return parseAtlasResponse<AtlasTxReviewExplainResult>(response);
}

export async function getSwapQuote(input: {
  chainId?: number;
  tokenIn: string;
  tokenOut: string;
  amountIn: string;
  tokenInDecimals: number;
  tokenOutDecimals: number;
  slippageBps?: number;
}) {
  const response = await fetchApi('/swap/quote', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(input),
  });

  return parseAtlasResponse<AtlasSwapQuote>(response);
}

export async function getEarnProducts(chainId?: number) {
  const response = await fetchApi(buildQuery('/earn/products', { chainId }));
  return parseAtlasResponse<AtlasEarnProduct[]>(response);
}

export async function getMarketsSnapshot(chainId?: number) {
  const response = await fetchApi(buildQuery('/markets/snapshot', { chainId }));
  return parseAtlasResponse<AtlasMarketsSnapshot>(response);
}

export async function getUserPreferences(ownerAddress?: string) {
  const response = await fetchApi(buildQuery('/settings/preferences', { ownerAddress }));
  return parseAtlasResponse<AtlasUserPreferences>(response);
}

export async function updateUserPreferences(input: {
  ownerAddress: string;
  theme?: string;
  fiatCurrency?: string;
  testnetEnabled?: boolean;
  notificationsEnabled?: boolean;
  spamFilterLevel?: string;
}) {
  const response = await fetchApi('/settings/preferences', {
    method: 'PUT',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(input),
  });

  return parseAtlasResponse<AtlasUserPreferences>(response);
}

export async function getNotificationPreferences(ownerAddress?: string) {
  const response = await fetchApi(
    buildQuery('/settings/notifications', { ownerAddress }),
  );
  return parseAtlasResponse<AtlasNotificationPreferences>(response);
}

export async function updateNotificationPreferences(input: {
  ownerAddress: string;
  securityAlerts?: boolean;
  txUpdates?: boolean;
  marketAlerts?: boolean;
  productUpdates?: boolean;
}) {
  const response = await fetchApi('/settings/notifications', {
    method: 'PUT',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(input),
  });

  return parseAtlasResponse<AtlasNotificationPreferences>(response);
}

export async function getHiddenAssets(ownerAddress?: string, chainId?: number) {
  const response = await fetchApi(
    buildQuery('/settings/hidden-assets', { ownerAddress, chainId }),
  );
  return parseAtlasResponse<AtlasHiddenAsset[]>(response);
}

export async function hideAsset(input: {
  ownerAddress: string;
  walletAddress?: string | null;
  chainId: number;
  assetAddress?: string | null;
  symbol: string;
  reason?: string;
}) {
  const response = await fetchApi('/settings/hidden-assets', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify(input),
  });

  return parseAtlasResponse<AtlasHiddenAsset>(response);
}

export async function removeHiddenAsset(hiddenAssetId: string, ownerAddress: string) {
  const response = await fetchApi(
    buildQuery(`/settings/hidden-assets/${hiddenAssetId}`, { ownerAddress }),
    {
      method: 'DELETE',
    },
  );

  return parseAtlasResponse<{ id: string; removed: boolean }>(response);
}

export interface AtlasCampaign {
  id: string;
  title: string;
  description: string | null;
  banner: string | null;
  startDate: string;
  endDate: string;
  status: string;
  reward: string | null;
  participants: number;
  /** Real COUNT of campaign_participants rows (unlike the editorial scalar). */
  participantCount: number;
  /** Present only when the request carried a verified wallet token. */
  isParticipating?: boolean;
  hasReminder?: boolean;
}

export async function getCampaigns(status?: string) {
  const response = await fetchApi(buildQuery('/campaign', { status }));
  return parseAtlasResponse<AtlasCampaign[]>(response);
}

export interface AtlasRankingToken {
  id: string;
  // The `/token` list endpoint (Go `token.Token`) returns the real contract
  // `address` (nullable until a token is deployed/indexed). RankingPage links by
  // it; rows without an address are rendered non-clickable rather than linking to
  // a symbol the token-detail `[address]` route cannot resolve.
  address: string | null;
  symbol: string;
  name: string;
  tags: string[] | null;
  status: string;
  // Normalized NATIVE decimal strings (2026-07-10 unit contract).
  marketCap: string | null;
  volume24h: string | null;
  priceChange24h: number | null;
}

export async function getRankingTokens(params: {
  status?: string;
  sortBy?: 'marketCap' | 'volume' | 'trending';
  limit?: number;
} = {}) {
  const response = await fetchApi(buildQuery('/token', params));
  // /api/token returns {data, meta}; parseAtlasResponse already
  // unwraps `.data`, so we drop pagination metadata here. Add a
  // sibling helper later if the FE needs `meta`.
  return parseAtlasResponse<AtlasRankingToken[]>(response);
}

export interface AtlasTokenDetail {
  id: string;
  address: string | null;
  chainId: number;
  symbol: string;
  name: string;
  image: string | null;
  banner: string | null;
  tags: string[] | null;
  status: string;
  launchType: string;
  isOfficial: boolean;
  // Normalized NATIVE decimal strings (2026-07-10 unit contract).
  marketCap: string | null;
  volume24h: string | null;
  priceChange24h: number | null;
  creatorAddress: string;
  launchedAt: string | null;
  createdAt: string;
}

export async function getTokenBySymbol(symbol: string) {
  const response = await fetchApi(`/token/symbol/${encodeURIComponent(symbol)}`);
  return parseAtlasResponse<AtlasTokenDetail>(response);
}

// ── Product diagnostics (GET /api/status/product) ──────────────────────────
// Read-only Go-owned health/readiness surface. Used to explain WHY a screen is
// empty or an action disabled (RPC/indexer/quote/media/bridge/realtime/data).
export interface AtlasProductStatusWarning {
  code: string;
  severity: 'info' | 'warn';
  message: string;
}

export interface AtlasProductStatus {
  runtime: { backend: string; version: string };
  chain: { defaultChainId: number; supportedChains: number[] };
  database: { status: string; detail?: string };
  indexer: {
    status: string;
    cursorBlock: number | null;
    highestEventBlock: number | null;
    lagBlocks: number | null;
    eventsTotal: number | null;
    lastEventAt: string | null;
    detail?: string;
  };
  realtime: { status: string; detail?: string };
  swap: { status: string; routerConfigured: boolean; routerAddress: string | null };
  bridge: {
    mode: string;
    executable: boolean;
    deliverableChains: number[];
    /** Delivery-worker liveness. Null only when the heartbeat is unreadable. */
    relayer: {
      status: string;
      address?: string;
      lastSeenAt: string | null;
      canDeliver: boolean;
      lastCycleError?: string;
      gasWei?: Record<string, string>;
      stuckTransfers: number | null;
    } | null;
  };
  media: { status: string; mode: string };
  /** Plain-language tx-review explanations. Off by default; off is not a fault. */
  aiExplain: { status: string; detail?: string };
  paperTrade: { status: string; detail?: string };
  data: {
    tokens: number | null;
    perpTrades: number | null;
    tokenTrades: number | null;
    web3Events: number | null;
    campaigns: number | null;
    stakingPools: number | null;
  };
  warnings: AtlasProductStatusWarning[];
  generatedAt: string;
}

export async function getProductStatus(): Promise<AtlasProductStatus> {
  // NB: parse directly (not parseAtlasResponse). This endpoint returns a raw
  // object whose top-level `data` field (scenario counts) would otherwise be
  // mistaken for a {data} envelope and unwrapped, dropping `warnings`.
  const response = await fetchApi('/status/product');
  if (!response.ok) {
    throw new Error(`HTTP ${response.status}`);
  }
  return response.json() as Promise<AtlasProductStatus>;
}
