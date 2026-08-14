package indexer

import (
	"context"
	"fmt"
	"log/slog"
	"math/big"
	"sync/atomic"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/rpc"
)

// Backfill retry budget. Mirrors the NestJS constants in
// indexer.service.ts: a per-batch bounded retry with exponential backoff
// (base 1s, capped at 30s). The attempt count itself is configurable via
// Config.BackfillMaxRetries (INDEXER_BACKFILL_MAX_RETRIES).
const (
	backfillRetryBaseMs = 1_000
	backfillRetryMaxMs  = 30_000
)

// ChainReader is the slim RPC surface the backfill loop needs:
// eth_blockNumber to find the head and eth_getLogs to pull each batch.
// *rpc.Client satisfies it directly; tests substitute a fake so the loop
// can be exercised without an HTTP server.
type ChainReader interface {
	BlockNumber(ctx context.Context) (*big.Int, error)
	GetLogs(ctx context.Context, filter rpc.LogFilter) ([]rpc.Log, error)
}

// Checkpointer is the persistence boundary the backfill loop drives.
// *CheckpointStore satisfies it; tests use an in-memory fake so the loop
// can run without a database.
type Checkpointer interface {
	Get(ctx context.Context, chainID int, contractAddress string, deployBlock uint64) (uint64, error)
	Save(ctx context.Context, chainID int, contractAddress string, blockNumber uint64, confirmationDepth int, deployBlock uint64) error
}

// ParsedEvent is a decoded on-chain event ready for persistence. It
// bundles the parser output with the block/tx coordinates the event
// handlers (Phase 6a.6) and the persist-layer idempotency key need.
type ParsedEvent struct {
	ChainID         int
	ContractAddress string // the emitting contract (lower-case)
	BlockNumber     int64
	TxHash          string
	LogIndex        int
	Parsed          ParsedIndexedLog
}

// EventSink consumes parsed events. Phase 6a.4 ships CountingSink (no
// persistence); Phase 6a.6 wires the DB-writing implementation behind
// this same interface.
type EventSink interface {
	HandleEvent(ctx context.Context, ev ParsedEvent) error
}

// CountingSink is the default EventSink: it tallies events without
// persisting them. Lets the backfill loop run end-to-end (and be
// integration-smoke-tested against a live RPC) before the DB handlers
// land in Phase 6a.6.
type CountingSink struct{ count atomic.Int64 }

// HandleEvent increments the tally.
func (s *CountingSink) HandleEvent(_ context.Context, _ ParsedEvent) error {
	s.count.Add(1)
	return nil
}

// Count returns the number of events handled so far.
func (s *CountingSink) Count() int64 { return s.count.Load() }

// Backfiller replays historical logs for a contract in bounded block
// batches, parsing each log and advancing the checkpoint at every batch
// boundary. Mirrors backfill() in legacy NestJS indexer/indexer.service.ts.
type Backfiller struct {
	cfg         Config
	reader      ChainReader
	checkpoints Checkpointer
	sink        EventSink

	// sleep is the backoff timer between failed getLogs attempts.
	// Injectable so tests don't wait real wall-clock seconds.
	sleep func(ctx context.Context, d time.Duration) error
}

// NewBackfiller wires the loop. cfg supplies ChainID, BatchSize,
// ConfirmationDepth, DeployBlock and BackfillMaxRetries.
func NewBackfiller(cfg Config, reader ChainReader, checkpoints Checkpointer, sink EventSink) *Backfiller {
	return &Backfiller{
		cfg:         cfg,
		reader:      reader,
		checkpoints: checkpoints,
		sink:        sink,
		sleep:       sleepCtx,
	}
}

// Run replays every log for contractAddress from the stored checkpoint
// (exclusive) up to the current chain head (inclusive), in batches of
// cfg.BatchSize. Returns the number of events handed to the sink.
//
// The checkpoint is saved after each batch — including empty ones — so a
// re-run skips ranges we've already scanned. Reorg safety comes from the
// checkpoint store rewinding last_safe_block by ConfirmationDepth, so the
// next Run re-scans the confirmation window even though we backfill all
// the way to head here (parity with the NestJS loop).
//
// A context cancellation (shutdown) aborts cleanly between batches and
// surfaces ctx.Err(); the events already handed to the sink and the
// checkpoints already saved are durable.
func (b *Backfiller) Run(ctx context.Context, contractAddress string) (int, error) {
	latest, err := b.headBlock(ctx)
	if err != nil {
		return 0, err
	}

	checkpoint, err := b.checkpoints.Get(ctx, b.cfg.ChainID, contractAddress, b.cfg.DeployBlock)
	if err != nil {
		return 0, fmt.Errorf("backfill checkpoint: %w", err)
	}

	slog.Info("backfill starting",
		"contract", contractAddress,
		"checkpoint", checkpoint,
		"latest_block", latest,
	)

	if checkpoint >= latest {
		slog.Info("backfill already up to date", "contract", contractAddress, "block", latest)
		return 0, nil
	}

	batch := b.cfg.BatchSize
	if batch == 0 {
		batch = DefaultBatchSize
	}

	processed := 0
	start := checkpoint + 1
	for start <= latest {
		if err := ctx.Err(); err != nil {
			slog.Warn("backfill aborting: context cancelled", "contract", contractAddress, "at_block", start)
			return processed, err
		}

		end := start + batch - 1
		// Clamp to head, and guard the uint64 wrap when start+batch
		// overflows near the top of the range.
		if end < start || end > latest {
			end = latest
		}

		logs, err := b.fetchLogsWithRetry(ctx, contractAddress, start, end)
		if err != nil {
			return processed, err
		}

		slog.Debug("backfill batch", "contract", contractAddress, "from", start, "to", end, "events", len(logs))

		for _, raw := range logs {
			event, err := toParsedEvent(b.cfg.ChainID, contractAddress, raw)
			if err != nil {
				return processed, fmt.Errorf("backfill normalize event %s#%d: %w", raw.TxHash, raw.LogIndex, err)
			}
			if err := b.sink.HandleEvent(ctx, event); err != nil {
				return processed, fmt.Errorf("backfill handle event %s#%d: %w", raw.TxHash, raw.LogIndex, err)
			}
			processed++
		}

		if err := b.checkpoints.Save(ctx, b.cfg.ChainID, contractAddress, end, b.cfg.ConfirmationDepth, b.cfg.DeployBlock); err != nil {
			return processed, fmt.Errorf("backfill save checkpoint %d: %w", end, err)
		}

		if end == latest {
			break
		}
		start = end + 1
	}

	slog.Info("backfill complete", "contract", contractAddress, "events", processed, "block", latest)
	return processed, nil
}

// headBlock reads the current chain head and converts it to uint64,
// rejecting a negative or out-of-range value rather than wrapping it.
func (b *Backfiller) headBlock(ctx context.Context) (uint64, error) {
	bn, err := b.reader.BlockNumber(ctx)
	if err != nil {
		return 0, fmt.Errorf("backfill blockNumber: %w", err)
	}
	if bn == nil || bn.Sign() < 0 {
		return 0, fmt.Errorf("backfill: invalid latest block %v", bn)
	}
	if !bn.IsUint64() {
		return 0, fmt.Errorf("backfill: latest block %s exceeds uint64", bn.String())
	}
	return bn.Uint64(), nil
}

// fetchLogsWithRetry pulls one batch with a bounded retry budget and
// exponential backoff. A persistent RPC failure surfaces as an error
// (the loop aborts and the checkpoint stays put) rather than silently
// skipping the range — same contract as fetchLogsWithRetry() in the
// NestJS service.
func (b *Backfiller) fetchLogsWithRetry(ctx context.Context, contractAddress string, from, to uint64) ([]rpc.Log, error) {
	maxRetries := max(b.cfg.BackfillMaxRetries, 1)

	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		logs, err := b.reader.GetLogs(ctx, rpc.LogFilter{
			Address:   contractAddress,
			FromBlock: from,
			ToBlock:   to,
		})
		if err == nil {
			return logs, nil
		}
		lastErr = err
		slog.Warn("backfill getLogs failed",
			"contract", contractAddress, "from", from, "to", to,
			"attempt", attempt, "max", maxRetries, "err", err,
		)
		if attempt == maxRetries {
			break
		}
		if err := b.sleep(ctx, backfillRetryDelay(attempt)); err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("backfill: getLogs %d-%d on %s failed after %d attempts: %w",
		from, to, contractAddress, maxRetries, lastErr)
}

// backfillRetryDelay returns base*2^(attempt-1), capped at the max. The
// doubling is computed iteratively so a large configured retry count
// can't overflow the shift.
func backfillRetryDelay(attempt int) time.Duration {
	ms := backfillRetryBaseMs
	for i := 1; i < attempt; i++ {
		ms *= 2
		if ms >= backfillRetryMaxMs {
			return time.Duration(backfillRetryMaxMs) * time.Millisecond
		}
	}
	return time.Duration(ms) * time.Millisecond
}

// sleepCtx is a context-aware sleep: it returns early with ctx.Err() if
// the context is cancelled before the duration elapses.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
