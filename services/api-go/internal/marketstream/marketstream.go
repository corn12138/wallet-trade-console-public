// Package marketstream ports the NestJS market-stream subsystem
// (legacy NestJS markets/market-stream.*) to Go, per ADR 0006 (strict
// zero-Nest). It is the snapshot cache + pub/sub bus between the worker
// (publisher) and the realtime gateway (subscriber): a MemoryDriver for a single
// process, or a RedisDriver (next slice) for cross-process fan-out matching the
// NestJS redis driver.
package marketstream

import (
	"context"
	"fmt"
	"strings"
)

// Snapshot mirrors MarketStreamSnapshot in market-stream.types.ts.
type Snapshot struct {
	Symbol    string    `json:"symbol"`
	ChainID   int       `json:"chainId,omitempty"`
	Ticker    any       `json:"ticker"`
	Orderbook Orderbook `json:"orderbook"`
	Trades    any       `json:"trades"`
	UpdatedAt string    `json:"updatedAt"`
}

// Orderbook mirrors the {bids, asks} shape of TradingOrderbookView.
type Orderbook struct {
	Bids any `json:"bids"`
	Asks any `json:"asks"`
}

// TrackedMarket mirrors MarketStreamTrackedMarket — a (symbol, chainId) the
// worker refreshes each tick.
type TrackedMarket struct {
	Symbol  string
	ChainID int
}

// Driver mirrors MarketStreamDriver: a snapshot cache plus a publish/subscribe
// bus so the worker and the gateway communicate in-process (memory) or across
// processes (redis).
type Driver interface {
	Connect(ctx context.Context) error
	Disconnect(ctx context.Context) error
	GetSnapshot(ctx context.Context, symbol string, chainID int) (*Snapshot, error)
	PrimeSnapshot(ctx context.Context, snap *Snapshot) error
	PublishSnapshot(ctx context.Context, snap *Snapshot) error
	// Subscribe registers a listener and returns an unsubscribe func.
	Subscribe(listener func(*Snapshot)) (func(), error)
}

// cacheKey matches the NestJS cache key convention: UPPER symbol + chain suffix
// ("all" when chainId is 0/absent), so memory and redis drivers agree.
func cacheKey(symbol string, chainID int) string {
	suffix := "all"
	if chainID != 0 {
		suffix = fmt.Sprintf("%d", chainID)
	}
	return strings.ToUpper(strings.TrimSpace(symbol)) + ":" + suffix
}
