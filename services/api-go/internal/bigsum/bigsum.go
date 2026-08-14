// Package bigsum is the Go port of sumStringBigInts/compareNumericStrings
// from legacy NestJS trading/trading.utils.ts. Position sizes,
// collateral, and trade deltas are stored as bigint strings (USD with
// 30-decimal precision, per the Prisma schema), so we need exact-width
// arithmetic — math/big is the right tool.
package bigsum

import (
	"fmt"
	"math/big"
)

// Sum returns the decimal-string sum of all values. Empty strings are
// treated as zero (matches the JS `value || '0'` coercion). Returns an
// error on the first malformed entry — matches the TS BigInt throw,
// so callers can choose to log+continue or 500 the request.
func Sum(values []string) (string, error) {
	total := new(big.Int)
	scratch := new(big.Int)
	for _, raw := range values {
		if raw == "" {
			continue
		}
		if _, ok := scratch.SetString(raw, 10); !ok {
			return "", fmt.Errorf("bigsum: %q is not a base-10 integer", raw)
		}
		total.Add(total, scratch)
	}
	return total.String(), nil
}

// Compare matches compareNumericStrings in trading.utils.ts:
//   - parses left and right (empty/blank → 0) as big.Int
//   - returns -1 / 0 / 1
//
// Errors on malformed input so the orderbook sort can fail loud rather
// than silently misorder rows.
func Compare(left, right string) (int, error) {
	l, err := parseOrZero(left)
	if err != nil {
		return 0, err
	}
	r, err := parseOrZero(right)
	if err != nil {
		return 0, err
	}
	return l.Cmp(r), nil
}

func parseOrZero(raw string) (*big.Int, error) {
	if raw == "" {
		return new(big.Int), nil
	}
	v, ok := new(big.Int).SetString(raw, 10)
	if !ok {
		return nil, fmt.Errorf("bigsum: %q is not a base-10 integer", raw)
	}
	return v, nil
}
