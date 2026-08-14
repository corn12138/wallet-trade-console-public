package trading

import (
	"strconv"
	"strings"
)

// NormalizeSymbol mirrors trading.utils.ts normalizeTradingSymbol:
// trim whitespace, uppercase. The Prisma `token` column stores the
// upper-cased canonical form; queries that skip this helper silently
// return empty results, so every public entry point that takes a
// user-supplied symbol must funnel through here.
func NormalizeSymbol(symbol string) string {
	return strings.ToUpper(strings.TrimSpace(symbol))
}

// DeriveMidPrice returns the float-average of bestBid and bestAsk as
// a string, or nil if either side is missing or unparseable. Matches
// the trading.service.ts helper exactly — including its lossy
// JS `Number()` semantics. We use this *only* for derived display
// prices, never for arithmetic on stored bigint values.
func DeriveMidPrice(bestBid, bestAsk *string) *string {
	if bestBid == nil || bestAsk == nil {
		return nil
	}
	bidF, err := strconv.ParseFloat(*bestBid, 64)
	if err != nil {
		return nil
	}
	askF, err := strconv.ParseFloat(*bestAsk, 64)
	if err != nil {
		return nil
	}
	mid := (bidF + askF) / 2
	s := strconv.FormatFloat(mid, 'f', -1, 64)
	return &s
}

// DeriveSpread returns askPrice - bidPrice as a float-derived string,
// or nil if either side is missing. Lossy-by-design (matches NestJS).
func DeriveSpread(bestBid, bestAsk *string) *string {
	if bestBid == nil || bestAsk == nil {
		return nil
	}
	bidF, err := strconv.ParseFloat(*bestBid, 64)
	if err != nil {
		return nil
	}
	askF, err := strconv.ParseFloat(*bestAsk, 64)
	if err != nil {
		return nil
	}
	diff := askF - bidF
	s := strconv.FormatFloat(diff, 'f', -1, 64)
	return &s
}
