package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/outboxrun"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/durableoutbox"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/runtimeenv"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx); err != nil {
		slog.Error("campaign outbox stopped", "reason", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	cfg, err := outboxrun.Load(os.Getenv)
	if err != nil {
		return err
	}
	if !cfg.Enabled {
		slog.Info("campaign outbox disabled")
		return nil
	}
	dsn, err := runtimeenv.MustResolve()
	if err != nil {
		return outboxrun.ErrConfig
	}
	var last time.Time
	return outboxrun.Run(ctx, dsn, cfg, func(p durableoutbox.PassResult) {
		// A completed database pass is observable even when no events exist;
		// this does not claim broker consumption or browser synchronization.
		if p.Failed || p.Claimed > 0 || time.Since(last) >= time.Minute {
			slog.Info("campaign outbox pass", "failed", p.Failed, "claimed", p.Claimed, "published", p.Published, "retried", p.Retried, "deadLettered", p.DeadLettered)
			last = time.Now()
		}
	})
}
