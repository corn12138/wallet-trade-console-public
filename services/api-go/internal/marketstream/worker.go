package marketstream

import (
	"context"
	"log/slog"
	"time"
)

// ComputeFunc returns the latest snapshot for a tracked market. DB-backed in
// production (reads the markets/trading projections); injected so the worker is
// testable without a database.
type ComputeFunc func(ctx context.Context, m TrackedMarket) (*Snapshot, error)

// TrackedFunc resolves which markets to refresh each tick (from
// MARKET_STREAM_SYMBOLS / chain config in production).
type TrackedFunc func(ctx context.Context) ([]TrackedMarket, error)

// DefaultRefreshInterval mirrors marketStream.refreshIntervalMs (5s).
const DefaultRefreshInterval = 5 * time.Second

// Worker is the Go port of MarketStreamWorkerService: on an interval it computes
// a snapshot per tracked market and publishes it through the driver, which fans
// it out to the realtime gateway's subscribers.
type Worker struct {
	driver   Driver
	tracked  TrackedFunc
	compute  ComputeFunc
	interval time.Duration

	stop chan struct{}
	done chan struct{}
}

func NewWorker(driver Driver, tracked TrackedFunc, compute ComputeFunc, interval time.Duration) *Worker {
	if interval <= 0 {
		interval = DefaultRefreshInterval
	}
	return &Worker{driver: driver, tracked: tracked, compute: compute, interval: interval}
}

// Start connects the driver and launches the refresh loop (immediate first tick,
// matching the NestJS worker).
func (w *Worker) Start(ctx context.Context) error {
	if err := w.driver.Connect(ctx); err != nil {
		return err
	}
	w.stop = make(chan struct{})
	w.done = make(chan struct{})
	go w.loop(ctx)
	return nil
}

func (w *Worker) loop(ctx context.Context) {
	defer close(w.done)
	w.refresh(ctx)
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-ctx.Done():
			return
		case <-t.C:
			w.refresh(ctx)
		}
	}
}

func (w *Worker) refresh(ctx context.Context) {
	markets, err := w.tracked(ctx)
	if err != nil {
		slog.Warn("market-stream: resolve tracked markets failed", "err", err)
		return
	}
	for _, m := range markets {
		snap, err := w.compute(ctx, m)
		if err != nil {
			slog.Warn("market-stream: compute snapshot failed", "symbol", m.Symbol, "err", err)
			continue
		}
		if snap == nil {
			continue
		}
		if err := w.driver.PublishSnapshot(ctx, snap); err != nil {
			slog.Warn("market-stream: publish snapshot failed", "symbol", m.Symbol, "err", err)
		}
	}
}

// Stop halts the loop and disconnects the driver. Idempotent.
func (w *Worker) Stop(ctx context.Context) error {
	if w.stop != nil {
		close(w.stop)
		<-w.done
		w.stop = nil
	}
	return w.driver.Disconnect(ctx)
}
