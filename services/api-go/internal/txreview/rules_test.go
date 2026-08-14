package txreview

import (
	"math/big"
	"testing"
	"time"
)

// findCheck returns the first check with the given ID (nil if none).
func findCheck(checks []Check, id string) *Check {
	for i := range checks {
		if checks[i].ID == id {
			return &checks[i]
		}
	}
	return nil
}

func TestEvaluateRules_AllPassWhenInputsClean(t *testing.T) {
	bps := 50
	checks := EvaluateRules(RulesInput{
		Now:             time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC),
		SlippageBps:     &bps,
		EstimatedFeeWei: big.NewInt(21_000 * 1_000_000_000),
	})
	for _, c := range checks {
		if c.Status != StatusPass {
			t.Errorf("expected all pass, got %s for %q", c.Status, c.ID)
		}
	}
	if findCheck(checks, "spender-scope") == nil {
		t.Errorf("missing spender-scope check")
	}
	if findCheck(checks, "gas-estimated") == nil {
		t.Errorf("missing gas-estimated check")
	}
}

func TestEvaluateRules_MaxApprovalFlags(t *testing.T) {
	checks := EvaluateRules(RulesInput{
		Now:              time.Now(),
		AllowanceRequest: &AllowanceRequest{IsUnlimited: true},
		EstimatedFeeWei:  big.NewInt(1),
	})
	c := findCheck(checks, "max-approval")
	if c == nil || c.Status != StatusWarn || c.Severity != SeverityHigh {
		t.Errorf("max-approval check = %+v", c)
	}
}

func TestEvaluateRules_LargeApprovalFlagsBelowUnlimited(t *testing.T) {
	val := new(big.Int).Mul(LargeApprovalThreshold, big.NewInt(1))
	checks := EvaluateRules(RulesInput{
		Now:              time.Now(),
		AllowanceRequest: &AllowanceRequest{RequestedAllowance: val},
		EstimatedFeeWei:  big.NewInt(1),
	})
	c := findCheck(checks, "large-approval")
	if c == nil || c.Status != StatusWarn || c.Severity != SeverityMedium {
		t.Errorf("large-approval check = %+v", c)
	}
}

func TestEvaluateRules_UnknownSpenderWarn(t *testing.T) {
	checks := EvaluateRules(RulesInput{
		Now:             time.Now(),
		Spender:         "0xdeadbeef",
		KnownSpenders:   []string{"0x1111"},
		EstimatedFeeWei: big.NewInt(1),
	})
	c := findCheck(checks, "unknown-spender")
	if c == nil || c.Status != StatusWarn {
		t.Errorf("unknown-spender = %+v", c)
	}
}

func TestEvaluateRules_KnownSpenderPass(t *testing.T) {
	checks := EvaluateRules(RulesInput{
		Now:             time.Now(),
		Spender:         "0xABCD",
		KnownSpenders:   []string{"0xabcd"},
		EstimatedFeeWei: big.NewInt(1),
	})
	c := findCheck(checks, "spender-known")
	if c == nil || c.Status != StatusPass {
		t.Errorf("spender-known = %+v", c)
	}
}

func TestEvaluateRules_QuoteExpired(t *testing.T) {
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	checks := EvaluateRules(RulesInput{
		Now:             now,
		QuoteExpiresAt:  "2026-05-28T10:00:00Z",
		EstimatedFeeWei: big.NewInt(1),
	})
	c := findCheck(checks, "quote-expired")
	if c == nil || c.Status != StatusFail {
		t.Errorf("quote-expired = %+v", c)
	}
}

func TestEvaluateRules_QuoteMalformedWarn(t *testing.T) {
	checks := EvaluateRules(RulesInput{
		Now:             time.Now(),
		QuoteExpiresAt:  "not-iso",
		EstimatedFeeWei: big.NewInt(1),
	})
	c := findCheck(checks, "quote-invalid")
	if c == nil || c.Status != StatusWarn {
		t.Errorf("quote-invalid = %+v", c)
	}
}

func TestEvaluateRules_HighSlippageBands(t *testing.T) {
	highBps := 100
	critBps := 500
	{
		c := findCheck(EvaluateRules(RulesInput{Now: time.Now(), SlippageBps: &highBps, EstimatedFeeWei: big.NewInt(1)}), "high-slippage")
		if c == nil || c.Status != StatusWarn {
			t.Errorf("high-slippage = %+v", c)
		}
	}
	{
		c := findCheck(EvaluateRules(RulesInput{Now: time.Now(), SlippageBps: &critBps, EstimatedFeeWei: big.NewInt(1)}), "high-slippage-critical")
		if c == nil || c.Status != StatusFail {
			t.Errorf("high-slippage-critical = %+v", c)
		}
	}
}

func TestEvaluateRules_GasInsufficientFunds(t *testing.T) {
	checks := EvaluateRules(RulesInput{
		Now:             time.Now(),
		SimulationError: "execution reverted: insufficient funds for transfer",
	})
	c := findCheck(checks, "insufficient-gas")
	if c == nil || c.Status != StatusFail {
		t.Errorf("insufficient-gas = %+v", c)
	}
}

func TestEvaluateRules_GasBalanceLow(t *testing.T) {
	checks := EvaluateRules(RulesInput{
		Now:              time.Now(),
		NativeBalanceWei: big.NewInt(100),
		EstimatedFeeWei:  big.NewInt(200),
	})
	c := findCheck(checks, "gas-balance-low")
	if c == nil || c.Status != StatusWarn {
		t.Errorf("gas-balance-low = %+v", c)
	}
}

func TestEvaluateRules_GasUnavailable(t *testing.T) {
	checks := EvaluateRules(RulesInput{Now: time.Now()})
	c := findCheck(checks, "gas-unavailable")
	if c == nil || c.Status != StatusWarn {
		t.Errorf("gas-unavailable = %+v", c)
	}
}

func TestEvaluateRules_UnsupportedTokenFail(t *testing.T) {
	no := false
	checks := EvaluateRules(RulesInput{
		Now:              time.Now(),
		IsSupportedToken: &no,
		EstimatedFeeWei:  big.NewInt(1),
	})
	c := findCheck(checks, "unsupported-token")
	if c == nil || c.Status != StatusFail {
		t.Errorf("unsupported-token = %+v", c)
	}
}

func TestEvaluateRules_TokenWarningsMerged(t *testing.T) {
	checks := EvaluateRules(RulesInput{
		Now:             time.Now(),
		TokenWarnings:   []string{"scam tag", "phishing tag"},
		EstimatedFeeWei: big.NewInt(1),
	})
	c := findCheck(checks, "token-warning")
	if c == nil || c.Status != StatusWarn || c.Summary != "scam tag · phishing tag" {
		t.Errorf("token-warning = %+v", c)
	}
}

func TestBuildRecommendedActions(t *testing.T) {
	checks := []Check{
		{ID: "max-approval"},
		{ID: "large-approval"},
		{ID: "unknown-spender"},
		{ID: "gas-balance-low"},
	}
	got := BuildRecommendedActions(checks, nil, SimulationModeFallback, nil)
	want := map[string]bool{
		"Reduce allowance size before signing.":                                        true,
		"Verify spender ownership and contract source.":                                true,
		"Top up native gas balance before signing.":                                    true,
		"Treat the gas preview as provisional because RPC simulation was unavailable.": true,
	}
	if len(got) != len(want) {
		t.Errorf("len = %d, want %d (got: %v)", len(got), len(want), got)
	}
	for _, s := range got {
		if !want[s] {
			t.Errorf("unexpected action: %q", s)
		}
	}
}

func TestBuildRecommendedActions_DeduplicatesMaxAndLargeApproval(t *testing.T) {
	got := BuildRecommendedActions([]Check{{ID: "max-approval"}, {ID: "large-approval"}}, nil, SimulationModeEstimate, nil)
	if len(got) != 1 || got[0] != "Reduce allowance size before signing." {
		t.Errorf("got = %v, want single allowance-reduction action", got)
	}
}

func TestBuildRecommendedActions_AppendsSitePermissions(t *testing.T) {
	site := &ConnectedSiteSummary{ID: "s1"}
	got := BuildRecommendedActions(nil, site, SimulationModeEstimate, []string{"transfer", "approve"})
	if len(got) != 1 || got[0] != "Connected site permissions: transfer, approve." {
		t.Errorf("got = %v", got)
	}
}
