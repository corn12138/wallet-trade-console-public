/**
 * @file Bridge API client (GET/POST /api/bridge).
 *
 * Every field here reflects REAL on-chain state read by the Go service from the
 * deployed BridgeGateway contracts — route availability, destination liquidity,
 * pause state — plus the bridge_transfers projection, which is written only
 * from observed on-chain events.
 *
 * TRUST MODEL. This is a trusted-relayer bridge. `trustModel` is on every
 * routes response and is NOT optional decoration: a client that cannot render
 * it must not offer an execution control. See BridgeGateway.sol.
 */

import { buildApiUrl } from './base-url';

/** Machine-readable reasons a route cannot execute. Never render a raw code. */
export type BridgeBlocker =
  | 'SAME_CHAIN'
  | 'SRC_TOKEN_REQUIRED'
  | 'NO_SOURCE_GATEWAY'
  | 'NO_DESTINATION_GATEWAY'
  | 'SOURCE_RPC_UNAVAILABLE'
  | 'ROUTE_READ_FAILED'
  | 'ROUTE_NOT_CONFIGURED'
  | 'GATEWAY_PAUSED'
  | 'BELOW_MIN_AMOUNT'
  | 'INSUFFICIENT_DESTINATION_LIQUIDITY'
  | 'DESTINATION_LIQUIDITY_UNKNOWN';

export type BridgeTransferStatus = 'INITIATED' | 'FULFILLED' | 'REFUNDED';

export interface BridgeRoute {
  id: string;
  fromChainId: number;
  toChainId: number;
  tokenSymbol: string;
  srcToken: string;
  dstToken: string;
  srcGateway: string;
  dstGateway: string;
  amountIn: string;
  /** Equals amountIn — lock-and-release takes no protocol fee. */
  amountOut: string;
  minAmount: string;
  executable: boolean;
  /** Empty iff executable. Every reason, not just the first. */
  blockers: BridgeBlocker[];
  /** Destination gateway balance in base units, or "" when it could not be read. */
  dstLiquidity: string;
  paused: boolean;
  trustModel: string;
}

export interface BridgeRoutesResponse {
  requestedAt: string;
  executionMode: 'live' | 'unavailable';
  executable: boolean;
  trustModel: string;
  /** Chains with a deployed gateway. */
  chains: number[];
  routes: BridgeRoute[];
}

export interface BridgeBuildDepositResponse {
  chainId: number;
  to: string;
  data: string;
  value: string;
  approvalTarget: string;
  trustModel: string;
}

export interface BridgeTransfer {
  transferId: string;
  status: BridgeTransferStatus;
  srcChainId: number;
  dstChainId: number;
  sender: string;
  recipient: string;
  srcToken: string;
  dstToken: string;
  amount: string;
  depositTxHash: string;
  depositedAt: string;
  fulfillTxHash?: string;
  fulfilledAt: string | null;
  refundTxHash?: string;
  refundedAt: string | null;
  /** Verbatim relayer failure — a stuck transfer must explain itself. */
  lastError?: string;
  attempts: number;
  updatedAt: string;
}

/**
 * Thrown when the server refuses to build a deposit because the route is not
 * currently executable (HTTP 409). This is a real, expected outcome — liquidity
 * can drop between the routes poll and the click — so callers should re-read
 * routes and show the fresh blockers rather than treating it as a crash.
 */
export class BridgeRouteUnavailableError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'BridgeRouteUnavailableError';
  }
}

export interface BridgeRoutesRequest {
  fromChainId: number;
  toChainId: number;
  tokenSymbol: string;
  srcToken: string;
  /** Base units, integer string — no floats ever touch a transfer amount. */
  amount: string;
}

export async function getBridgeRoutes(req: BridgeRoutesRequest): Promise<BridgeRoutesResponse> {
  const response = await fetch(buildApiUrl('/bridge/routes'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(req),
  });
  if (!response.ok) {
    throw new Error(`Failed to resolve bridge routes (HTTP ${response.status})`);
  }
  return (await response.json()) as BridgeRoutesResponse;
}

export interface BridgeBuildDepositRequest {
  fromChainId: number;
  toChainId: number;
  srcToken: string;
  amount: string;
  recipient: string;
}

/**
 * Server-side executability re-check immediately before signing.
 *
 * The page does NOT send this calldata — it encodes the call from the
 * BridgeGateway ABI via useTxFlow, matching how SwapPage uses routerAbi. What
 * this call buys is a fresh server-side verdict at click time: the routes poll
 * is up to 30s stale, and destination liquidity can drop in that window. A 409
 * here stops a transaction that would revert on-chain.
 */
export async function buildBridgeDeposit(
  req: BridgeBuildDepositRequest,
): Promise<BridgeBuildDepositResponse> {
  const response = await fetch(buildApiUrl('/bridge/build-deposit'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(req),
  });
  if (response.status === 409) {
    throw new BridgeRouteUnavailableError('bridge route is no longer executable');
  }
  if (!response.ok) {
    throw new Error(`Failed to build bridge deposit (HTTP ${response.status})`);
  }
  return (await response.json()) as BridgeBuildDepositResponse;
}

/** A wallet's transfers, newest first. */
export async function getBridgeTransfers(address: string, limit = 20): Promise<BridgeTransfer[]> {
  const response = await fetch(
    buildApiUrl(`/bridge/transfers?address=${encodeURIComponent(address)}&limit=${limit}`),
  );
  if (!response.ok) {
    throw new Error(`Failed to list bridge transfers (HTTP ${response.status})`);
  }
  return (await response.json()) as BridgeTransfer[];
}

/**
 * One transfer by its canonical id.
 *
 * 503 means the transfer store could not be read — deliberately distinct from
 * 404 ("no such transfer"). Callers must not collapse the two.
 */
export async function getBridgeStatus(transferId: string): Promise<BridgeTransfer | null> {
  const response = await fetch(buildApiUrl(`/bridge/status/${encodeURIComponent(transferId)}`));
  if (response.status === 404) {
    return null;
  }
  if (!response.ok) {
    throw new Error(`Failed to read bridge status (HTTP ${response.status})`);
  }
  return (await response.json()) as BridgeTransfer;
}
