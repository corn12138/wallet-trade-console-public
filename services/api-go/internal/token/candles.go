// Package token's candle aggregation layer is the Go port of
// legacy NestJS token/candle-aggregation.service.ts. It powers the
// two charting endpoints from token.controller.ts:
//
//	GET /api/token/address/{address}/candles
//	GET /api/token/address/{address}/stats
//
// Read-only port — persistCandle (the writer used by the trade ingest
// pipeline) stays on NestJS until the trade-ingest path moves to Go.
package token

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/sync/errgroup"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/num"
)

// CandleResolution mirrors the NestJS string-literal union. Validation
// happens in ParseCandleResolution; any other value falls back to "1h"
// to match the controller's `validResolution` ternary.
type CandleResolution string

const (
	Resolution1m  CandleResolution = "1m"
	Resolution5m  CandleResolution = "5m"
	Resolution15m CandleResolution = "15m"
	Resolution1h  CandleResolution = "1h"
	Resolution4h  CandleResolution = "4h"
	Resolution1d  CandleResolution = "1d"
)

// resolutionMS matches the RESOLUTION_MS table in the NestJS service.
var resolutionMS = map[CandleResolution]int64{
	Resolution1m:  60 * 1000,
	Resolution5m:  5 * 60 * 1000,
	Resolution15m: 15 * 60 * 1000,
	Resolution1h:  60 * 60 * 1000,
	Resolution4h:  4 * 60 * 60 * 1000,
	Resolution1d:  24 * 60 * 60 * 1000,
}

// ParseCandleResolution mirrors the NestJS controller fallback: any
// unrecognized value (including empty) becomes 1h.
func ParseCandleResolution(raw string) CandleResolution {
	r := CandleResolution(raw)
	if _, ok := resolutionMS[r]; ok {
		return r
	}
	return Resolution1h
}

// Candle mirrors the OHLCV shape the frontend chart expects. All
// price/volume fields are strings to match the NestJS service's
// `.toString()` calls on Prisma's Float columns (avoids JS float
// drift on the wire).
type Candle struct {
	Timestamp int64  `json:"timestamp"`
	Open      string `json:"open"`
	High      string `json:"high"`
	Low       string `json:"low"`
	Close     string `json:"close"`
	Volume    string `json:"volume"`
	Trades    int    `json:"trades"`
}

// Stats mirrors the JSON returned by GET /address/{address}/stats:
// {tokenId, address, latestPrice, volume24h, priceChange24h}. LatestPrice
// is *string so we serialize JSON null when no trades exist (same as
// `latestTrade?.price?.toString() ?? null`).
type Stats struct {
	TokenID        string  `json:"tokenId"`
	Address        *string `json:"address"`
	LatestPrice    *string `json:"latestPrice"`
	Volume24h      string  `json:"volume24h"`
	PriceChange24h float64 `json:"priceChange24h"`
}

// candleRow is the raw DB projection from token_candles. Values arrive as
// NUMERIC::text decimal strings — no float64 touches a financial value.
type candleRow struct {
	Time   time.Time
	Open   string
	High   string
	Low    string
	Close  string
	Volume string
}

const candleAggregationQueryTimeout = 3 * time.Second

var (
	errCandleAggregationBusy = errors.New("token candle aggregation capacity exhausted")
	candleAggregationSlots   = make(chan struct{}, maxCandleAggregations)
)

// ListCandles mirrors candleService.getCandles: read persisted candles first,
// then fall back to a bounded recent trade window if none exist. fromTime and
// toTime are unix milliseconds.
func (r *Repository) ListCandles(ctx context.Context, tokenID string, resolution CandleResolution, limit int, fromTime, toTime int64) ([]Candle, error) {
	limit = boundedLimit(limit, defaultTokenCandleLimit, maxTokenCandleLimit)
	if r.pool == nil {
		return nil, ErrPoolUnavailable
	}
	fromTime, toTime, empty := normalizeCandleQueryRange(fromTime, toTime, time.Now().UnixMilli())
	if empty {
		return []Candle{}, nil
	}

	rows, err := r.queryCandleRows(ctx, tokenID, resolution, limit, fromTime, toTime)
	if err != nil {
		return nil, err
	}
	if len(rows) > 0 {
		out := make([]Candle, 0, len(rows))
		for _, c := range rows {
			out = append(out, Candle{
				Timestamp: c.Time.UnixMilli(),
				Open:      num.NormalizeDecimal(c.Open),
				High:      num.NormalizeDecimal(c.High),
				Low:       num.NormalizeDecimal(c.Low),
				Close:     num.NormalizeDecimal(c.Close),
				Volume:    num.NormalizeDecimal(c.Volume),
				Trades:    0,
			})
		}
		return out, nil
	}

	return r.aggregateFromTrades(ctx, tokenID, resolution, limit, fromTime, toTime)
}

func (r *Repository) queryCandleRows(ctx context.Context, tokenID string, resolution CandleResolution, limit int, fromTime, toTime int64) ([]candleRow, error) {
	// Argument list is built dynamically so the where clauses match
	// the optional fromTime/toTime semantics of the Prisma query.
	args := []any{tokenID, string(resolution)}
	where := `"tokenId" = $1 AND resolution = $2`
	if fromTime > 0 {
		args = append(args, time.UnixMilli(fromTime).UTC())
		where += fmt.Sprintf(` AND time >= $%d`, len(args))
	}
	if toTime > 0 {
		args = append(args, time.UnixMilli(toTime).UTC())
		where += fmt.Sprintf(` AND time <= $%d`, len(args))
	}
	args = append(args, limit)
	query := `
		SELECT time, open::text, high::text, low::text, close::text, volume::text
		FROM token_candles
		WHERE ` + where + `
		ORDER BY time DESC
		LIMIT $` + strconv.Itoa(len(args))

	rs, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query token_candles: %w", err)
	}
	defer rs.Close()

	out := make([]candleRow, 0, limit)
	for rs.Next() {
		var c candleRow
		if err := rs.Scan(&c.Time, &c.Open, &c.High, &c.Low, &c.Close, &c.Volume); err != nil {
			return nil, fmt.Errorf("scan token_candles: %w", err)
		}
		out = append(out, c)
	}
	if err := rs.Err(); err != nil {
		return nil, fmt.Errorf("iterate token_candles: %w", err)
	}
	return out, nil
}

// aggregateFromTrades reconstructs candles from token_trades when no
// persisted candles exist for the requested window. Matches NestJS:
// bucket by floor(timestamp_ms / resolutionMs) * resolutionMs, then
// take open/close from first/last trade by timestamp.
func (r *Repository) aggregateFromTrades(ctx context.Context, tokenID string, resolution CandleResolution, limit int, fromTime, toTime int64) ([]Candle, error) {
	resMS := resolutionMS[resolution]
	if resMS == 0 {
		resMS = resolutionMS[Resolution1h]
	}
	from, toTime, empty := boundedCandleTradeWindow(resMS, limit, fromTime, toTime, time.Now().UnixMilli())
	if empty {
		return []Candle{}, nil
	}

	select {
	case candleAggregationSlots <- struct{}{}:
		defer func() { <-candleAggregationSlots }()
	default:
		return nil, errCandleAggregationBusy
	}
	return r.queryTradeCandles(ctx, tokenID, resMS, limit, from, toTime)
}

// normalizeCandleQueryRange keeps arbitrary client timestamps within the safe
// range that pgx/PostgreSQL can encode. It deliberately preserves a missing or
// ancient lower bound so persisted latest-N candle semantics do not change.
func normalizeCandleQueryRange(fromTime, toTime, nowMS int64) (int64, int64, bool) {
	if nowMS < 1 {
		nowMS = 1
	}
	anchor := nowMS
	effectiveTo := int64(0)
	if toTime > 0 && toTime < anchor {
		anchor = toTime
		effectiveTo = toTime
	} else if toTime > 0 {
		// Future upper bounds add no useful rows and can exceed the range that
		// pgx/PostgreSQL can encode safely, so cap them at the current anchor.
		effectiveTo = anchor
	}
	if fromTime > anchor {
		return 0, effectiveTo, true
	}
	return fromTime, effectiveTo, false
}

// boundedCandleTradeWindow adds the recent lower bound used only by the
// token-trade fallback. Persisted candles are already aggregated, while raw
// trade history must stay bounded to resolution*limit database work.
func boundedCandleTradeWindow(resolutionMS int64, limit int, fromTime, toTime, nowMS int64) (int64, int64, bool) {
	fromTime, toTime, empty := normalizeCandleQueryRange(fromTime, toTime, nowMS)
	if empty {
		return 0, toTime, true
	}
	anchor := nowMS
	if anchor < 1 {
		anchor = 1
	}
	if toTime > 0 {
		anchor = toTime
	}
	earliest := anchor - resolutionMS*int64(limit)
	if earliest < 1 {
		earliest = 1
	}
	if fromTime <= 0 || fromTime < earliest {
		fromTime = earliest
	}
	return fromTime, toTime, false
}

// queryTradeCandles performs the fallback aggregation inside PostgreSQL and
// returns only the requested buckets. A short query deadline plus the package
// semaphore bounds database occupancy without truncating a busy bucket or
// loading every matching trade into the API process.
func (r *Repository) queryTradeCandles(ctx context.Context, tokenID string, resolutionMS int64, limit int, fromTime, toTime int64) ([]Candle, error) {
	args := []any{tokenID, resolutionMS}
	where := `"tokenId" = $1`
	if fromTime > 0 {
		args = append(args, time.UnixMilli(fromTime).UTC())
		where += fmt.Sprintf(` AND timestamp >= $%d`, len(args))
	}
	if toTime > 0 {
		args = append(args, time.UnixMilli(toTime).UTC())
		where += fmt.Sprintf(` AND timestamp <= $%d`, len(args))
	}
	args = append(args, limit)
	queryCtx, cancel := context.WithTimeout(ctx, candleAggregationQueryTimeout)
	defer cancel()
	rs, err := r.pool.Query(queryCtx, `
		WITH bucketed AS (
			SELECT
				(FLOOR(EXTRACT(EPOCH FROM timestamp) * 1000 / $2)::bigint * $2) AS bucket_ms,
				price,
				"ethAmount"::numeric AS eth_amount,
				timestamp,
				block_number,
				transaction_hash
			FROM token_trades
			WHERE `+where+`
		), ranked AS (
			SELECT *,
				ROW_NUMBER() OVER (
					PARTITION BY bucket_ms
					ORDER BY timestamp ASC, block_number ASC, transaction_hash ASC
				) AS open_rank,
				ROW_NUMBER() OVER (
					PARTITION BY bucket_ms
					ORDER BY timestamp DESC, block_number DESC, transaction_hash DESC
				) AS close_rank
			FROM bucketed
		)
		SELECT
			bucket_ms,
			MAX(price) FILTER (WHERE open_rank = 1)::text AS open,
			MAX(price)::text AS high,
			MIN(price)::text AS low,
			MAX(price) FILTER (WHERE close_rank = 1)::text AS close,
			SUM(eth_amount)::text AS volume_wei,
			COUNT(*)::bigint AS trades
		FROM ranked
		GROUP BY bucket_ms
		ORDER BY bucket_ms DESC
		LIMIT $`+strconv.Itoa(len(args))+`
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("query token_trades for candles: %w", err)
	}
	defer rs.Close()

	candles := make([]Candle, 0, limit)
	for rs.Next() {
		var candle Candle
		var volumeWei string
		var trades int64
		if err := rs.Scan(&candle.Timestamp, &candle.Open, &candle.High, &candle.Low, &candle.Close, &volumeWei, &trades); err != nil {
			return nil, fmt.Errorf("scan token_trades for candles: %w", err)
		}
		candle.Open = num.NormalizeDecimal(candle.Open)
		candle.High = num.NormalizeDecimal(candle.High)
		candle.Low = num.NormalizeDecimal(candle.Low)
		candle.Close = num.NormalizeDecimal(candle.Close)
		candle.Volume, err = num.WeiStringToDecimal(volumeWei)
		if err != nil {
			return nil, fmt.Errorf("normalize aggregated candle volume: %w", err)
		}
		candle.Trades = int(trades)
		candles = append(candles, candle)
	}
	if err := rs.Err(); err != nil {
		return nil, fmt.Errorf("iterate token_trades for candles: %w", err)
	}
	return candles, nil
}

// LatestPrice mirrors candleService.getLatestPrice: most-recent trade's
// price as a string, or nil if no trades.
func (r *Repository) LatestPrice(ctx context.Context, tokenID string) (*string, error) {
	if r.pool == nil {
		return nil, ErrPoolUnavailable
	}
	var price string
	err := r.pool.QueryRow(ctx, `
		SELECT price::text
		FROM token_trades
		WHERE "tokenId" = $1
		ORDER BY timestamp DESC
		LIMIT 1
	`, tokenID).Scan(&price)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("query latest price: %w", err)
	}
	s := num.NormalizeDecimal(price)
	return &s, nil
}

// Volume24h mirrors candleService.get24hVolume: sum of ethAmount over
// trades from the last 24h, as a string. Returns "0" when there are
// no trades (matches `totalVolume.toString()` on a zero accumulator).
func (r *Repository) Volume24h(ctx context.Context, tokenID string) (string, error) {
	if r.pool == nil {
		return "", ErrPoolUnavailable
	}
	since := time.Now().UTC().Add(-24 * time.Hour)
	// Exact integer SUM of the raw wei strings in SQL, then one normalization
	// to NATIVE in Go — no floats, no per-row parse drift.
	var sumWei string
	err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM("ethAmount"::numeric), 0)::text
		FROM token_trades
		WHERE "tokenId" = $1 AND timestamp >= $2
	`, tokenID, since).Scan(&sumWei)
	if err != nil {
		return "", fmt.Errorf("query 24h volume: %w", err)
	}
	vol, err := num.WeiStringToDecimal(num.NormalizeDecimal(sumWei))
	if err != nil {
		return "", fmt.Errorf("normalize 24h volume: %w", err)
	}
	return vol, nil
}

// PriceChange24h mirrors candleService.get24hPriceChange: percent change
// between the oldest trade within the last 24h and the latest overall
// trade. Returns 0 when either endpoint is missing or the old price is
// zero (matches the NestJS short-circuits).
func (r *Repository) PriceChange24h(ctx context.Context, tokenID string) (float64, error) {
	if r.pool == nil {
		return 0, ErrPoolUnavailable
	}
	since := time.Now().UTC().Add(-24 * time.Hour)
	var oldPrice string
	errOld := r.pool.QueryRow(ctx, `
		SELECT price::text FROM token_trades
		WHERE "tokenId" = $1 AND timestamp >= $2
		ORDER BY timestamp ASC LIMIT 1
	`, tokenID, since).Scan(&oldPrice)
	if errOld != nil && !errors.Is(errOld, pgx.ErrNoRows) {
		return 0, fmt.Errorf("query oldest 24h price: %w", errOld)
	}
	var newPrice string
	errNew := r.pool.QueryRow(ctx, `
		SELECT price::text FROM token_trades
		WHERE "tokenId" = $1
		ORDER BY timestamp DESC LIMIT 1
	`, tokenID).Scan(&newPrice)
	if errNew != nil && !errors.Is(errNew, pgx.ErrNoRows) {
		return 0, fmt.Errorf("query latest price: %w", errNew)
	}
	if errors.Is(errOld, pgx.ErrNoRows) || errors.Is(errNew, pgx.ErrNoRows) {
		return 0, nil
	}
	// The percent is a derived display value — exact rational math first,
	// float64 only at the very end.
	oldR, ok1 := new(big.Rat).SetString(oldPrice)
	newR, ok2 := new(big.Rat).SetString(newPrice)
	if !ok1 || !ok2 || oldR.Sign() == 0 {
		return 0, nil
	}
	diff := new(big.Rat).Sub(newR, oldR)
	diff.Quo(diff, oldR)
	diff.Mul(diff, big.NewRat(100, 1))
	f, _ := diff.Float64()
	return f, nil
}

// GetCandles is the service-layer wrapper used by the handler. Degrades
// nil-pool to []; other errors propagate.
func (s *Service) GetCandles(ctx context.Context, tokenID string, resolution CandleResolution, limit int, fromTime, toTime int64) ([]Candle, error) {
	limit = boundedLimit(limit, defaultTokenCandleLimit, maxTokenCandleLimit)
	out, err := s.repo.ListCandles(ctx, tokenID, resolution, limit, fromTime, toTime)
	if errors.Is(err, ErrPoolUnavailable) {
		return []Candle{}, nil
	}
	return out, err
}

// GetStats runs the three stat queries in parallel via errgroup, then
// assembles the response. Degrades to a zero-value Stats when the pool
// is unavailable so the token detail page still renders.
func (s *Service) GetStats(ctx context.Context, tokenID string, address *string) (Stats, error) {
	if s.repo.pool == nil {
		return Stats{TokenID: tokenID, Address: address, LatestPrice: nil, Volume24h: "0", PriceChange24h: 0}, nil
	}
	var (
		latest *string
		vol    string
		chg    float64
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		v, err := s.repo.LatestPrice(gctx, tokenID)
		if err != nil {
			return err
		}
		latest = v
		return nil
	})
	g.Go(func() error {
		v, err := s.repo.Volume24h(gctx, tokenID)
		if err != nil {
			return err
		}
		vol = v
		return nil
	})
	g.Go(func() error {
		v, err := s.repo.PriceChange24h(gctx, tokenID)
		if err != nil {
			return err
		}
		chg = v
		return nil
	})
	if err := g.Wait(); err != nil {
		return Stats{}, err
	}
	return Stats{
		TokenID:        tokenID,
		Address:        address,
		LatestPrice:    latest,
		Volume24h:      vol,
		PriceChange24h: chg,
	}, nil
}
