package txreview

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"net/http/httptest"
	"strings"
	"testing"
)

// Bridge-deposit review tests. Rule tests follow rules_test.go's
// one-Test-per-outcome style via findCheck; orchestration tests run through
// ReviewTransaction with the package's fakes; the wire test proves the
// bridgeDstChainId JSON contract end to end.

const (
	bridgeSrcGW  = "0x2d68a51fb4c3f3ac26fa48b8a457d150132f185d"
	bridgeToken  = "0x57e554d795a18f3ca0a0e9e03a17ac3c509c3bf8"
	bridgeDstTok = "0x2222222222222222222222222222222222222222"
	bridgeOther  = "0x000000000000000000000000000000000000beef"
	bridgeSrcID  = 11155111
	bridgeDstID  = 84532
)

// fakeBridge stubs BridgeStateReader with per-field canned behavior.
type fakeBridge struct {
	gateways map[int]string
	route    BridgeRouteInfo
	routeErr error
	liq      *big.Int
	liqErr   error
	hasPrior bool
	histErr  error
}

func (f *fakeBridge) GatewayAddress(chainID int) (string, bool) {
	gw, ok := f.gateways[chainID]
	return gw, ok
}
func (f *fakeBridge) Route(_ context.Context, _ int, _ string, _ int) (BridgeRouteInfo, error) {
	if f.routeErr != nil {
		return BridgeRouteInfo{}, f.routeErr
	}
	return f.route, nil
}
func (f *fakeBridge) Liquidity(_ context.Context, _ int, _ string) (*big.Int, error) {
	return f.liq, f.liqErr
}
func (f *fakeBridge) SenderHasBridged(_ context.Context, _, _ string) (bool, error) {
	return f.hasPrior, f.histErr
}
func (f *fakeBridge) DepositCalldata(_ string, _ *big.Int, _ int, _ string) string {
	return "0x8b6099db" + strings.Repeat("00", 128)
}

// healthyBridge is a fully-open route: both gateways deployed, route
// configured, liquidity ample, sender has bridged before.
func healthyBridge() *fakeBridge {
	return &fakeBridge{
		gateways: map[int]string{bridgeSrcID: bridgeSrcGW, bridgeDstID: "0x1111111111111111111111111111111111111111"},
		route:    BridgeRouteInfo{Supported: true, DstToken: bridgeDstTok, MinAmount: big.NewInt(1)},
		liq:      big.NewInt(1_000_000),
		hasPrior: true,
	}
}

// healthyState mirrors what prepareBridgeDeposit stages from healthyBridge.
func healthyState() BridgeReviewState {
	return BridgeReviewState{
		DstChainID:       bridgeDstID,
		FromAddress:      smokeAddr,
		Recipient:        smokeAddr,
		Amount:           big.NewInt(1000),
		SrcGateway:       bridgeSrcGW,
		DstGatewayOK:     true,
		RouteRead:        true,
		Supported:        true,
		MinAmount:        big.NewInt(1),
		LiquidityKnown:   true,
		Liquidity:        big.NewInt(1_000_000),
		HistoryKnown:     true,
		HasPriorTransfer: true,
	}
}

func bridgeInput() ReviewInput {
	return ReviewInput{
		OperationType:    OpBridgeDeposit,
		FromAddress:      smokeAddr,
		ChainID:          ptrInt(bridgeSrcID),
		TokenAddress:     ptrStr(bridgeToken),
		Amount:           ptrStr("1000"),
		BridgeDstChainID: ptrInt(bridgeDstID),
	}
}

// ─── EvaluateBridgeRules — one test per outcome ─────────────────────────────

// The trust model is a property of the bridge, not a passable condition — the
// check is a constant warn, which is exactly why a bridge review can never
// come back "approved".
func TestBridgeTrustModelAlwaysWarns(t *testing.T) {
	c := findCheck(EvaluateBridgeRules(healthyState()), "bridge-trust-model")
	if c == nil || c.Status != StatusWarn || c.Severity != SeverityMedium {
		t.Errorf("trust-model must be a constant medium warn: %+v", c)
	}
}

func TestBridgeRouteReadyWhenOpen(t *testing.T) {
	checks := EvaluateBridgeRules(healthyState())
	if c := findCheck(checks, "bridge-route-ready"); c == nil || c.Status != StatusPass {
		t.Errorf("open route must pass: %+v", c)
	}
	if c := findCheck(checks, "bridge-route-unavailable"); c != nil {
		t.Errorf("open route must not also flag unavailable: %+v", c)
	}
}

func TestBridgeRouteUnavailableVariants(t *testing.T) {
	// Each variant is a distinct real-world state with a distinct owner; the
	// summary must name which one the user is looking at.
	variants := map[string]struct {
		mutate  func(*BridgeReviewState)
		mention string
	}{
		"no source gateway":      {func(s *BridgeReviewState) { s.SrcGateway = "" }, "source chain"},
		"no destination gateway": {func(s *BridgeReviewState) { s.DstGatewayOK = false }, "destination chain"},
		"route unreadable":       {func(s *BridgeReviewState) { s.RouteRead = false }, "could not be read"},
		"route not configured":   {func(s *BridgeReviewState) { s.Supported = false }, "not been opened"},
	}
	for name, v := range variants {
		t.Run(name, func(t *testing.T) {
			state := healthyState()
			v.mutate(&state)
			c := findCheck(EvaluateBridgeRules(state), "bridge-route-unavailable")
			if c == nil || c.Status != StatusFail || c.Severity != SeverityCritical {
				t.Fatalf("want critical fail, got %+v", c)
			}
			if !strings.Contains(c.Summary, v.mention) {
				t.Errorf("summary must name the state (%q): %s", v.mention, c.Summary)
			}
		})
	}
}

func TestBridgeUnreadableRouteIsNotCalledUnconfigured(t *testing.T) {
	state := healthyState()
	state.RouteRead = false
	c := findCheck(EvaluateBridgeRules(state), "bridge-route-unavailable")
	if c == nil {
		t.Fatal("missing check")
	}
	// "Could not read" and "not configured" are different claims; asserting
	// the wrong one would be fabricating chain state.
	if strings.Contains(c.Summary, "not been opened") {
		t.Errorf("unreadable route must not claim the route is unconfigured: %s", c.Summary)
	}
}

func TestBridgeGatewayPaused(t *testing.T) {
	state := healthyState()
	state.Paused = true
	c := findCheck(EvaluateBridgeRules(state), "bridge-gateway-paused")
	if c == nil || c.Status != StatusFail || c.Severity != SeverityCritical {
		t.Errorf("paused gateway must be a critical fail: %+v", c)
	}
	if c := findCheck(EvaluateBridgeRules(healthyState()), "bridge-gateway-paused"); c != nil {
		t.Errorf("unpaused gateway must not emit the paused check: %+v", c)
	}
}

func TestBridgeBelowMinimum(t *testing.T) {
	state := healthyState()
	state.MinAmount = big.NewInt(5000) // amount is 1000
	c := findCheck(EvaluateBridgeRules(state), "bridge-below-minimum")
	if c == nil || c.Status != StatusFail || c.Severity != SeverityHigh {
		t.Errorf("below-minimum must be a high fail: %+v", c)
	}
	if c := findCheck(EvaluateBridgeRules(healthyState()), "bridge-below-minimum"); c != nil {
		t.Errorf("at/above minimum must not emit the check: %+v", c)
	}
	zero := healthyState()
	zero.MinAmount = big.NewInt(0)
	if c := findCheck(EvaluateBridgeRules(zero), "bridge-below-minimum"); c != nil {
		t.Errorf("a zero minimum means no minimum: %+v", c)
	}
}

func TestBridgeLiquidityOutcomes(t *testing.T) {
	if c := findCheck(EvaluateBridgeRules(healthyState()), "bridge-destination-liquidity"); c == nil || c.Status != StatusPass {
		t.Errorf("ample liquidity must pass: %+v", c)
	}

	short := healthyState()
	short.Liquidity = big.NewInt(5) // amount is 1000
	if c := findCheck(EvaluateBridgeRules(short), "bridge-liquidity-insufficient"); c == nil || c.Status != StatusFail || c.Severity != SeverityHigh {
		t.Errorf("short liquidity must be a high fail: %+v", c)
	}

	unknown := healthyState()
	unknown.LiquidityKnown = false
	unknown.Liquidity = nil
	c := findCheck(EvaluateBridgeRules(unknown), "bridge-liquidity-unknown")
	if c == nil || c.Status != StatusWarn || c.Severity != SeverityHigh {
		t.Errorf("unknown liquidity must be a high warn, not a guessed zero: %+v", c)
	}
	if !strings.Contains(c.Summary, "not zero") {
		t.Errorf("unknown-liquidity summary must say unknown is not zero: %s", c.Summary)
	}
	// Unknown must NOT surface as insufficient — that would fabricate a zero.
	if c := findCheck(EvaluateBridgeRules(unknown), "bridge-liquidity-insufficient"); c != nil {
		t.Errorf("unknown liquidity must not be reported as insufficient: %+v", c)
	}
}

// EvaluateBridgeRules is exported, so it must survive a hand-built state the
// way EvaluateRules survives nil pointer fields — degrading to "unknown"
// rather than panicking the review request into a 500.
func TestBridgeRulesToleratesIncompleteState(t *testing.T) {
	for name, state := range map[string]BridgeReviewState{
		"liquidity claimed known but nil": {
			SrcGateway: bridgeSrcGW, DstGatewayOK: true, RouteRead: true, Supported: true,
			Amount: big.NewInt(1000), LiquidityKnown: true, Liquidity: nil,
		},
		"amount unset": {
			SrcGateway: bridgeSrcGW, DstGatewayOK: true, RouteRead: true, Supported: true,
			MinAmount: big.NewInt(10), LiquidityKnown: true, Liquidity: big.NewInt(1),
		},
		"zero value": {},
	} {
		t.Run(name, func(t *testing.T) {
			checks := EvaluateBridgeRules(state) // must not panic
			if findCheck(checks, "bridge-trust-model") == nil {
				t.Error("trust-model check must always be present")
			}
			if c := findCheck(checks, "bridge-liquidity-insufficient"); c != nil {
				t.Errorf("incomplete state must not fabricate a shortfall: %+v", c)
			}
		})
	}
}

func TestBridgeLiquiditySkippedWhenRouteClosed(t *testing.T) {
	state := healthyState()
	state.Supported = false
	checks := EvaluateBridgeRules(state)
	for _, id := range []string{"bridge-destination-liquidity", "bridge-liquidity-insufficient", "bridge-liquidity-unknown"} {
		if c := findCheck(checks, id); c != nil {
			t.Errorf("liquidity checks are meaningless on a closed route: %+v", c)
		}
	}
}

func TestBridgeRecipientOutcomes(t *testing.T) {
	if c := findCheck(EvaluateBridgeRules(healthyState()), "bridge-recipient-self"); c == nil || c.Status != StatusPass {
		t.Errorf("self recipient must pass: %+v", c)
	}

	// Case-insensitive: a checksummed recipient of the same account is self.
	mixed := healthyState()
	mixed.Recipient = strings.ToUpper(smokeAddr[:2]) + strings.ToUpper(smokeAddr[2:])
	if c := findCheck(EvaluateBridgeRules(mixed), "bridge-recipient-mismatch"); c != nil {
		t.Errorf("checksum casing must not read as a different recipient: %+v", c)
	}

	other := healthyState()
	other.Recipient = bridgeOther
	if c := findCheck(EvaluateBridgeRules(other), "bridge-recipient-mismatch"); c == nil || c.Status != StatusWarn || c.Severity != SeverityMedium {
		t.Errorf("foreign recipient must be a medium warn: %+v", c)
	}
}

func TestBridgeHistoryOutcomes(t *testing.T) {
	if c := findCheck(EvaluateBridgeRules(healthyState()), "bridge-repeat-transfer"); c == nil || c.Status != StatusPass {
		t.Errorf("prior transfer must pass: %+v", c)
	}

	first := healthyState()
	first.HasPriorTransfer = false
	if c := findCheck(EvaluateBridgeRules(first), "bridge-first-transfer"); c == nil || c.Status != StatusWarn || c.Severity != SeverityLow {
		t.Errorf("first transfer must be a low warn: %+v", c)
	}

	unknown := healthyState()
	unknown.HistoryKnown = false
	if c := findCheck(EvaluateBridgeRules(unknown), "bridge-history-unknown"); c == nil || c.Status != StatusWarn {
		t.Errorf("unreadable history must warn, not silently pass: %+v", c)
	}
}

// ─── prepare validation (hard 400s) ─────────────────────────────────────────

func TestBridgePrepareValidation(t *testing.T) {
	svc := NewService(nil, nil, nil, nil, nil, nil, healthyBridge())

	mutate := map[string]func(*ReviewInput){
		"missing token":     func(in *ReviewInput) { in.TokenAddress = nil },
		"missing amount":    func(in *ReviewInput) { in.Amount = nil },
		"missing dst chain": func(in *ReviewInput) { in.BridgeDstChainID = nil },
		"zero amount":       func(in *ReviewInput) { in.Amount = ptrStr("0") },
		"decimal amount":    func(in *ReviewInput) { in.Amount = ptrStr("1.5") },
		"garbage amount":    func(in *ReviewInput) { in.Amount = ptrStr("lots") },
		"zero dst chain":    func(in *ReviewInput) { in.BridgeDstChainID = ptrInt(0) },
		"same chain":        func(in *ReviewInput) { in.BridgeDstChainID = ptrInt(bridgeSrcID) },
		"bad recipient":     func(in *ReviewInput) { in.Recipient = ptrStr("0xnope") },
	}
	for name, m := range mutate {
		t.Run(name, func(t *testing.T) {
			in := bridgeInput()
			m(&in)
			if _, err := svc.ReviewTransaction(context.Background(), in); !errors.Is(err, ErrInvalidInput) {
				t.Errorf("want ErrInvalidInput, got %v", err)
			}
		})
	}
}

func TestBridgePrepareWithoutReaderHardErrors(t *testing.T) {
	svc := NewService(nil, nil, nil, nil, nil, nil, nil)
	_, err := svc.ReviewTransaction(context.Background(), bridgeInput())
	if err == nil || errors.Is(err, ErrInvalidInput) {
		// Not configured is a server fault (500), not a client error.
		t.Errorf("want a non-ErrInvalidInput error, got %v", err)
	}
}

// ─── orchestration through ReviewTransaction ────────────────────────────────

// A fully healthy bridge deposit is still WARNING, never approved — the trust
// model guarantees at least one warn. This is the load-bearing property of the
// whole review: no AI layer or UI may present a bridge transfer as risk-free.
func TestBridgeReviewIsNeverApproved(t *testing.T) {
	svc := NewService(
		&stubRPCSimple{estimateGas: big.NewInt(21000), gasPrice: big.NewInt(1e9), balance: big.NewInt(1e18)},
		nil, nil, nil, nil, fakeKnown{spenders: []string{bridgeSrcGW}}, healthyBridge(),
	)
	out, err := svc.ReviewTransaction(context.Background(), bridgeInput())
	if err != nil {
		t.Fatalf("ReviewTransaction: %v", err)
	}
	if out.ReviewStatus != ReviewWarning {
		t.Errorf("healthy bridge review must be warning (trust model), got %s", out.ReviewStatus)
	}
	if out.OperationType != OpBridgeDeposit {
		t.Errorf("operationType echo: %s", out.OperationType)
	}
	// The simulated tx must be the REAL deposit the wallet would sign.
	if out.GeneratedTx.To != bridgeSrcGW {
		t.Errorf("generatedTx.to must be the source gateway, got %s", out.GeneratedTx.To)
	}
	if !strings.HasPrefix(out.GeneratedTx.Data, "0x8b6099db") {
		t.Errorf("generatedTx.data must be deposit calldata, got %.20s", out.GeneratedTx.Data)
	}
	if out.GeneratedTx.ChainID != bridgeSrcID {
		t.Errorf("generatedTx.chainId: %d", out.GeneratedTx.ChainID)
	}
	// The gateway is the spender the user must trust, and it is registry-known.
	for _, c := range out.Checks {
		if c.ID == "unknown-spender" {
			t.Errorf("registry-known gateway flagged as unknown spender: %+v", c)
		}
	}
	if c := findCheck(out.Checks, "bridge-trust-model"); c == nil {
		t.Error("trust-model check missing from orchestrated review")
	}
	// The trust model always yields its recommended action.
	found := false
	for _, a := range out.RecommendedActions {
		if strings.Contains(a, "relayer failure") {
			found = true
		}
	}
	if !found {
		t.Errorf("missing trust-model recommended action: %v", out.RecommendedActions)
	}
}

func TestBridgeReviewBlockedOutcomes(t *testing.T) {
	cases := map[string]func(*fakeBridge){
		"paused gateway":  func(f *fakeBridge) { f.route.Paused = true },
		"route unopened":  func(f *fakeBridge) { f.route.Supported = false },
		"short liquidity": func(f *fakeBridge) { f.liq = big.NewInt(1) },
		"no dst gateway":  func(f *fakeBridge) { delete(f.gateways, bridgeDstID) },
		"route unreadable": func(f *fakeBridge) {
			f.routeErr = errors.New("rpc: connection refused")
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			fb := healthyBridge()
			mutate(fb)
			svc := NewService(
				&stubRPCSimple{estimateGas: big.NewInt(21000), gasPrice: big.NewInt(1e9), balance: big.NewInt(1e18)},
				nil, nil, nil, nil, fakeKnown{spenders: []string{bridgeSrcGW}}, fb,
			)
			out, err := svc.ReviewTransaction(context.Background(), bridgeInput())
			if err != nil {
				t.Fatalf("ReviewTransaction: %v", err)
			}
			if out.ReviewStatus != ReviewBlocked {
				t.Errorf("want blocked, got %s (checks: %+v)", out.ReviewStatus, out.Checks)
			}
		})
	}
}

// Unreadable projection state degrades to warnings, never to a block and never
// to a fabricated pass.
func TestBridgeReviewUnknownStateWarns(t *testing.T) {
	fb := healthyBridge()
	fb.liqErr = errors.New("bridge: transfer store unavailable")
	fb.histErr = errors.New("bridge: transfer store unavailable")
	svc := NewService(
		&stubRPCSimple{estimateGas: big.NewInt(21000), gasPrice: big.NewInt(1e9), balance: big.NewInt(1e18)},
		nil, nil, nil, nil, fakeKnown{spenders: []string{bridgeSrcGW}}, fb,
	)
	out, err := svc.ReviewTransaction(context.Background(), bridgeInput())
	if err != nil {
		t.Fatalf("ReviewTransaction: %v", err)
	}
	if out.ReviewStatus != ReviewWarning {
		t.Errorf("unknown liquidity/history must warn, not block or pass: %s", out.ReviewStatus)
	}
	if c := findCheck(out.Checks, "bridge-liquidity-unknown"); c == nil {
		t.Error("missing bridge-liquidity-unknown")
	}
	if c := findCheck(out.Checks, "bridge-history-unknown"); c == nil {
		t.Error("missing bridge-history-unknown")
	}
}

// ─── wire contract ──────────────────────────────────────────────────────────

// bridgeDstChainId must round-trip through the HTTP handler exactly as the
// frontend client sends it.
func TestBridgeReviewHandlerWire(t *testing.T) {
	svc := NewService(
		&stubRPCSimple{estimateGas: big.NewInt(21000), gasPrice: big.NewInt(1e9), balance: big.NewInt(1e18)},
		nil, nil, nil, nil, fakeKnown{spenders: []string{bridgeSrcGW}}, healthyBridge(),
	)
	body := `{"operationType":"bridge-deposit","fromAddress":"` + smokeAddr + `","chainId":11155111,` +
		`"tokenAddress":"` + bridgeToken + `","amount":"1000","bridgeDstChainId":84532}`

	rec := httptest.NewRecorder()
	Router(svc, nil).ServeHTTP(rec, authedReview(body, smokeAddr))

	if rec.Code != 200 {
		t.Fatalf("want 200, got %d (%s)", rec.Code, rec.Body.String())
	}
	var out Result
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.OperationType != OpBridgeDeposit {
		t.Errorf("operationType: %s", out.OperationType)
	}
	if out.ReviewStatus != ReviewWarning {
		t.Errorf("healthy wire review must be warning, got %s", out.ReviewStatus)
	}
	if findCheck(out.Checks, "bridge-trust-model") == nil {
		t.Error("trust-model check missing on the wire")
	}
}
