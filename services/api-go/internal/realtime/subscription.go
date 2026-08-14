// Package realtime ports the NestJS Socket.IO realtime tier (markets +
// token-events gateways) to Go using a Socket.IO v4-compatible server, per
// ADR 0006 (strict zero-Nest). This file is the pure, dependency-free
// subscription logic mirrored from legacy NestJS markets/markets.gateway.ts
// and common/ws/ws-subscription-guard.ts so it is unit-testable without a
// live socket.
package realtime

import (
	"regexp"
	"strconv"
	"strings"
)

type marketTopic string

const (
	topicTicker marketTopic = "ticker"
	topicBook   marketTopic = "book"
	topicTrades marketTopic = "trades"
)

// marketTopics is the iteration order used on snapshot fan-out, matching the
// NestJS `(['ticker','book','trades'] as const).forEach(...)`.
var marketTopics = []marketTopic{topicTicker, topicBook, topicTrades}

// safeKeyRE mirrors SAFE_KEY_RE in ws-subscription-guard.ts:
// /^[a-zA-Z0-9._:/\-]+$/.
var safeKeyRE = regexp.MustCompile(`^[a-zA-Z0-9._:/\-]+$`)

// ethAddressRE mirrors ETH_ADDRESS_RE in token-events.gateway.ts.
var ethAddressRE = regexp.MustCompile(`^0x[a-fA-F0-9]{40}$`)

const maxKeyLength = 64

// normalizeTokenAddress mirrors token-events.gateway.ts normalizeTokenAddress:
// trim, require a 0x + 20-byte hex string, return lowercased (so the subscribe
// room token:{addr} matches the emit room token:{addr.toLowerCase()}). Returns
// "" when invalid.
func normalizeTokenAddress(input string) string {
	trimmed := strings.TrimSpace(input)
	if !ethAddressRE.MatchString(trimmed) {
		return ""
	}
	return strings.ToLower(trimmed)
}

// normalizeKey mirrors WsSubscriptionGuard.normalizeKey: trim, reject empty,
// reject > maxKeyLength, reject non-SAFE_KEY_RE. Returns "" when invalid.
func normalizeKey(input string) string {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" || len(trimmed) > maxKeyLength || !safeKeyRE.MatchString(trimmed) {
		return ""
	}
	return trimmed
}

// sanitizeChainID mirrors sanitizeChainId() in markets.gateway.ts: only a
// positive integer in (0, 0xffffffff] survives; everything else collapses to 0
// ("all"). `present` is false when the JSON omitted chainId.
func sanitizeChainID(v float64, present bool) int {
	if !present || v != float64(int64(v)) {
		return 0
	}
	iv := int64(v)
	if iv <= 0 || iv > 0xffffffff {
		return 0
	}
	return int(iv)
}

type marketSubscription struct {
	topic   marketTopic
	symbol  string // canonical UPPER form
	chainID int    // 0 == "all" / omitted
	room    string
	event   string
}

// buildSubscription mirrors buildSubscription() in markets.gateway.ts:
//
//	room  = market:{topic}:{SYMBOL}:{chainId|all}
//	event = market:{topic}:{SYMBOL}
func buildSubscription(topic marketTopic, symbol string, chainID int) marketSubscription {
	sym := strings.ToUpper(strings.TrimSpace(symbol))
	suffix := "all"
	if chainID != 0 {
		suffix = strconv.Itoa(chainID)
	}
	return marketSubscription{
		topic:   topic,
		symbol:  sym,
		chainID: chainID,
		room:    "market:" + string(topic) + ":" + sym + ":" + suffix,
		event:   "market:" + string(topic) + ":" + sym,
	}
}
