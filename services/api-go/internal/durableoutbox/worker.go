package durableoutbox

import (
	"context"
	"time"
)

type WorkerOptions struct {
	PollInterval time.Duration
	PassTimeout  time.Duration
}

// PassResult deliberately excludes database/transport errors and event data.
// Delivery diagnostics remain on the durable row; observers report health only.
type PassResult struct {
	Result
	Failed bool
}

// Run scans durable pending rows immediately on startup and after every bounded
// pass, including failures. Correctness never depends on receiving a wakeup.
// The caller owns lifecycle and transport authorization. observe, when supplied,
// must return promptly; Publisher must honor cancellation. No worker starts
// implicitly, and restarting does not clear another worker's live leases.
func (r *Repository) Run(ctx context.Context, o Options, w WorkerOptions, p Publisher, observe func(PassResult)) error {
	if r == nil || r.pool == nil || p == nil || o.validate() != nil ||
		w.PollInterval < 100*time.Millisecond || w.PollInterval > time.Minute ||
		w.PassTimeout < time.Second || w.PassTimeout > 5*time.Minute {
		return ErrInvalidInput
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		passCtx, cancel := context.WithTimeout(ctx, w.PassTimeout)
		result, err := r.RunOnce(passCtx, o, p)
		cancel()
		if observe != nil {
			observe(PassResult{Result: result, Failed: err != nil})
		}
		// A delay after failures and full batches also bounds database pressure
		// during outages or sustained backlog; no error path becomes a busy loop.
		timer := time.NewTimer(w.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
