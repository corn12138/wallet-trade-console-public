// Package marketstreamwire adapts the markets read service into the
// market-stream worker's data sources (auto-track lister + snapshot compute).
// It lives in its own package so both cmd/api and cmd/market-stream share the
// exact same wiring without duplicating it, and without making the low-level
// internal/marketstream pub/sub primitive depend on internal/markets.
package marketstreamwire

import (
	"context"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/markets"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/marketstream"
)

// ListMarkets adapts markets.Service.GetSymbols into a marketstream auto-track
// source: the deployed (symbol, chainId) markets for a chain filter. This is
// the Go equivalent of NestJS MarketStreamSnapshotService.getTrackedMarkets,
// which maps tradingService.getMarkets(chainId) to {symbol, chainId}.
func ListMarkets(svc *markets.Service) marketstream.ListMarketsFunc {
	return func(_ context.Context, chainID *int) ([]marketstream.TrackedMarket, error) {
		if svc == nil {
			return nil, nil
		}
		syms, err := svc.GetSymbols(chainID)
		if err != nil {
			return nil, err
		}
		out := make([]marketstream.TrackedMarket, 0, len(syms))
		for _, s := range syms {
			out = append(out, marketstream.TrackedMarket{Symbol: s.Symbol, ChainID: s.ChainID})
		}
		return out, nil
	}
}

// Compute adapts markets.Service.GetRealtimeSnapshot into the worker's
// ComputeFunc — the DB-backed snapshot per tracked market.
func Compute(svc *markets.Service) marketstream.ComputeFunc {
	return func(ctx context.Context, m marketstream.TrackedMarket) (*marketstream.Snapshot, error) {
		if svc == nil {
			return nil, nil
		}
		var filter *int
		if m.ChainID != 0 {
			c := m.ChainID
			filter = &c
		}
		rs, err := svc.GetRealtimeSnapshot(ctx, m.Symbol, filter)
		if err != nil {
			return nil, err
		}
		return ToStreamSnapshot(rs), nil
	}
}

// ToStreamSnapshot converts the markets REST snapshot into a market-stream
// snapshot. Guards against the typed-nil-in-interface trap for Ticker (a nil
// *markets.Ticker boxed in `any` would otherwise be a non-nil interface and the
// gateway would emit a null ticker frame).
func ToStreamSnapshot(rs markets.RealtimeSnapshot) *marketstream.Snapshot {
	chain := 0
	if rs.ChainID != nil {
		chain = *rs.ChainID
	}
	var ticker any
	if rs.Ticker != nil {
		ticker = rs.Ticker
	}
	return &marketstream.Snapshot{
		Symbol:    rs.Symbol,
		ChainID:   chain,
		Ticker:    ticker,
		Orderbook: marketstream.Orderbook{Bids: rs.Orderbook.Bids, Asks: rs.Orderbook.Asks},
		Trades:    rs.Trades,
		UpdatedAt: rs.UpdatedAt,
	}
}
