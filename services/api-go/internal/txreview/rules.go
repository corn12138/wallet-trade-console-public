package txreview

import (
	"math/big"
	"strings"
	"time"
)

// Rule thresholds match the NestJS constants.
const (
	HighSlippageBps     = 100
	CriticalSlippageBps = 500
)

// RulesInput is the snapshot the rules engine evaluates. Mirrors
// EvaluateRulesInput in NestJS; pointer types convey "absent".
type RulesInput struct {
	OperationType    OperationType
	Spender          string // "" = no spender
	KnownSpenders    []string
	IsSupportedToken *bool // nil = unknown
	TokenWarnings    []string
	SlippageBps      *int
	QuoteExpiresAt   string // "" = no constraint
	NativeBalanceWei *big.Int
	EstimatedFeeWei  *big.Int
	AllowanceRequest *AllowanceRequest

	SimulationError string // raw NestJS msg used for the insufficient-funds substring check
	Now             time.Time
}

// EvaluateRules runs the 6 rule families and returns the Checks. Same
// ordering as the NestJS engine so the FE renders the same sequence
// (the FE doesn't care about order but tests pin it for stability).
func EvaluateRules(in RulesInput) []Check {
	if in.Now.IsZero() {
		in.Now = time.Now()
	}
	out := make([]Check, 0, 6)
	if c := approvalRule(in); c != nil {
		out = append(out, *c)
	}
	if c := spenderRule(in); c != nil {
		out = append(out, *c)
	}
	if c := quoteRule(in); c != nil {
		out = append(out, *c)
	}
	if c := slippageRule(in); c != nil {
		out = append(out, *c)
	}
	if c := gasRule(in); c != nil {
		out = append(out, *c)
	}
	if c := tokenRule(in); c != nil {
		out = append(out, *c)
	}
	return out
}

func approvalRule(in RulesInput) *Check {
	if in.AllowanceRequest == nil {
		return nil
	}
	if in.AllowanceRequest.IsUnlimited {
		return &Check{
			ID: "max-approval", Severity: SeverityHigh, Status: StatusWarn,
			Title:   "Unlimited approval requested",
			Summary: "This transaction grants a spender an effectively uncapped token allowance.",
		}
	}
	if in.AllowanceRequest.RequestedAllowance != nil &&
		in.AllowanceRequest.RequestedAllowance.Cmp(LargeApprovalThreshold) >= 0 {
		return &Check{
			ID: "large-approval", Severity: SeverityMedium, Status: StatusWarn,
			Title:   "Large approval requested",
			Summary: "The requested allowance is unusually large and should be justified before signing.",
		}
	}
	return &Check{
		ID: "approval-size", Severity: SeverityInfo, Status: StatusPass,
		Title:   "Approval size is bounded",
		Summary: "No max-approval pattern was detected in the requested allowance.",
	}
}

func spenderRule(in RulesInput) *Check {
	if in.Spender == "" {
		return &Check{
			ID: "spender-scope", Severity: SeverityInfo, Status: StatusPass,
			Title:   "No external spender involved",
			Summary: "This transaction does not introduce a new third-party token spender.",
		}
	}
	want := strings.ToLower(in.Spender)
	for _, k := range in.KnownSpenders {
		if k == want {
			return &Check{
				ID: "spender-known", Severity: SeverityInfo, Status: StatusPass,
				Title:   "Known spender",
				Summary: "The spender matches a known protocol contract or tracked workflow.",
			}
		}
	}
	return &Check{
		ID: "unknown-spender", Severity: SeverityMedium, Status: StatusWarn,
		Title:   "Unknown spender",
		Summary: "The spender is not in the current known contract list for this chain or workflow.",
	}
}

func quoteRule(in RulesInput) *Check {
	if in.QuoteExpiresAt == "" {
		return &Check{
			ID: "quote-presence", Severity: SeverityInfo, Status: StatusPass,
			Title:   "No quote expiry constraint",
			Summary: "No quote expiry window was provided for this review.",
		}
	}
	parsedOK, passed := IsoExpiryInPast(in.QuoteExpiresAt, in.Now)
	if !parsedOK {
		return &Check{
			ID: "quote-invalid", Severity: SeverityLow, Status: StatusWarn,
			Title:   "Quote expiry is malformed",
			Summary: "The quote expiry timestamp could not be parsed reliably.",
		}
	}
	if passed {
		return &Check{
			ID: "quote-expired", Severity: SeverityHigh, Status: StatusFail,
			Title:   "Quote expired",
			Summary: "The quote expiry window has already passed. Refresh before signing.",
		}
	}
	return &Check{
		ID: "quote-valid", Severity: SeverityInfo, Status: StatusPass,
		Title:   "Quote still valid",
		Summary: "The quote expiry window has not elapsed yet.",
	}
}

func slippageRule(in RulesInput) *Check {
	if in.SlippageBps == nil {
		return &Check{
			ID: "slippage-unknown", Severity: SeverityInfo, Status: StatusPass,
			Title:   "No slippage guard provided",
			Summary: "Slippage was not part of this review payload.",
		}
	}
	v := *in.SlippageBps
	if v >= CriticalSlippageBps {
		return &Check{
			ID: "high-slippage-critical", Severity: SeverityHigh, Status: StatusFail,
			Title:   "High slippage",
			Summary: "Slippage is set to " + FormatBps(v) + ", which exceeds the critical threshold.",
		}
	}
	if v >= HighSlippageBps {
		return &Check{
			ID: "high-slippage", Severity: SeverityMedium, Status: StatusWarn,
			Title:   "Elevated slippage",
			Summary: "Slippage is set to " + FormatBps(v) + ", which is higher than the normal safe band.",
		}
	}
	return &Check{
		ID: "slippage-ok", Severity: SeverityInfo, Status: StatusPass,
		Title:   "Slippage within guardrail",
		Summary: "Configured slippage " + FormatBps(v) + " stays inside the normal execution range.",
	}
}

func gasRule(in RulesInput) *Check {
	if strings.Contains(strings.ToLower(in.SimulationError), "insufficient funds") {
		return &Check{
			ID: "insufficient-gas", Severity: SeverityHigh, Status: StatusFail,
			Title:   "Insufficient gas balance",
			Summary: "The RPC simulation reports insufficient native balance to cover execution.",
		}
	}
	if in.NativeBalanceWei != nil && in.EstimatedFeeWei != nil &&
		in.NativeBalanceWei.Cmp(in.EstimatedFeeWei) < 0 {
		return &Check{
			ID: "gas-balance-low", Severity: SeverityMedium, Status: StatusWarn,
			Title:   "Native balance may be too low",
			Summary: "The supplied native balance is below the estimated execution fee.",
		}
	}
	if in.EstimatedFeeWei != nil && in.EstimatedFeeWei.Sign() > 0 {
		return &Check{
			ID: "gas-estimated", Severity: SeverityInfo, Status: StatusPass,
			Title:   "Gas estimate available",
			Summary: "Gas estimation completed and no insufficient-funds signal was detected.",
		}
	}
	return &Check{
		ID: "gas-unavailable", Severity: SeverityLow, Status: StatusWarn,
		Title:   "Gas estimate unavailable",
		Summary: "Execution falls back to an unverified gas preview because the RPC estimate could not be completed.",
	}
}

func tokenRule(in RulesInput) *Check {
	if in.IsSupportedToken != nil && !*in.IsSupportedToken {
		return &Check{
			ID: "unsupported-token", Severity: SeverityHigh, Status: StatusFail,
			Title:   "Unsupported token",
			Summary: "The transaction includes at least one token that is not in the supported token set.",
		}
	}
	if len(in.TokenWarnings) > 0 {
		return &Check{
			ID: "token-warning", Severity: SeverityMedium, Status: StatusWarn,
			Title:   "Suspicious token warning",
			Summary: strings.Join(in.TokenWarnings, " · "),
		}
	}
	return &Check{
		ID: "token-check", Severity: SeverityInfo, Status: StatusPass,
		Title:   "No token risk flags",
		Summary: "No unsupported-token or suspicious-token flags were raised by the review input.",
	}
}

// BuildRecommendedActions ports the NestJS buildRecommendedActions.
// Preserves insertion order while deduping.
func BuildRecommendedActions(checks []Check, site *ConnectedSiteSummary, mode SimulationMode, permissions []string) []string {
	seen := map[string]bool{}
	out := []string{}
	add := func(s string) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, c := range checks {
		switch c.ID {
		case "max-approval", "large-approval":
			add("Reduce allowance size before signing.")
		case "unknown-spender":
			add("Verify spender ownership and contract source.")
		case "quote-expired", "quote-invalid":
			add("Refresh the quote before submitting this transaction.")
		case "high-slippage", "high-slippage-critical":
			add("Tighten slippage or reduce trade size.")
		case "insufficient-gas", "gas-balance-low":
			add("Top up native gas balance before signing.")
		case "unsupported-token", "token-warning":
			add("Verify token contract and metadata manually.")
		case "connected-site-risk":
			add("Review the connected site entry and disconnect if unnecessary.")
		case "bridge-trust-model":
			add("Only bridge amounts you can afford to lose to a relayer failure.")
		case "bridge-route-unavailable", "bridge-gateway-paused":
			add("Do not send — the deposit would revert or strand funds.")
		case "bridge-below-minimum":
			add("Increase the amount to at least the route minimum.")
		case "bridge-liquidity-insufficient":
			add("Wait for the operator to add destination liquidity before depositing.")
		case "bridge-liquidity-unknown":
			add("Confirm destination liquidity before depositing.")
		case "bridge-recipient-mismatch":
			add("Double-check the recipient address on the destination chain.")
		case "bridge-first-transfer", "bridge-history-unknown":
			add("Send a small test amount before bridging the full amount.")
		}
	}
	if mode == SimulationModeFallback {
		add("Treat the gas preview as provisional because RPC simulation was unavailable.")
	}
	if site != nil && len(permissions) > 0 {
		add("Connected site permissions: " + strings.Join(permissions, ", ") + ".")
	}
	return out
}
