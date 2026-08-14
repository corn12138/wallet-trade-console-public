package txreview

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
)

// Bridge-deposit review (TD 2026-08-09, P1).
//
// Every check in this file is DETERMINISTIC: on-chain reads (route
// configuration, pause state, destination liquidity), a projection read
// (sender history), and pure comparisons. Nothing here consults a model, and
// nothing downstream may treat a model's output as overriding these checks —
// the AI layer (P2) only rephrases what this file has already decided.
//
// The dependency is a txreview-owned interface with a cmd/api adapter, same as
// SwapPreparer/EarnPreparer: txreview must not import internal/bridge (the
// convention that keeps this package cycle-free and fake-testable).

// BridgeRouteInfo mirrors the source gateway's on-chain route() + paused()
// reads. MinAmount nil means the route carries no minimum.
type BridgeRouteInfo struct {
	Supported bool
	DstToken  string
	MinAmount *big.Int
	Paused    bool
}

// BridgeStateReader is everything the bridge-deposit review needs to observe.
// Every method reports real state; an error means "could not be read", which
// the rules render as unknown/unverifiable — never as a guessed value.
type BridgeStateReader interface {
	// GatewayAddress returns the deployed gateway for a chain, if any.
	GatewayAddress(chainID int) (string, bool)
	// Route reads route(srcToken, dstChainID) + paused() on the SOURCE gateway.
	Route(ctx context.Context, srcChainID int, srcToken string, dstChainID int) (BridgeRouteInfo, error)
	// Liquidity reads availableLiquidity(dstToken) on the DESTINATION gateway.
	Liquidity(ctx context.Context, dstChainID int, dstToken string) (*big.Int, error)
	// SenderHasBridged reports whether sender has a recorded transfer through
	// srcGateway in the bridge_transfers projection.
	SenderHasBridged(ctx context.Context, sender, srcGateway string) (bool, error)
	// DepositCalldata encodes deposit(srcToken, amount, dstChainID, recipient)
	// so the simulator can estimate the REAL transaction the wallet would sign.
	DepositCalldata(srcToken string, amount *big.Int, dstChainID int, recipient string) string
}

// BridgeReviewState is the staged observation set EvaluateBridgeRules judges.
// The Known/Read booleans keep "could not be read" distinct from real
// negatives — collapsing them is exactly the dishonesty this product bans.
type BridgeReviewState struct {
	DstChainID  int
	FromAddress string
	Recipient   string
	Amount      *big.Int

	SrcGateway   string // "" = no gateway deployed on the source chain
	DstGatewayOK bool

	RouteRead bool // false = route()/paused() could not be read on-chain
	Supported bool
	MinAmount *big.Int
	Paused    bool

	LiquidityKnown bool
	Liquidity      *big.Int

	HistoryKnown     bool
	HasPriorTransfer bool
}

// prepareBridgeDeposit stages a bridge-deposit review. Malformed input is a
// hard ErrInvalidInput (→400) per the prepare* contract; unreadable CHAIN
// state is not an error — it is recorded on BridgeReviewState and judged by
// the rules, matching how Simulate failures surface as checks.
func (s *Service) prepareBridgeDeposit(ctx context.Context, in ReviewInput, base ReviewContext) (ReviewContext, error) {
	if s.bridge == nil {
		return ReviewContext{}, errors.New("txreview: bridge reader not configured")
	}
	if in.TokenAddress == nil || in.Amount == nil || in.BridgeDstChainID == nil {
		return ReviewContext{}, fmt.Errorf("%w: bridge-deposit requires tokenAddress, amount, bridgeDstChainId", ErrInvalidInput)
	}
	srcToken, err := ValidateAddress(*in.TokenAddress, "token address")
	if err != nil {
		return ReviewContext{}, err
	}
	amount := ParseOptionalBigInt(*in.Amount)
	if amount == nil || amount.Sign() <= 0 {
		return ReviewContext{}, fmt.Errorf("%w: amount must be a positive base-unit integer", ErrInvalidInput)
	}
	dstChainID := *in.BridgeDstChainID
	if dstChainID <= 0 {
		return ReviewContext{}, fmt.Errorf("%w: bridgeDstChainId must be a positive chain id", ErrInvalidInput)
	}
	if dstChainID == base.ChainID {
		// The contract hard-reverts SameChain; reviewing it is meaningless.
		return ReviewContext{}, fmt.Errorf("%w: source and destination chains are the same", ErrInvalidInput)
	}
	recipient := base.FromAddress
	if in.Recipient != nil && *in.Recipient != "" {
		recipient, err = ValidateAddress(*in.Recipient, "recipient")
		if err != nil {
			return ReviewContext{}, err
		}
	}

	state := &BridgeReviewState{
		DstChainID:  dstChainID,
		FromAddress: base.FromAddress,
		Recipient:   recipient,
		Amount:      amount,
	}

	if gw, ok := s.bridge.GatewayAddress(base.ChainID); ok && gw != "" {
		state.SrcGateway = gw
	}
	_, state.DstGatewayOK = s.bridge.GatewayAddress(dstChainID)

	if state.SrcGateway != "" {
		if info, routeErr := s.bridge.Route(ctx, base.ChainID, srcToken, dstChainID); routeErr == nil {
			state.RouteRead = true
			state.Supported = info.Supported
			state.MinAmount = info.MinAmount
			state.Paused = info.Paused
			// Liquidity is only meaningful once the route names its
			// destination token on a deployed destination gateway.
			if state.DstGatewayOK && info.Supported && info.DstToken != "" {
				if liq, liqErr := s.bridge.Liquidity(ctx, dstChainID, info.DstToken); liqErr == nil && liq != nil {
					state.LiquidityKnown = true
					state.Liquidity = liq
				}
			}
		}
		if has, histErr := s.bridge.SenderHasBridged(ctx, base.FromAddress, state.SrcGateway); histErr == nil {
			state.HistoryKnown = true
			state.HasPriorTransfer = has
		}

		// The real transaction the wallet would sign — so Simulate estimates
		// the actual deposit (a revert from a missing ERC20 approval surfaces
		// the same way it does for swap).
		base.GeneratedTx = TxShape{
			ChainID: base.ChainID,
			To:      state.SrcGateway,
			Value:   "0",
			Data:    s.bridge.DepositCalldata(srcToken, amount, dstChainID, recipient),
		}
		// The gateway pulls the token via transferFrom, so it IS the spender
		// the user must trust. It is registered in the deployments registry,
		// so the known-spenders rule recognizes our own gateway; a gateway
		// missing from the registry correctly flags as unknown.
		base.Spender = state.SrcGateway
	} else {
		base.GeneratedTx = TxShape{ChainID: base.ChainID}
	}

	base.TokenAddresses = []string{srcToken}
	base.Bridge = state
	return base, nil
}

// EvaluateBridgeRules turns the staged bridge state into checks. Pure and
// deterministic, mirroring EvaluateRules. IDs follow the package convention of
// one kebab-case ID per OUTCOME (quote-valid vs quote-expired), because
// BuildRecommendedActions and the FE key on IDs alone.
func EvaluateBridgeRules(state BridgeReviewState) []Check {
	checks := []Check{{
		// Constant by design: the trust model is a property of this bridge,
		// not a condition that can pass. A bridge review is never "approved"
		// wholesale — the user is always trusting an operator-held key.
		ID:       "bridge-trust-model",
		Severity: SeverityMedium,
		Status:   StatusWarn,
		Title:    "Trusted-relayer bridge",
		Summary:  "This bridge is trusted-relayer, not proof-based: a compromised relayer can drain destination liquidity, an offline relayer strands deposits until an admin refund, and the admin can withdraw escrowed funds.",
	}}

	routeOpen := false
	switch {
	case state.SrcGateway == "":
		checks = append(checks, Check{
			ID: "bridge-route-unavailable", Severity: SeverityCritical, Status: StatusFail,
			Title:   "No source gateway",
			Summary: "No bridge gateway is deployed on the source chain, so this deposit cannot exist.",
		})
	case !state.DstGatewayOK:
		checks = append(checks, Check{
			ID: "bridge-route-unavailable", Severity: SeverityCritical, Status: StatusFail,
			Title:   "No destination gateway",
			Summary: "No bridge gateway is deployed on the destination chain yet, so this route does not exist and nothing could deliver the funds.",
		})
	case !state.RouteRead:
		checks = append(checks, Check{
			ID: "bridge-route-unavailable", Severity: SeverityCritical, Status: StatusFail,
			Title:   "Route state unreadable",
			Summary: "The on-chain route could not be read. Unreadable is not the same as unconfigured — but the deposit cannot be verified safe, so do not send.",
		})
	case !state.Supported:
		checks = append(checks, Check{
			ID: "bridge-route-unavailable", Severity: SeverityCritical, Status: StatusFail,
			Title:   "Route not configured",
			Summary: "The gateway is deployed but this token route has not been opened on-chain; the deposit would revert.",
		})
	default:
		routeOpen = true
		checks = append(checks, Check{
			ID: "bridge-route-ready", Severity: SeverityInfo, Status: StatusPass,
			Title:   "Route is configured",
			Summary: "The on-chain route for this token and destination chain is open on the source gateway.",
		})
	}

	if state.RouteRead && state.Paused {
		checks = append(checks, Check{
			ID: "bridge-gateway-paused", Severity: SeverityCritical, Status: StatusFail,
			Title:   "Gateway paused",
			Summary: "The source gateway is paused by its admin and rejects new deposits.",
		})
	}

	if state.RouteRead && state.Supported && state.MinAmount != nil && state.MinAmount.Sign() > 0 &&
		state.Amount != nil && state.Amount.Cmp(state.MinAmount) < 0 {
		checks = append(checks, Check{
			ID: "bridge-below-minimum", Severity: SeverityHigh, Status: StatusFail,
			Title:   "Below route minimum",
			Summary: fmt.Sprintf("The amount is below the route minimum of %s base units; the deposit would revert.", state.MinAmount),
		})
	}

	if routeOpen {
		// Exported rule evaluators tolerate incomplete input (same contract as
		// EvaluateRules' pointer fields): an unset amount or liquidity is
		// "cannot compare", which is unknown — never a fabricated shortfall.
		comparable := state.LiquidityKnown && state.Liquidity != nil && state.Amount != nil
		switch {
		case !comparable:
			checks = append(checks, Check{
				ID: "bridge-liquidity-unknown", Severity: SeverityHigh, Status: StatusWarn,
				Title:   "Destination liquidity unknown",
				Summary: "The destination gateway's liquidity could not be read. Unknown is not zero — but delivery cannot be verified, so confirm liquidity before depositing.",
			})
		case state.Liquidity.Cmp(state.Amount) < 0:
			checks = append(checks, Check{
				ID: "bridge-liquidity-insufficient", Severity: SeverityHigh, Status: StatusFail,
				Title:   "Insufficient destination liquidity",
				Summary: fmt.Sprintf("The destination gateway holds %s base units against a %s base-unit transfer; delivery would revert until the operator adds liquidity.", state.Liquidity, state.Amount),
			})
		default:
			checks = append(checks, Check{
				ID: "bridge-destination-liquidity", Severity: SeverityInfo, Status: StatusPass,
				Title:   "Destination liquidity covers this transfer",
				Summary: fmt.Sprintf("The destination gateway holds %s base units.", state.Liquidity),
			})
		}
	}

	if strings.EqualFold(state.Recipient, state.FromAddress) {
		checks = append(checks, Check{
			ID: "bridge-recipient-self", Severity: SeverityInfo, Status: StatusPass,
			Title:   "Recipient is the sending wallet",
			Summary: "Funds will be delivered to your own address on the destination chain.",
		})
	} else {
		checks = append(checks, Check{
			ID: "bridge-recipient-mismatch", Severity: SeverityMedium, Status: StatusWarn,
			Title:   "Recipient is a different address",
			Summary: "The recipient is not the sending wallet. A mistyped recipient delivers funds to a stranger on another chain — verify it before signing.",
		})
	}

	if state.SrcGateway != "" {
		switch {
		case !state.HistoryKnown:
			checks = append(checks, Check{
				ID: "bridge-history-unknown", Severity: SeverityLow, Status: StatusWarn,
				Title:   "Transfer history unavailable",
				Summary: "Past transfers through this gateway could not be read; treat this as a first transfer and consider a small test amount.",
			})
		case !state.HasPriorTransfer:
			checks = append(checks, Check{
				ID: "bridge-first-transfer", Severity: SeverityLow, Status: StatusWarn,
				Title:   "First transfer through this gateway",
				Summary: "This wallet has not bridged through this gateway before. Consider a small test amount first.",
			})
		default:
			checks = append(checks, Check{
				ID: "bridge-repeat-transfer", Severity: SeverityInfo, Status: StatusPass,
				Title:   "Gateway used before",
				Summary: "This wallet has completed transfers through this gateway before.",
			})
		}
	}

	return checks
}
