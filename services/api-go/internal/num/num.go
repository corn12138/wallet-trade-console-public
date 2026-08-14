// Package num provides exact conversions between raw on-chain integer
// amounts (wei / smallest-unit strings) and normalized decimal strings for
// PostgreSQL NUMERIC columns and JSON financial fields.
//
// The canonical unit contract (docs/migration/backend-go/token-units.md):
// raw event amounts stay integer strings for auditability; every derived
// financial value (price, OHLC, volume, market cap) is normalized to NATIVE
// units exactly once at the indexer/service boundary using pure integer
// math — no float64 ever holds a durable financial value.
package num

import (
	"fmt"
	"math/big"
	"strings"
)

// WeiToDecimal converts a wei amount to an exact NATIVE decimal string
// ("1500000000000000000" → "1.5"; "1" → "0.000000000000000001").
// Negative amounts keep their sign. nil returns "0".
func WeiToDecimal(wei *big.Int) string {
	if wei == nil {
		return "0"
	}
	return ScaleToDecimal(wei, 18)
}

// ScaleToDecimal converts an integer amount in 10^-decimals units to an
// exact decimal string with trailing zeros trimmed.
func ScaleToDecimal(amount *big.Int, decimals int) string {
	if amount == nil || amount.Sign() == 0 {
		return "0"
	}
	neg := amount.Sign() < 0
	abs := new(big.Int).Abs(amount)
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	intPart := new(big.Int)
	frac := new(big.Int)
	intPart.QuoRem(abs, scale, frac)
	out := intPart.String()
	if frac.Sign() != 0 {
		fracStr := frac.String()
		if pad := decimals - len(fracStr); pad > 0 {
			fracStr = strings.Repeat("0", pad) + fracStr
		}
		fracStr = strings.TrimRight(fracStr, "0")
		out += "." + fracStr
	}
	if neg {
		out = "-" + out
	}
	return out
}

// WeiStringToDecimal parses a raw integer string (wei) and converts it to a
// NATIVE decimal string. Invalid input returns an error — callers must not
// silently coerce.
func WeiStringToDecimal(wei string) (string, error) {
	n, ok := new(big.Int).SetString(strings.TrimSpace(wei), 10)
	if !ok {
		return "", fmt.Errorf("num: %q is not a base-10 integer", wei)
	}
	return WeiToDecimal(n), nil
}

// MulWeiInt multiplies a wei amount by an integer factor (e.g. a fixed token
// supply) exactly and returns the normalized NATIVE decimal string.
func MulWeiInt(wei *big.Int, factor int64) string {
	if wei == nil {
		return "0"
	}
	product := new(big.Int).Mul(wei, big.NewInt(factor))
	return WeiToDecimal(product)
}

// SumWeiStrings adds raw integer strings exactly and returns the normalized
// NATIVE decimal string. Invalid entries are reported, not skipped.
func SumWeiStrings(weis []string) (string, error) {
	total := new(big.Int)
	for _, w := range weis {
		n, ok := new(big.Int).SetString(strings.TrimSpace(w), 10)
		if !ok {
			return "", fmt.Errorf("num: %q is not a base-10 integer", w)
		}
		total.Add(total, n)
	}
	return WeiToDecimal(total), nil
}

// CompareDecimal compares two decimal strings numerically (-1, 0, 1).
// Invalid input returns an error.
func CompareDecimal(a, b string) (int, error) {
	ra, ok := new(big.Rat).SetString(strings.TrimSpace(a))
	if !ok {
		return 0, fmt.Errorf("num: %q is not a decimal", a)
	}
	rb, ok := new(big.Rat).SetString(strings.TrimSpace(b))
	if !ok {
		return 0, fmt.Errorf("num: %q is not a decimal", b)
	}
	return ra.Cmp(rb), nil
}

// NormalizeDecimal canonicalizes a decimal string produced by PostgreSQL
// (e.g. "1.500000000000000000" → "1.5", "0.000000000000000000" → "0").
func NormalizeDecimal(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "0"
	}
	if !strings.Contains(s, ".") {
		return s
	}
	s = strings.TrimRight(s, "0")
	s = strings.TrimSuffix(s, ".")
	if s == "" || s == "-" {
		return "0"
	}
	return s
}
