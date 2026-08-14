package txreview

import (
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// ErrInvalidInput is returned by validation helpers; the handler maps
// it to HTTP 400. Wrapping with %w preserves the helper's specific
// message ("Invalid token address", etc.) — same shape as NestJS
// BadRequestException.
var ErrInvalidInput = errors.New("txreview: invalid input")

var (
	addressRE = regexp.MustCompile(`^0x[a-fA-F0-9]{40}$`)
	digitsRE  = regexp.MustCompile(`^\d+$`)
)

// MaxUint256Div100 = (2^256 - 1) / 100 — the threshold viem uses to
// flag a max-approval pattern in isUnlimitedAllowance.
var MaxUint256Div100 = func() *big.Int {
	v := new(big.Int).Lsh(big.NewInt(1), 256)
	v.Sub(v, big.NewInt(1))
	v.Div(v, big.NewInt(100))
	return v
}()

// LargeApprovalThreshold = 10^24 — matches LARGE_APPROVAL_THRESHOLD in
// the NestJS rules engine.
var LargeApprovalThreshold = new(big.Int).Exp(big.NewInt(10), big.NewInt(24), nil)

// ValidateAddress mirrors the NestJS validateAddress helper. Returns
// the lower-cased address or ErrInvalidInput.
func ValidateAddress(value, label string) (string, error) {
	if !addressRE.MatchString(value) {
		return "", fmt.Errorf("%w: invalid %s", ErrInvalidInput, label)
	}
	return strings.ToLower(value), nil
}

// NormalizeOptionalAddressLiteral returns lowercase form or "" if the
// input isn't a well-formed address (matches NestJS — null becomes "").
func NormalizeOptionalAddressLiteral(addr string) string {
	if addr == "" || !addressRE.MatchString(addr) {
		return ""
	}
	return strings.ToLower(addr)
}

// NormalizeOptionalOrigin parses an origin like "https://x.com" or
// "x.com" and returns "scheme://host" lower-cased. Empty input is
// fine; malformed input → ErrInvalidInput.
func NormalizeOptionalOrigin(origin string) (string, error) {
	origin = strings.TrimSpace(origin)
	if origin == "" {
		return "", nil
	}
	if !strings.HasPrefix(origin, "http") {
		origin = "https://" + origin
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("%w: invalid siteOrigin", ErrInvalidInput)
	}
	return strings.ToLower(u.Scheme + "://" + u.Host), nil
}

// NormalizeBigIntString returns the input if it's digits-only, else "".
func NormalizeBigIntString(value string) string {
	if !digitsRE.MatchString(value) {
		return ""
	}
	return value
}

// ParseBigIntLike is value-or-zero. Mirrors parseBigIntLike (NestJS).
func ParseBigIntLike(value string) *big.Int {
	if v := ParseOptionalBigInt(value); v != nil {
		return v
	}
	return big.NewInt(0)
}

// ParseOptionalBigInt returns nil for non-decimal input.
func ParseOptionalBigInt(value string) *big.Int {
	if !digitsRE.MatchString(value) {
		return nil
	}
	v, ok := new(big.Int).SetString(value, 10)
	if !ok {
		return nil
	}
	return v
}

// ApplyGasPadding mirrors viem-side gas padding: 120% of the estimate.
func ApplyGasPadding(gasEstimate *big.Int) *big.Int {
	out := new(big.Int).Mul(gasEstimate, big.NewInt(120))
	out.Quo(out, big.NewInt(100))
	return out
}

// IsUnlimitedAllowance: value ≥ (2^256-1)/100.
func IsUnlimitedAllowance(value *big.Int) bool {
	if value == nil {
		return false
	}
	return value.Cmp(MaxUint256Div100) >= 0
}

// GetErrorMessage mirrors NestJS getErrorMessage — fall back to a
// generic "Simulation failed" when nothing useful is present.
func GetErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	if msg := err.Error(); msg != "" {
		return msg
	}
	return "Simulation failed"
}

// TruncateAddress: "0x1234...beef" projection used by token-intel
// warnings.
func TruncateAddress(address string) string {
	if len(address) < 10 {
		return address
	}
	return address[:6] + "..." + address[len(address)-4:]
}

// DedupeChecks keeps the LAST occurrence of each Check.ID — matches
// `Array.from(new Map(checks.map((c) => [c.id, c])).values())` which
// in JS preserves insertion order and overwrites earlier entries.
func DedupeChecks(in []Check) []Check {
	seen := make(map[string]int, len(in))
	for _, c := range in {
		seen[c.ID] = 0
	}
	// Two-pass: collect last index per ID, then emit in encounter order.
	lastIdx := make(map[string]int, len(in))
	order := make([]string, 0, len(in))
	for i, c := range in {
		if _, ok := lastIdx[c.ID]; !ok {
			order = append(order, c.ID)
		}
		lastIdx[c.ID] = i
	}
	out := make([]Check, 0, len(order))
	for _, id := range order {
		out = append(out, in[lastIdx[id]])
	}
	return out
}

// DeriveReviewStatus mirrors the NestJS deriveReviewStatus.
func DeriveReviewStatus(checks []Check, simulationMode SimulationMode) ReviewStatus {
	for _, c := range checks {
		if c.Status == StatusFail {
			return ReviewBlocked
		}
	}
	for _, c := range checks {
		if c.Status == StatusWarn {
			return ReviewWarning
		}
	}
	if simulationMode == SimulationModeFallback {
		return ReviewSimulationUnavailable
	}
	return ReviewApproved
}

// ComputeRiskScore mirrors computeRiskScore. Connected-site penalty of
// 12 fires when the riskLevel matches high|critical|suspicious|blocked
// (case-insensitive).
func ComputeRiskScore(checks []Check, site *ConnectedSiteSummary) int {
	total := 0
	for _, c := range checks {
		if c.Status == StatusPass {
			continue
		}
		w := severityToScore(c.Severity)
		if c.Status == StatusFail {
			total += w
		} else {
			total += (w + 1) / 2 // Math.round(w/2)
		}
	}
	if site != nil && riskLevelRE.MatchString(site.RiskLevel) {
		total += 12
	}
	if total > 100 {
		total = 100
	}
	return total
}

var riskLevelRE = regexp.MustCompile(`(?i)high|critical|suspicious|blocked`)

func severityToScore(s CheckSeverity) int {
	switch s {
	case SeverityCritical:
		return 40
	case SeverityHigh:
		return 28
	case SeverityMedium:
		return 16
	case SeverityLow:
		return 8
	default:
		return 0
	}
}

// FormatBps mirrors formatBps: 50 → "0.50%".
func FormatBps(value int) string {
	pct := float64(value) / 100.0
	return fmt.Sprintf("%.2f%%", pct)
}

// IsoExpiryInPast parses an RFC3339 timestamp; returns (parsed-ok,
// passed). Used by the rules engine's quote check.
func IsoExpiryInPast(iso string, now time.Time) (parsedOK, passed bool) {
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return false, false
	}
	return true, !t.After(now)
}
