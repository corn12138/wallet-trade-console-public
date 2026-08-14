// Package txreview is the Go port of legacy NestJS security/
// tx-review.*.ts. Phase 5d.2a delivers the primitives: types, helpers,
// rules engine, and the RPC-backed simulator. Phase 5d.2b adds the
// orchestrating service + HTTP wiring (it ports the per-operation-type
// preparation that depends on swap + earn services).
package txreview

import "math/big"

// OperationType matches the closed set in security.types.ts.
type OperationType string

const (
	OpApprove        OperationType = "approve"
	OpRevokeApproval OperationType = "revoke-approval"
	OpSwap           OperationType = "swap"
	OpEarnDeposit    OperationType = "earn-deposit"
	OpEarnWithdraw   OperationType = "earn-withdraw"
	OpBridgeDeposit  OperationType = "bridge-deposit"
	OpCustom         OperationType = "custom"
)

// CheckSeverity / CheckStatus match TxReviewCheckRecord in NestJS.
type CheckSeverity string

const (
	SeverityInfo     CheckSeverity = "info"
	SeverityLow      CheckSeverity = "low"
	SeverityMedium   CheckSeverity = "medium"
	SeverityHigh     CheckSeverity = "high"
	SeverityCritical CheckSeverity = "critical"
)

type CheckStatus string

const (
	StatusPass CheckStatus = "pass"
	StatusWarn CheckStatus = "warn"
	StatusFail CheckStatus = "fail"
)

// Check mirrors TxReviewCheckRecord. Returned by the rules engine.
type Check struct {
	ID       string        `json:"id"`
	Severity CheckSeverity `json:"severity"`
	Status   CheckStatus   `json:"status"`
	Title    string        `json:"title"`
	Summary  string        `json:"summary"`
}

// ReviewStatus is the top-level outcome of a tx-review.
type ReviewStatus string

const (
	ReviewApproved              ReviewStatus = "approved"
	ReviewWarning               ReviewStatus = "warning"
	ReviewBlocked               ReviewStatus = "blocked"
	ReviewSimulationUnavailable ReviewStatus = "simulation-unavailable"
)

// SimulationMode matches the {simulation.mode} field in the response.
type SimulationMode string

const (
	SimulationModeEstimate SimulationMode = "estimate-gas"
	SimulationModeFallback SimulationMode = "fallback"
)

// SimulationResult is the simulator's output, consumed by the rules
// engine. Pointer-typed *big.Int / *bool fields carry "absent" via
// nil when the RPC call didn't succeed (mirrors NestJS nullable types).
type SimulationResult struct {
	Mode               SimulationMode `json:"mode"`
	GasEstimate        *big.Int       `json:"-"`
	GasEstimateString  *string        `json:"gasEstimate,omitempty"`
	GasLimitSuggestion *string        `json:"gasLimitSuggestion,omitempty"`
	EstimatedFeeWei    *big.Int       `json:"-"`
	EstimatedFeeString *string        `json:"estimatedFeeWei,omitempty"`
	CallSucceeded      *bool          `json:"callSucceeded,omitempty"`
	ErrorMessage       *string        `json:"errorMessage,omitempty"`
	NativeBalanceWei   *big.Int       `json:"-"` // not serialized, fed to rules
}

// AllowanceChange mirrors TxReviewResult.allowanceChange.
type AllowanceChange struct {
	TokenAddress       string  `json:"tokenAddress"`
	Spender            string  `json:"spender"`
	CurrentAllowance   *string `json:"currentAllowance"`
	RequestedAllowance *string `json:"requestedAllowance"`
	IsUnlimited        bool    `json:"isUnlimited"`
}

// ConnectedSiteSummary is the slim ConnectedSite shape included in the
// response (the rules engine also looks at riskLevel).
type ConnectedSiteSummary struct {
	ID        string `json:"id"`
	Origin    string `json:"origin"`
	SiteName  string `json:"siteName"`
	RiskLevel string `json:"riskLevel"`
}

// TxShape is the canonical {chainId, to, value, data} carried by every
// generated tx + the custom-input pass-through.
type TxShape struct {
	ChainID int    `json:"chainId"`
	To      string `json:"to"`
	Value   string `json:"value"`
	Data    string `json:"data"`
}

// ReviewInput matches ReviewSecurityTransactionInput from NestJS. All
// optional fields use pointer types so the JSON decoder can tell
// "omitted" apart from "zero value".
type ReviewInput struct {
	OperationType    OperationType `json:"operationType,omitempty"`
	FromAddress      string        `json:"fromAddress"`
	ChainID          *int          `json:"chainId,omitempty"`
	SiteOrigin       *string       `json:"siteOrigin,omitempty"`
	NativeBalanceWei *string       `json:"nativeBalanceWei,omitempty"`
	TokenAddress     *string       `json:"tokenAddress,omitempty"`
	Spender          *string       `json:"spender,omitempty"`
	Amount           *string       `json:"amount,omitempty"`
	TokenDecimals    *int          `json:"tokenDecimals,omitempty"`
	TokenIn          *string       `json:"tokenIn,omitempty"`
	TokenOut         *string       `json:"tokenOut,omitempty"`
	TokenInDecimals  *int          `json:"tokenInDecimals,omitempty"`
	TokenOutDecimals *int          `json:"tokenOutDecimals,omitempty"`
	AmountOutMin     *string       `json:"amountOutMin,omitempty"`
	Recipient        *string       `json:"recipient,omitempty"`
	BridgeDstChainID *int          `json:"bridgeDstChainId,omitempty"`
	DeadlineSeconds  *int          `json:"deadlineSeconds,omitempty"`
	SlippageBps      *int          `json:"slippageBps,omitempty"`
	QuoteExpiresAt   *string       `json:"quoteExpiresAt,omitempty"`
	ProductID        *string       `json:"productId,omitempty"`
	Tx               *TxShape      `json:"tx,omitempty"`
}

// ReviewContext is the staged result of preparation. The orchestrator
// (Phase 5d.2b) populates this from per-operation handlers and feeds
// it to the simulator + rules engine. Exported here so the
// orchestrator package (security or txreview itself in 5d.2b) can
// build it.
type ReviewContext struct {
	OperationType    OperationType
	ChainID          int
	FromAddress      string
	SiteOrigin       string
	Spender          string
	QuoteExpiresAt   string
	SlippageBps      *int
	AllowanceRequest *AllowanceRequest
	TokenAddresses   []string
	TokenWarnings    []string
	GeneratedTx      TxShape
	NativeBalanceHex string // optional pre-supplied balance from the FE
	// Bridge is set only for bridge-deposit reviews: the staged on-chain +
	// projection observations that EvaluateBridgeRules judges.
	Bridge *BridgeReviewState
}

// AllowanceRequest is the prepared allowance side-input (subset of
// AllowanceChange — RequestedAllowance is the original raw int rather
// than its string projection).
type AllowanceRequest struct {
	TokenAddress       string
	Spender            string
	RequestedAllowance *big.Int
	IsUnlimited        bool
}

// Result mirrors TxReviewResult.
type Result struct {
	ReviewStatus       ReviewStatus          `json:"reviewStatus"`
	OperationType      OperationType         `json:"operationType"`
	ChainID            int                   `json:"chainId"`
	RiskScore          int                   `json:"riskScore"`
	Simulation         SimulationResult      `json:"simulation"`
	GeneratedTx        TxShape               `json:"generatedTx"`
	AllowanceChange    *AllowanceChange      `json:"allowanceChange"`
	ConnectedSite      *ConnectedSiteSummary `json:"connectedSite"`
	RecommendedActions []string              `json:"recommendedActions"`
	Checks             []Check               `json:"checks"`
}
