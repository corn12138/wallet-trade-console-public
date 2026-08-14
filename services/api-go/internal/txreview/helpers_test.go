package txreview

import (
	"errors"
	"math/big"
	"testing"
	"time"
)

func TestValidateAddress(t *testing.T) {
	addr, err := ValidateAddress("0x000000000000000000000000000000000000aAaA", "from")
	if err != nil || addr != "0x000000000000000000000000000000000000aaaa" {
		t.Errorf("ok case: addr=%q err=%v", addr, err)
	}
	if _, err := ValidateAddress("not-an-addr", "x"); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("bad case: err=%v, want ErrInvalidInput", err)
	}
}

func TestNormalizeOptionalOrigin(t *testing.T) {
	cases := map[string]string{
		"":                 "",
		"https://x.com":    "https://x.com",
		"x.com":            "https://x.com",
		"https://X.COM/y":  "https://x.com",
		"http://localhost": "http://localhost",
	}
	for in, want := range cases {
		got, err := NormalizeOptionalOrigin(in)
		if err != nil {
			t.Errorf("err for %q: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("origin(%q) = %q, want %q", in, got, want)
		}
	}
	if _, err := NormalizeOptionalOrigin("https://"); err == nil {
		t.Errorf("expected err for empty host")
	}
}

func TestIsUnlimitedAllowance(t *testing.T) {
	threshold := MaxUint256Div100
	tests := []struct {
		v    *big.Int
		want bool
	}{
		{big.NewInt(0), false},
		{LargeApprovalThreshold, false},
		{threshold, true},
		{new(big.Int).Add(threshold, big.NewInt(1)), true},
	}
	for _, tc := range tests {
		if got := IsUnlimitedAllowance(tc.v); got != tc.want {
			t.Errorf("v=%s got %v want %v", tc.v, got, tc.want)
		}
	}
}

func TestApplyGasPadding(t *testing.T) {
	if got := ApplyGasPadding(big.NewInt(100)); got.Cmp(big.NewInt(120)) != 0 {
		t.Errorf("100 → %s, want 120", got)
	}
	if got := ApplyGasPadding(big.NewInt(0)); got.Sign() != 0 {
		t.Errorf("0 → %s, want 0", got)
	}
}

func TestDedupeChecks_KeepsLast(t *testing.T) {
	in := []Check{
		{ID: "a", Title: "first-a"},
		{ID: "b", Title: "first-b"},
		{ID: "a", Title: "second-a"},
	}
	got := DedupeChecks(in)
	if len(got) != 2 {
		t.Fatalf("len = %d", len(got))
	}
	if got[0].ID != "a" || got[0].Title != "second-a" {
		t.Errorf("first = %+v, want a/second-a", got[0])
	}
	if got[1].ID != "b" {
		t.Errorf("second.ID = %q", got[1].ID)
	}
}

func TestDeriveReviewStatus(t *testing.T) {
	pass := Check{Status: StatusPass}
	warn := Check{Status: StatusWarn}
	fail := Check{Status: StatusFail}
	if DeriveReviewStatus([]Check{pass, fail, warn}, SimulationModeEstimate) != ReviewBlocked {
		t.Errorf("fail should block")
	}
	if DeriveReviewStatus([]Check{pass, warn}, SimulationModeEstimate) != ReviewWarning {
		t.Errorf("warn → warning")
	}
	if DeriveReviewStatus([]Check{pass}, SimulationModeFallback) != ReviewSimulationUnavailable {
		t.Errorf("fallback → simulation-unavailable")
	}
	if DeriveReviewStatus([]Check{pass}, SimulationModeEstimate) != ReviewApproved {
		t.Errorf("estimate + no warn/fail → approved")
	}
}

func TestComputeRiskScore(t *testing.T) {
	checks := []Check{
		{Status: StatusPass, Severity: SeverityHigh}, // 0
		{Status: StatusWarn, Severity: SeverityHigh}, // round(28/2) = 14
		{Status: StatusFail, Severity: SeverityHigh}, // 28
	}
	if got := ComputeRiskScore(checks, nil); got != 42 {
		t.Errorf("score = %d, want 42", got)
	}
	site := &ConnectedSiteSummary{RiskLevel: "Suspicious"}
	if got := ComputeRiskScore(checks, site); got != 42+12 {
		t.Errorf("score with site = %d, want 54", got)
	}
	if got := ComputeRiskScore([]Check{{Status: StatusFail, Severity: SeverityCritical}}, &ConnectedSiteSummary{RiskLevel: "critical"}); got != 52 {
		t.Errorf("score critical-only = %d, want 52", got)
	}
	if got := ComputeRiskScore([]Check{
		{Status: StatusFail, Severity: SeverityCritical}, // 40
		{Status: StatusFail, Severity: SeverityCritical}, // 40
		{Status: StatusFail, Severity: SeverityCritical}, // 40
	}, nil); got != 100 {
		t.Errorf("score capped = %d, want 100", got)
	}
}

func TestIsoExpiryInPast(t *testing.T) {
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	if ok, past := IsoExpiryInPast("2026-05-28T11:00:00Z", now); !ok || !past {
		t.Errorf("past expiry: ok=%v past=%v", ok, past)
	}
	if ok, past := IsoExpiryInPast("2026-05-28T13:00:00Z", now); !ok || past {
		t.Errorf("future expiry: ok=%v past=%v", ok, past)
	}
	if ok, _ := IsoExpiryInPast("garbage", now); ok {
		t.Errorf("garbage should not parse")
	}
}

func TestFormatBps(t *testing.T) {
	if got := FormatBps(50); got != "0.50%" {
		t.Errorf("50 → %q", got)
	}
	if got := FormatBps(500); got != "5.00%" {
		t.Errorf("500 → %q", got)
	}
}

func TestTruncateAddress(t *testing.T) {
	if got := TruncateAddress("0xabcdef1234567890abcdef1234567890abcdef12"); got != "0xabcd...ef12" {
		t.Errorf("got = %q", got)
	}
	if got := TruncateAddress("0xabc"); got != "0xabc" {
		t.Errorf("short address mangled: %q", got)
	}
}
