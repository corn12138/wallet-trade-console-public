package indexer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/rpc"
)

// Worker is the indexer's main loop.
//
//   - With a runner wired (NewWorkerWithRunner), each poll tick drives the
//     backfiller for every watched contract: the first tick replays history
//     from the checkpoint, every later tick processes only the blocks mined
//     since — i.e. HTTP-poll head subscription (Phase 6a.5), reusing the
//     getLogs + checkpoint + parser machinery.
//   - Without a runner (NewWorker / NewWorkerWithClient), the loop falls back
//     to the Phase 6a.1 heartbeat that pings eth_blockNumber, so the binary
//     still runs and confirms RPC connectivity when no DB is configured.
type Worker struct {
	cfg    Config
	client BlockNumberLooker

	// runner drives live indexing when set. nil runner → heartbeat.
	runner BlockRangeRunner

	// watch is the set of contract addresses scanned each tick. It starts with
	// the static contracts (factory/router) and grows at runtime via WatchCurve
	// as bonding curves are created, so it is guarded by its own RWMutex —
	// indexTick reads a snapshot while WatchCurve (called from the DB sink's
	// TokenCreated handler) appends concurrently.
	watchMu sync.RWMutex
	watch   []string

	mu      sync.Mutex
	running bool
	stopCh  chan struct{}
	doneCh  chan struct{}
}

// CurveWatcher lets the DB sink tell the worker to start scanning a newly
// created bonding curve. *Worker satisfies it; the sink holds one (optionally)
// and calls WatchCurve after persisting a TokenCreated, so Buy/Sell on that
// curve are picked up from the next tick. Defined here (same package as the
// sink) so there's no import cycle.
type CurveWatcher interface {
	WatchCurve(address string)
}

// *Worker is the CurveWatcher the DB sink calls.
var _ CurveWatcher = (*Worker)(nil)

// CurveSource lists bonding curves already persisted, for seeding the watch set
// at startup. *DBSink satisfies it (SELECT DISTINCT bonding_curve FROM tokens).
type CurveSource interface {
	KnownCurves(ctx context.Context) ([]string, error)
}

// SeedWatchset adds every already-known bonding curve to watcher so a cold
// restart resumes scanning curves created in earlier runs rather than waiting
// for a new TokenCreated. Returns the number of curves read from source. A nil
// watcher or source is a no-op (e.g. the heartbeat worker with no DB).
func SeedWatchset(ctx context.Context, watcher CurveWatcher, source CurveSource) (int, error) {
	if watcher == nil || source == nil {
		return 0, nil
	}
	curves, err := source.KnownCurves(ctx)
	if err != nil {
		return 0, err
	}
	for _, c := range curves {
		watcher.WatchCurve(c)
	}
	return len(curves), nil
}

// BlockRangeRunner indexes new blocks for one contract from the stored
// checkpoint up to chain head and returns the number of events handled.
// *Backfiller satisfies it: invoking Run on every poll tick turns the
// backfill loop into a live head-follower.
type BlockRangeRunner interface {
	Run(ctx context.Context, contractAddress string) (int, error)
}

// BlockNumberLooker is the slim RPC surface Phase 6a.1 needs. Kept
// as an interface so worker tests can substitute a fake without
// spinning a real HTTP server. The full log/event surface lands
// with Phase 6a.3.
type BlockNumberLooker interface {
	BlockNumber(ctx context.Context) (*big.Int, error)
}

// BlockNumberCaller wraps rpc.Client to satisfy BlockNumberLooker.
type BlockNumberCaller struct {
	rpc *rpc.Client
}

// NewBlockNumberCaller wraps a generic rpc.Client.
func NewBlockNumberCaller(c *rpc.Client) *BlockNumberCaller {
	return &BlockNumberCaller{rpc: c}
}

// BlockNumber issues eth_blockNumber via the underlying client.
func (b *BlockNumberCaller) BlockNumber(ctx context.Context) (*big.Int, error) {
	if b == nil || b.rpc == nil {
		return nil, errors.New("indexer: rpc client nil")
	}
	return b.rpc.BlockNumber(ctx)
}

// NewWorker builds the worker. cfg must be the result of Load().
func NewWorker(cfg Config) *Worker {
	client := NewBlockNumberCaller(rpc.NewClient(cfg.RPCURL, 0))
	return &Worker{cfg: cfg, client: client}
}

// NewWorkerWithClient builds the worker with an explicit RPC surface
// (useful for tests). cfg.RPCURL may be empty.
func NewWorkerWithClient(cfg Config, client BlockNumberLooker) *Worker {
	return &Worker{cfg: cfg, client: client}
}

// NewWorkerWithRunner builds a worker that, on every poll tick, drives runner
// for each non-empty watched contract address (live indexing). Used by
// cmd/indexer with a *Backfiller once a DB pool (for checkpoints) is wired.
func NewWorkerWithRunner(cfg Config, runner BlockRangeRunner, watch []string) *Worker {
	return &Worker{cfg: cfg, runner: runner, watch: nonEmpty(watch)}
}

// Start launches the poll loop. Non-blocking; the caller (usually
// cmd/indexer) waits on a context cancel before Stop is called.
func (w *Worker) Start(ctx context.Context) error {
	w.mu.Lock()
	if w.running {
		w.mu.Unlock()
		return errors.New("indexer: worker already running")
	}
	w.running = true
	w.stopCh = make(chan struct{})
	w.doneCh = make(chan struct{})
	w.mu.Unlock()

	slog.Info("indexer worker starting",
		"chain_id", w.cfg.ChainID,
		"rpc_url", rpc.RedactURL(w.cfg.RPCURL),
		"poll_interval_ms", w.cfg.PollIntervalMs,
		"factory", redactIfEmpty(w.cfg.Contracts.Factory),
		"router", redactIfEmpty(w.cfg.Contracts.Router),
	)

	go w.runLoop(ctx)
	return nil
}

// Stop signals the loop to exit and waits up to 5s for it to drain.
func (w *Worker) Stop(ctx context.Context) error {
	w.mu.Lock()
	if !w.running {
		w.mu.Unlock()
		return nil
	}
	close(w.stopCh)
	w.running = false
	doneCh := w.doneCh
	w.mu.Unlock()

	select {
	case <-doneCh:
		return nil
	case <-time.After(5 * time.Second):
		return errors.New("indexer: stop timed out after 5s")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *Worker) runLoop(ctx context.Context) {
	defer close(w.doneCh)

	interval := time.Duration(w.cfg.PollIntervalMs) * time.Millisecond
	if interval <= 0 {
		interval = time.Duration(DefaultPollIntervalMs) * time.Millisecond
	}

	// On a healthy tick the next poll is one interval away. On a failing tick
	// (RPC/DB outage) we back off exponentially instead of hammering the
	// endpoint every interval, escalating the log once consecutive failures
	// reach MaxReconnectAttempts. The counter resets on the next success. We
	// never exit on failure — an indexer process that gives up is worse than
	// one that keeps retrying at the capped backoff once the endpoint returns.
	consecutiveFailures := 0
	for {
		if ctx.Err() != nil {
			slog.Info("indexer worker stopping (context cancelled)")
			return
		}
		select {
		case <-w.stopCh:
			slog.Info("indexer worker stopping (stop signal)")
			return
		default:
		}

		err := w.poll(ctx)

		// Don't log a shutdown-induced poll error as a reconnect failure.
		select {
		case <-ctx.Done():
			slog.Info("indexer worker stopping (context cancelled)")
			return
		case <-w.stopCh:
			slog.Info("indexer worker stopping (stop signal)")
			return
		default:
		}

		if err == nil {
			consecutiveFailures = 0
			if !w.waitOrStop(ctx, interval) {
				return
			}
			continue
		}

		consecutiveFailures++
		delay := reconnectBackoff(interval, consecutiveFailures)
		if max := w.cfg.MaxReconnectAttempts; max > 0 && consecutiveFailures >= max {
			slog.Error("indexer poll failing persistently; still retrying at capped backoff",
				"err", err, "consecutive", consecutiveFailures, "max_reconnect", max, "retry_in", delay)
		} else {
			slog.Warn("indexer poll failed; backing off",
				"err", err, "consecutive", consecutiveFailures, "retry_in", delay)
		}
		if !w.waitOrStop(ctx, delay) {
			return
		}
	}
}

// waitOrStop sleeps for d, returning false (caller should exit) if the context
// is cancelled or Stop is signalled first, true if the full duration elapsed.
func (w *Worker) waitOrStop(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-w.stopCh:
		return false
	case <-t.C:
		return true
	}
}

// maxReconnectBackoff caps the per-failure backoff. The cap floors at the poll
// interval so the backoff is never shorter than a healthy tick.
const maxReconnectBackoff = 30 * time.Second

// reconnectBackoff returns base·2^(attempt-1), capped. attempt is the
// consecutive-failure count (1 = first failure). Doubling is iterative so a
// large failure count can't overflow.
func reconnectBackoff(base time.Duration, attempt int) time.Duration {
	limit := max(maxReconnectBackoff, base)
	d := base
	for i := 1; i < attempt; i++ {
		d *= 2
		if d >= limit {
			return limit
		}
	}
	return d
}

// poll is the per-tick body. With a runner it indexes new blocks for every
// watched contract (Phase 6a.5); otherwise it falls back to the heartbeat.
func (w *Worker) poll(ctx context.Context) error {
	if w == nil {
		return errors.New("indexer: worker nil")
	}
	if w.runner != nil {
		return w.indexTick(ctx)
	}
	if w.client == nil {
		return errors.New("indexer: block number client nil")
	}
	bn, err := w.client.BlockNumber(ctx)
	if err != nil {
		return fmt.Errorf("blockNumber: %w", err)
	}
	slog.Debug("indexer tick", "latest_block", bn.String())
	return nil
}

// indexTick drives the runner for each watched address. A failure on one
// address is logged and does not stop the others — the loop keeps ticking, so
// a transient RPC/DB error simply retries next interval. The first error is
// returned for the runLoop summary; a cancelled context aborts immediately.
func (w *Worker) indexTick(ctx context.Context) error {
	watch := w.watchSnapshot()
	var firstErr error
	total := 0
	for _, addr := range watch {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := w.runner.Run(ctx, addr)
		total += n
		if err != nil {
			slog.Warn("indexer tick: address failed", "address", addr, "err", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	if total > 0 {
		slog.Info("indexer tick processed events", "events", total, "addresses", len(watch))
	}
	return firstErr
}

// watchSnapshot copies the watch set under the read lock so a tick iterates a
// stable view while WatchCurve may be appending concurrently.
func (w *Worker) watchSnapshot() []string {
	w.watchMu.RLock()
	defer w.watchMu.RUnlock()
	out := make([]string, len(w.watch))
	copy(out, w.watch)
	return out
}

// WatchCurve adds a bonding-curve address to the watch set so the next tick
// scans it. Lower-cases, ignores blanks, and de-duplicates (the sink may emit a
// TokenCreated more than once across replays). Safe to call concurrently with a
// running tick. Satisfies CurveWatcher.
func (w *Worker) WatchCurve(address string) {
	addr := strings.ToLower(strings.TrimSpace(address))
	if addr == "" {
		return
	}
	w.watchMu.Lock()
	defer w.watchMu.Unlock()
	if slices.Contains(w.watch, addr) {
		return
	}
	w.watch = append(w.watch, addr)
	slog.Info("indexer now watching bonding curve", "address", addr, "watched", len(w.watch))
}

// nonEmpty drops blank addresses so an unconfigured Factory/Router doesn't
// produce a wasted getLogs call every tick.
func nonEmpty(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func redactIfEmpty(s string) string {
	if s == "" {
		return "(unset)"
	}
	return s
}
