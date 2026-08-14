package pricefeed

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// Worker ingests both real price sources on independent tickers.
//
// The two loops are deliberately separate: the market venue and the RPC node
// fail independently, and one being unreachable must never stop the other from
// recording truth. A failed cycle is logged and retried on the next tick; it
// never writes a placeholder row.
type Worker struct {
	cfg      Config
	store    *Store
	provider MarketProvider
	oracle   *ChainlinkReader
	log      *slog.Logger
}

// NewWorker builds an ingest worker. A nil provider disables market ingestion;
// a nil oracle reader disables oracle ingestion.
func NewWorker(cfg Config, store *Store, provider MarketProvider, oracle *ChainlinkReader, log *slog.Logger) *Worker {
	if log == nil {
		log = slog.Default()
	}
	return &Worker{cfg: cfg, store: store, provider: provider, oracle: oracle, log: log}
}

// Run blocks until ctx is cancelled, ingesting on the configured intervals.
// Both loops run one cycle immediately so a fresh deploy has data in seconds
// rather than after a full interval.
func (w *Worker) Run(ctx context.Context) error {
	if !w.cfg.Enabled {
		w.log.Info("pricefeed: disabled (PRICEFEED_ENABLED is not true); no ingestion")
		return nil
	}
	if !w.store.Available() {
		return errors.New("pricefeed: database is required for ingestion")
	}

	marketOn := w.cfg.MarketEnabled && w.provider != nil
	oracleOn := w.cfg.OracleEnabled && w.oracle != nil && len(w.cfg.Feeds) > 0

	if !marketOn && !oracleOn {
		return errors.New("pricefeed: enabled but no source is configured")
	}

	w.log.Info("pricefeed: starting",
		"symbols", w.cfg.Symbols,
		"market", marketOn,
		"marketIntervalMs", w.cfg.MarketInterval.Milliseconds(),
		"resolutions", w.cfg.Resolutions,
		"oracle", oracleOn,
		"oracleIntervalMs", w.cfg.OracleInterval.Milliseconds(),
		"feeds", len(w.cfg.Feeds),
	)

	done := make(chan struct{}, 2)
	running := 0

	if marketOn {
		running++
		go func() {
			defer func() { done <- struct{}{} }()
			w.loop(ctx, w.cfg.MarketInterval, "market", w.IngestMarketOnce)
		}()
	}
	if oracleOn {
		running++
		go func() {
			defer func() { done <- struct{}{} }()
			w.loop(ctx, w.cfg.OracleInterval, "oracle", w.IngestOracleOnce)
		}()
	}

	for i := 0; i < running; i++ {
		<-done
	}
	return ctx.Err()
}

func (w *Worker) loop(ctx context.Context, interval time.Duration, name string, cycle func(context.Context) error) {
	if interval <= 0 {
		interval = time.Minute
	}
	run := func() {
		start := time.Now()
		if err := cycle(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			// Upstream failures are expected (rate limits, transient RPC
			// errors). Log and wait for the next tick — never substitute data.
			w.log.Warn("pricefeed: ingest cycle failed", "loop", name, "err", err)
			return
		}
		w.log.Debug("pricefeed: ingest cycle done", "loop", name, "tookMs", time.Since(start).Milliseconds())
	}

	run()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

// IngestMarketOnce pulls and stores one window per symbol × resolution.
// Exported so a one-shot backfill command and tests can drive a single cycle.
func (w *Worker) IngestMarketOnce(ctx context.Context) error {
	var firstErr error
	for _, symbol := range w.cfg.Symbols {
		if !w.provider.Supports(symbol) {
			w.log.Warn("pricefeed: provider does not support symbol", "symbol", symbol, "provider", w.provider.ID())
			continue
		}
		for _, res := range w.cfg.Resolutions {
			candles, err := w.provider.FetchCandles(ctx, symbol, res, w.cfg.MarketLimit)
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				w.log.Warn("pricefeed: market fetch failed", "symbol", symbol, "resolution", res, "err", err)
				continue
			}
			written, err := w.store.UpsertMarketCandles(ctx, w.provider.ID(), symbol, res, candles)
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				w.log.Warn("pricefeed: market upsert failed", "symbol", symbol, "resolution", res, "err", err)
				continue
			}
			w.log.Debug("pricefeed: market ingested",
				"symbol", symbol, "resolution", res, "fetched", len(candles), "written", written)
		}
	}
	return firstErr
}

// IngestOracleOnce reads each configured aggregator once and appends any new
// round. Repeated polls of an unchanged round write nothing.
func (w *Worker) IngestOracleOnce(ctx context.Context) error {
	var firstErr error
	for _, feed := range w.cfg.Feeds {
		obs, err := w.oracle.LatestRound(ctx, feed)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			w.log.Warn("pricefeed: oracle read failed", "symbol", feed.Symbol, "feed", feed.Address, "err", err)
			continue
		}
		inserted, err := w.store.InsertObservation(ctx, obs)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			w.log.Warn("pricefeed: oracle insert failed", "symbol", feed.Symbol, "err", err)
			continue
		}
		if inserted {
			w.log.Info("pricefeed: new oracle round",
				"symbol", obs.Symbol, "price", obs.Price, "round", obs.RoundID, "observedAt", obs.ObservedAt)
		}
	}
	return firstErr
}
