package abi

import (
	"errors"
	"fmt"
	"math/big"
	"strings"
)

// ErrInvalidAmount is returned by ParseUnits when the input string is
// not a valid non-negative decimal or when decimals is out of range.
// Callers that want HTTP 400-ish semantics should remap to their own
// sentinel and check with errors.Is.
var ErrInvalidAmount = errors.New("abi: invalid amount")

// ParseUnits mirrors viem's parseUnits: convert a decimal string to a
// big.Int representing the value × 10^decimals. Negative input is
// rejected (uint-only here); fractional digits beyond `decimals`
// truncate (matches viem). Lifted from earn.parseAmount so the swap
// port can share the same parser.
func ParseUnits(s string, decimals int) (*big.Int, error) {
	if decimals < 0 || decimals > 78 {
		return nil, fmt.Errorf("%w: decimals out of range", ErrInvalidAmount)
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, ErrInvalidAmount
	}
	if strings.HasPrefix(s, "-") {
		return nil, ErrInvalidAmount
	}
	parts := strings.SplitN(s, ".", 2)
	whole := parts[0]
	frac := ""
	if len(parts) == 2 {
		frac = parts[1]
	}
	for _, c := range whole {
		if c < '0' || c > '9' {
			return nil, ErrInvalidAmount
		}
	}
	for _, c := range frac {
		if c < '0' || c > '9' {
			return nil, ErrInvalidAmount
		}
	}
	if len(frac) > decimals {
		frac = frac[:decimals]
	} else {
		frac += strings.Repeat("0", decimals-len(frac))
	}
	combined := whole + frac
	combined = strings.TrimLeft(combined, "0")
	if combined == "" {
		return big.NewInt(0), nil
	}
	v, ok := new(big.Int).SetString(combined, 10)
	if !ok {
		return nil, ErrInvalidAmount
	}
	return v, nil
}

// FormatUnits mirrors viem's formatUnits: integer-divide by 10^decimals
// and emit a decimal string. Trailing zeros after the decimal point
// are trimmed; if the entire fractional part is zero, no decimal point
// is emitted. Negative inputs are formatted with a leading minus.
func FormatUnits(value *big.Int, decimals int) string {
	if value == nil {
		return "0"
	}
	if decimals <= 0 {
		return value.String()
	}
	negative := value.Sign() < 0
	abs := new(big.Int).Abs(value)
	display := abs.String()
	if len(display) <= decimals {
		display = strings.Repeat("0", decimals-len(display)+1) + display
	}
	cut := len(display) - decimals
	integer := display[:cut]
	fraction := strings.TrimRight(display[cut:], "0")
	out := integer
	if fraction != "" {
		out = integer + "." + fraction
	}
	if negative {
		out = "-" + out
	}
	return out
}
