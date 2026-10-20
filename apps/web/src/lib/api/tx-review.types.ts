export type SimulationObservationKind = 'call' | 'gas' | 'balance' | 'allowance' | 'feeQuote';
export type SimulationObservationStatus = 'succeeded' | 'reverted' | 'unavailable' | 'unsupported' | 'not-required';
export type SimulationPinning = 'hash-pinned' | 'number-pinned' | 'latest-only' | 'unavailable';
export interface SimulationObservation {
  observationStatus: SimulationObservationStatus;
  source: 'rpc' | 'not-requested';
  blockReference?: { number: string; hash: string; pinning: 'hash-pinned' | 'number-pinned' };
  observedAt: string;
  pinning: SimulationPinning;
  reason?: string;
  tokenAddress?: string;
  spender?: string;
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
    simulationScope?: 'full-envelope' | 'legacy-read-call' | 'unavailable';
    consistentSnapshot?: boolean;
    observations?: Partial<Record<SimulationObservationKind, SimulationObservation>>;
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
  connectedSite: { id: string; origin: string; siteName: string; riskLevel: string } | null;
  recommendedActions: string[];
  checks: AtlasTxReviewCheck[];
}
