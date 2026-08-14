package pricefeed

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNoPool is returned when the store was built without a database. Callers
// surface it as an empty series plus an "unavailable" status — never as data.
var ErrNoPool = errors.New("pricefeed: no database pool configured")

// Store persists and reads the two real price sources.
type Store struct{ pool *pgxpool.Pool }

// NewStore builds a store. A nil pool is valid and makes every method return
// ErrNoPool, which the service converts into an honest "unavailable" status.
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Available reports whether a database is wired.
func (s *Store) Available() bool { return s != nil && s.pool != nil }

// UpsertMarketCandles writes a provider's candles idempotently. Re-ingesting an
// overlapping window updates rows in place, so the still-forming newest bucket
// converges to its final values instead of duplicating. Returns rows written.
func (s *Store) UpsertMarketCandles(ctx context.Context, source, symbol string, res Resolution, candles []Candle) (int64, error) {
	if !s.Available() {
		return 0, ErrNoPool
	}
	if len(candles) == 0 {
		return 0, nil
	}

	batch := &pgx.Batch{}
	for _, c := range candles {
		batch.Queue(`
			INSERT INTO market_candles
				(id, source, symbol, resolution, bucket_start, open, high, low, close, volume, quote_volume, closed, ingested_at)
			VALUES
				(gen_random_uuid()::text, $1, $2, $3, $4, $5::numeric, $6::numeric, $7::numeric, $8::numeric, $9::numeric, $10::numeric, $11, NOW())
			ON CONFLICT (source, symbol, resolution, bucket_start) DO UPDATE SET
				open = EXCLUDED.open,
				high = EXCLUDED.high,
				low = EXCLUDED.low,
				close = EXCLUDED.close,
				volume = EXCLUDED.volume,
				quote_volume = EXCLUDED.quote_volume,
				closed = EXCLUDED.closed,
				ingested_at = NOW()
		`,
			source, NormalizeSymbol(symbol), string(res),
			time.UnixMilli(c.Timestamp).UTC(),
			c.Open, c.High, c.Low, c.Close, c.Volume, c.QuoteVolume, c.Closed,
		)
	}

	br := s.pool.SendBatch(ctx, batch)
	defer br.Close()

	var written int64
	for range candles {
		tag, err := br.Exec()
		if err != nil {
			return written, fmt.Errorf("pricefeed: upsert market candle: %w", err)
		}
		written += tag.RowsAffected()
	}
	return written, nil
}

// InsertObservation appends one Chainlink round. Repeated polls of the same
// round are a no-op (ON CONFLICT DO NOTHING), so the table records genuine
// oracle updates rather than polling noise. Returns true when a new round landed.
func (s *Store) InsertObservation(ctx context.Context, obs Observation) (bool, error) {
	if !s.Available() {
		return false, ErrNoPool
	}
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO oracle_price_observations
			(id, source, symbol, chain_id, feed_address, round_id, price, decimals, observed_at, recorded_at)
		VALUES
			(gen_random_uuid()::text, $1, $2, $3, $4, $5, $6::numeric, $7, $8, NOW())
		ON CONFLICT (chain_id, feed_address, round_id) DO NOTHING
	`,
		string(SourceOracle), obs.Symbol, obs.ChainID, obs.FeedAddress,
		obs.RoundID, obs.Price, obs.Decimals, obs.ObservedAt,
	)
	if err != nil {
		return false, fmt.Errorf("pricefeed: insert observation: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// ListMarketCandles reads stored provider candles newest-first.
func (s *Store) ListMarketCandles(ctx context.Context, source, symbol string, res Resolution, limit int) ([]Candle, error) {
	if !s.Available() {
		return nil, ErrNoPool
	}
	rows, err := s.pool.Query(ctx, `
		SELECT bucket_start, open::text, high::text, low::text, close::text,
		       volume::text, quote_volume::text, closed
		FROM market_candles
		WHERE source = $1 AND symbol = $2 AND resolution = $3
		ORDER BY bucket_start DESC
		LIMIT $4
	`, source, NormalizeSymbol(symbol), string(res), limit)
	if err != nil {
		return nil, fmt.Errorf("pricefeed: list market candles: %w", err)
	}
	defer rows.Close()

	out := make([]Candle, 0, limit)
	for rows.Next() {
		var (
			bucket time.Time
			c      Candle
		)
		if err := rows.Scan(&bucket, &c.Open, &c.High, &c.Low, &c.Close, &c.Volume, &c.QuoteVolume, &c.Closed); err != nil {
			return nil, fmt.Errorf("pricefeed: scan market candle: %w", err)
		}
		c.Timestamp = bucket.UTC().UnixMilli()
		out = append(out, c)
	}
	return out, rows.Err()
}

// AggregateOracleCandles builds OHLC buckets from stored Chainlink rounds.
//
// Aggregation happens in SQL over the raw rounds rather than from a
// materialized table: oracle updates are sparse (one per heartbeat/deviation),
// so there is nothing to precompute, and deriving on read guarantees a candle
// can never drift from the observations it claims to summarize.
//
// Volume is intentionally absent — an oracle reports a price, not traded size.
// Emitting a volume here would be inventing a number, so both volume fields
// stay "0" and the UI labels the source.
func (s *Store) AggregateOracleCandles(ctx context.Context, symbol string, res Resolution, limit int) ([]Candle, error) {
	if !s.Available() {
		return nil, ErrNoPool
	}
	// to_timestamp(floor(epoch / width) * width) is the same absolute,
	// epoch-anchored bucketing Resolution.BucketStart applies in Go.
	width := int64(res.Duration() / time.Second)
	if width <= 0 {
		return nil, fmt.Errorf("pricefeed: bad resolution %q", res)
	}

	rows, err := s.pool.Query(ctx, `
		WITH bucketed AS (
			SELECT
				to_timestamp(floor(extract(epoch FROM observed_at) / $3) * $3) AS bucket_start,
				price,
				observed_at,
				ROW_NUMBER() OVER (PARTITION BY floor(extract(epoch FROM observed_at) / $3) ORDER BY observed_at ASC, round_id ASC) AS rn_first,
				ROW_NUMBER() OVER (PARTITION BY floor(extract(epoch FROM observed_at) / $3) ORDER BY observed_at DESC, round_id DESC) AS rn_last
			FROM oracle_price_observations
			WHERE symbol = $1 AND source = $2
		)
		SELECT
			bucket_start,
			MAX(price) FILTER (WHERE rn_first = 1)::text AS open,
			MAX(price)::text                             AS high,
			MIN(price)::text                             AS low,
			MAX(price) FILTER (WHERE rn_last = 1)::text  AS close,
			COUNT(*)                                     AS rounds
		FROM bucketed
		GROUP BY bucket_start
		ORDER BY bucket_start DESC
		LIMIT $4
	`, NormalizeSymbol(symbol), string(SourceOracle), width, limit)
	if err != nil {
		return nil, fmt.Errorf("pricefeed: aggregate oracle candles: %w", err)
	}
	defer rows.Close()

	out := make([]Candle, 0, limit)
	for rows.Next() {
		var (
			bucket time.Time
			rounds int64
			c      Candle
		)
		if err := rows.Scan(&bucket, &c.Open, &c.High, &c.Low, &c.Close, &rounds); err != nil {
			return nil, fmt.Errorf("pricefeed: scan oracle candle: %w", err)
		}
		c.Timestamp = bucket.UTC().UnixMilli()
		c.Volume = "0"
		c.QuoteVolume = "0"
		c.Closed = true
		out = append(out, c)
	}
	return out, rows.Err()
}

// LatestObservation returns the newest stored round for a symbol.
func (s *Store) LatestObservation(ctx context.Context, symbol string) (Observation, bool, error) {
	if !s.Available() {
		return Observation{}, false, ErrNoPool
	}
	var obs Observation
	err := s.pool.QueryRow(ctx, `
		SELECT symbol, chain_id, feed_address, round_id, price::text, decimals, observed_at
		FROM oracle_price_observations
		WHERE symbol = $1
		ORDER BY observed_at DESC
		LIMIT 1
	`, NormalizeSymbol(symbol)).Scan(
		&obs.Symbol, &obs.ChainID, &obs.FeedAddress, &obs.RoundID, &obs.Price, &obs.Decimals, &obs.ObservedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Observation{}, false, nil
	}
	if err != nil {
		return Observation{}, false, fmt.Errorf("pricefeed: latest observation: %w", err)
	}
	obs.ObservedAt = obs.ObservedAt.UTC()
	return obs, true, nil
}

// CoverageRow summarizes what is actually stored for one source/symbol/resolution.
// The status endpoint uses it so an empty chart can always be explained.
type CoverageRow struct {
	Source     string     `json:"source"`
	Symbol     string     `json:"symbol"`
	Resolution string     `json:"resolution"`
	Candles    int64      `json:"candles"`
	OldestAt   *time.Time `json:"oldestAt"`
	NewestAt   *time.Time `json:"newestAt"`
}

// MarketCoverage reports stored provider-candle coverage.
func (s *Store) MarketCoverage(ctx context.Context) ([]CoverageRow, error) {
	if !s.Available() {
		return nil, ErrNoPool
	}
	rows, err := s.pool.Query(ctx, `
		SELECT source, symbol, resolution, COUNT(*), MIN(bucket_start), MAX(bucket_start)
		FROM market_candles
		GROUP BY source, symbol, resolution
		ORDER BY symbol, resolution, source
	`)
	if err != nil {
		return nil, fmt.Errorf("pricefeed: market coverage: %w", err)
	}
	defer rows.Close()

	out := []CoverageRow{}
	for rows.Next() {
		var r CoverageRow
		if err := rows.Scan(&r.Source, &r.Symbol, &r.Resolution, &r.Candles, &r.OldestAt, &r.NewestAt); err != nil {
			return nil, fmt.Errorf("pricefeed: scan coverage: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// OracleCoverage reports stored oracle-round coverage per symbol.
func (s *Store) OracleCoverage(ctx context.Context) ([]CoverageRow, error) {
	if !s.Available() {
		return nil, ErrNoPool
	}
	rows, err := s.pool.Query(ctx, `
		SELECT symbol, COUNT(*), MIN(observed_at), MAX(observed_at)
		FROM oracle_price_observations
		GROUP BY symbol
		ORDER BY symbol
	`)
	if err != nil {
		return nil, fmt.Errorf("pricefeed: oracle coverage: %w", err)
	}
	defer rows.Close()

	out := []CoverageRow{}
	for rows.Next() {
		r := CoverageRow{Source: string(SourceOracle), Resolution: "raw"}
		if err := rows.Scan(&r.Symbol, &r.Candles, &r.OldestAt, &r.NewestAt); err != nil {
			return nil, fmt.Errorf("pricefeed: scan oracle coverage: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
