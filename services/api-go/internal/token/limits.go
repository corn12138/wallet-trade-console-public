package token

import "strconv"

const (
	defaultTokenListLimit     = 20
	maxTokenListLimit         = 100
	defaultTokenListPage      = 1
	maxTokenListPage          = 1000
	defaultTrendingTokenLimit = 10
	maxTrendingTokenLimit     = 100
	defaultTokenCandleLimit   = 100
	maxTokenCandleLimit       = 500
	maxCandleAggregations     = 2
)

// boundedLimit keeps public query parameters from becoming SQL LIMIT values or
// slice capacities without an application-owned upper bound.
func boundedLimit(value, defaultValue, maxValue int) int {
	if value <= 0 {
		return defaultValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

// capPositiveLimit preserves repository methods' existing zero-result contract
// while defending direct callers that bypass the HTTP and service layers.
func capPositiveLimit(value, maxValue int) int {
	if value > maxValue {
		return maxValue
	}
	return value
}

func parseBoundedLimit(raw string, defaultValue, maxValue int) int {
	value, err := strconv.Atoi(raw)
	if err != nil {
		return defaultValue
	}
	return boundedLimit(value, defaultValue, maxValue)
}

func boundedPage(value int) int {
	return boundedLimit(value, defaultTokenListPage, maxTokenListPage)
}

func parseBoundedPage(raw string) int {
	value, err := strconv.Atoi(raw)
	if err != nil {
		return defaultTokenListPage
	}
	return boundedPage(value)
}
