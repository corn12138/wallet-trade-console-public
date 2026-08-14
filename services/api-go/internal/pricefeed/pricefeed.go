// Package pricefeed gives the product a real, persisted price time series.
//
// Why it exists: every other candle surface in this repo is a projection of
// OUR OWN on-chain activity (perp_trades → trading.GetCandles, token_trades →
// token candles). On a testnet that activity is near-zero, so /trade rendered
// a permanently empty chart and no screen in the product had a real price.
//
// This package ingests two genuinely real sources and stores both:
//
//	market  — dense OHLCV from a public spot-market provider (Gate.io). Real
//	          trades on a real venue; the only source with meaningful volume.
//	oracle  — Chainlink aggregator rounds read over the same JSON-RPC endpoint
//	          the indexer already uses. Trust-minimized and on-chain, but only
//	          updates on heartbeat/deviation, so it is sparse by nature.
//
// Nothing here synthesizes, interpolates or back-fills invented prices. Every
// stored row carries the source that produced it, and the read API returns
// that source so the UI can label it. A caller that asks for a source with no
// data gets an empty series — never a substitute.
package pricefeed

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Resolution is a candle bucket width. The set mirrors
// trading.TradingCandleResolution so the /trade chart can switch between
// on-chain and reference candles without changing its controls.
type Resolution string

const (
	Res1m  Resolution = "1m"
	Res5m  Resolution = "5m"
	Res15m Resolution = "15m"
	Res1h  Resolution = "1h"
	Res4h  Resolution = "4h"
	Res1d  Resolution = "1d"
)

var resolutionDuration = map[Resolution]time.Duration{
	Res1m:  time.Minute,
	Res5m:  5 * time.Minute,
	Res15m: 15 * time.Minute,
	Res1h:  time.Hour,
	Res4h:  4 * time.Hour,
	Res1d:  24 * time.Hour,
}

// SupportedResolutions returns the resolutions in ascending width order.
// Callers use it to drive ingestion and to validate request input.
func SupportedResolutions() []Resolution {
	return []Resolution{Res1m, Res5m, Res15m, Res1h, Res4h, Res1d}
}

// ParseResolution validates a raw resolution string. Unlike the trading
// package's parser it does NOT silently coerce an unknown value to a default —
// a bad resolution is a client error, and quietly serving a different bucket
// width than requested would be its own small lie.
func ParseResolution(raw string) (Resolution, error) {
	r := Resolution(strings.TrimSpace(raw))
	if _, ok := resolutionDuration[r]; ok {
		return r, nil
	}
	return "", fmt.Errorf("pricefeed: unsupported resolution %q", raw)
}

// Duration returns the bucket width.
func (r Resolution) Duration() time.Duration { return resolutionDuration[r] }

// BucketStart floors t to the start of its bucket, in UTC. Bucketing is
// absolute (epoch-anchored), matching how every provider aligns candles, so
// the same wall-clock minute maps to the same bucket everywhere.
func (r Resolution) BucketStart(t time.Time) time.Time {
	d := resolutionDuration[r]
	if d <= 0 {
		return t.UTC()
	}
	return t.UTC().Truncate(d)
}

// Source identifies which real feed produced a row. Stored on every record and
// echoed by the API so the UI can label what the user is looking at.
type Source string

const (
	// SourceMarket is dense spot-market OHLCV from a public venue.
	SourceMarket Source = "market"
	// SourceOracle is on-chain Chainlink aggregator rounds.
	SourceOracle Source = "oracle"
	// SourceOnchain is this product's own perp_trades projection. Served by
	// the trading package, not here; named so the API vocabulary is complete.
	SourceOnchain Source = "onchain"
)

// Candle is one OHLCV bucket. Prices and volumes are decimal strings so an
// exact NUMERIC(38,18) round-trips without float error, matching how the
// trading package already returns money.
type Candle struct {
	Timestamp   int64  `json:"timestamp"` // bucket start, epoch ms (UTC)
	Open        string `json:"open"`
	High        string `json:"high"`
	Low         string `json:"low"`
	Close       string `json:"close"`
	Volume      string `json:"volume"`      // base-asset volume
	QuoteVolume string `json:"quoteVolume"` // quote-asset (USD) volume
	Closed      bool   `json:"closed"`      // false = bucket still forming
}

// Observation is one Chainlink aggregator round.
type Observation struct {
	Symbol      string    `json:"symbol"`
	ChainID     int       `json:"chainId"`
	FeedAddress string    `json:"feedAddress"`
	RoundID     string    `json:"roundId"`
	Price       string    `json:"price"` // normalized to whole quote units
	Decimals    int       `json:"decimals"`
	ObservedAt  time.Time `json:"observedAt"`
}

// SortCandlesDesc orders candles newest-first, the order the /trade chart and
// the existing trading endpoint both expect.
func SortCandlesDesc(candles []Candle) {
	sort.Slice(candles, func(i, j int) bool { return candles[i].Timestamp > candles[j].Timestamp })
}

// NormalizeSymbol upper-cases and trims a market symbol. It deliberately does
// not rewrite separators: "ETH-USD" is the product's canonical form and a
// caller passing something else should get an empty result rather than a
// silently remapped market.
func NormalizeSymbol(raw string) string {
	return strings.ToUpper(strings.TrimSpace(raw))
}
