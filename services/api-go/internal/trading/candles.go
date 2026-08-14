// Package trading's candle aggregation mirrors
// legacy NestJS trading/trading-candle.utils.ts. perp_trades rows
// land in fixed-resolution buckets (1m/5m/15m/1h/4h/1d), open/close =
// first/last by createdAt within the bucket, high/low via bigsum.Compare
// to stay exact for 30-decimal USD price strings.
package trading

import (
	"context"
	"errors"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/bigsum"
)

// TradingCandleResolution mirrors the NestJS string union. Unknown
// values fall back to "15m" via ParseTradingCandleResolution.
type TradingCandleResolution string

const (
	TradingCandle1m  TradingCandleResolution = "1m"
	TradingCandle5m  TradingCandleResolution = "5m"
	TradingCandle15m TradingCandleResolution = "15m"
	TradingCandle1h  TradingCandleResolution = "1h"
	TradingCandle4h  TradingCandleResolution = "4h"
	TradingCandle1d  TradingCandleResolution = "1d"
)

var tradingResolutionMS = map[TradingCandleResolution]int64{
	TradingCandle1m:  60 * 1000,
	TradingCandle5m:  5 * 60 * 1000,
	TradingCandle15m: 15 * 60 * 1000,
	TradingCandle1h:  60 * 60 * 1000,
	TradingCandle4h:  4 * 60 * 60 * 1000,
	TradingCandle1d:  24 * 60 * 60 * 1000,
}

// ParseTradingCandleResolution mirrors parseTradingCandleResolution.
// Default is 15m (not 1h like the token-candle counterpart) to match
// the perp-trading FE's chart default.
func ParseTradingCandleResolution(raw string) TradingCandleResolution {
	r := TradingCandleResolution(raw)
	if _, ok := tradingResolutionMS[r]; ok {
		return r
	}
	return TradingCandle15m
}

// TradingCandleView is the JSON shape /trading/candles returns per
// candle. Big-int sums and exact string prices to preserve precision.
type TradingCandleView struct {
	Timestamp int64  `json:"timestamp"`
	Open      string `json:"open"`
	High      string `json:"high"`
	Low       string `json:"low"`
	Close     string `json:"close"`
	Volume    string `json:"volume"`
	Trades    int    `json:"trades"`
}

// GetCandles aggregates perp_trades into resolution-sized candles for
// one symbol. Empty symbol → []. Window defaults: from = now - res * limit,
// to unbounded (now). Limit clamped to (0, 500].
func (s *Service) GetCandles(ctx context.Context, symbol string, resolution TradingCandleResolution, limit int, fromMS, toMS int64, chainID *int) ([]TradingCandleView, error) {
	if symbol == "" {
		return []TradingCandleView{}, nil
	}
	if limit <= 0 {
		limit = 200
	}
	if limit > 500 {
		limit = 500
	}
	resMS := tradingResolutionMS[resolution]
	if resMS == 0 {
		resMS = tradingResolutionMS[TradingCandle15m]
	}
	from := time.UnixMilli(fromMS)
	if fromMS <= 0 {
		from = time.Now().Add(-time.Duration(resMS*int64(limit)) * time.Millisecond)
	}
	var to *time.Time
	if toMS > 0 {
		t := time.UnixMilli(toMS)
		to = &t
	}
	rows, err := s.repo.ListTradesForCandles(ctx, NormalizeSymbol(symbol), chainID, from, to)
	if errors.Is(err, ErrPoolUnavailable) {
		return []TradingCandleView{}, nil
	}
	if err != nil {
		return nil, err
	}
	return aggregateTradingCandles(rows, resMS, limit), nil
}

// aggregateTradingCandles is the deterministic, side-effect-free port
// of aggregateTradingCandles in trading-candle.utils.ts. Bucketed by
// floor(ts_ms / resolutionMs) * resolutionMs. Returns DESC-by-timestamp,
// then sliced to `limit`.
func aggregateTradingCandles(trades []TradeCandleRow, resolutionMs int64, limit int) []TradingCandleView {
	type bucket struct {
		startMS int64
		trades  []TradeCandleRow
	}
	// Insertion-order map by bucket start; the input is already sorted
	// asc by createdAt, so the first time we see a bucket is its
	// chronological start.
	order := make([]int64, 0, 64)
	groups := make(map[int64]*bucket, 64)
	for _, t := range trades {
		startMS := (t.CreatedAt.UnixMilli() / resolutionMs) * resolutionMs
		b, ok := groups[startMS]
		if !ok {
			b = &bucket{startMS: startMS}
			groups[startMS] = b
			order = append(order, startMS)
		}
		b.trades = append(b.trades, t)
	}
	out := make([]TradingCandleView, 0, len(order))
	for _, startMS := range order {
		b := groups[startMS]
		if len(b.trades) == 0 {
			continue
		}
		open := b.trades[0].Price
		closeP := b.trades[len(b.trades)-1].Price
		high, low := open, open
		deltas := make([]string, 0, len(b.trades))
		for _, t := range b.trades {
			if cmp, err := bigsum.Compare(t.Price, high); err == nil && cmp > 0 {
				high = t.Price
			}
			if cmp, err := bigsum.Compare(t.Price, low); err == nil && cmp < 0 {
				low = t.Price
			}
			deltas = append(deltas, t.SizeDelta)
		}
		volume, _ := bigsum.Sum(deltas)
		out = append(out, TradingCandleView{
			Timestamp: startMS,
			Open:      open,
			High:      high,
			Low:       low,
			Close:     closeP,
			Volume:    volume,
			Trades:    len(b.trades),
		})
	}
	// Sort DESC by timestamp (newest candle first), then slice to limit.
	// The bucket-insertion order was ASC; a manual reverse + slice is
	// cheap given the bounded limit.
	reverse(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func reverse(c []TradingCandleView) {
	for i, j := 0, len(c)-1; i < j; i, j = i+1, j-1 {
		c[i], c[j] = c[j], c[i]
	}
}
