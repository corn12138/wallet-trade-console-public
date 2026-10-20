package main

import (
	"context"
	"log/slog"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/executionregistry"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/txreview"
	"github.com/jackc/pgx/v5/pgxpool"
)

// buildExecutionRegistry enables the LG-03b authority only when its schema is
// migrated and the pointer/history audit is consistent. Otherwise it returns
// nil: current/check answer 503, create is refused on the Web, and reports are
// recorded exactly as before with no context (fail closed).
func buildExecutionRegistry(ctx context.Context, pool *pgxpool.Pool) *executionregistry.Service {
	if pool == nil {
		return nil
	}
	repo := executionregistry.NewRepository(pool)
	if err := repo.Probe(ctx); err != nil {
		slog.Warn("execution registry disabled: schema not migrated or unreadable", "err", err)
		return nil
	}
	status, err := repo.Audit(ctx)
	if err != nil {
		slog.Warn("execution registry disabled: audit unreadable", "err", err)
		return nil
	}
	if !status.Consistent {
		slog.Error("execution registry disabled: activation pointer and history disagree", "detail", status.ConsistencyDetail)
		return nil
	}
	return executionregistry.NewService(repo, executionregistry.BuiltinManifest())
}

// reviewExecution adapts the registry to txreview without txreview importing it.
type reviewExecution struct{ svc *executionregistry.Service }

func (r reviewExecution) ResolveReview(ctx context.Context, chainID int, to, data string) txreview.ReviewExecutionContext {
	got := r.svc.ResolveReview(ctx, chainID, to, data)
	out := txreview.ReviewExecutionContext{State: got.State, Reason: got.Reason, RegistryRevision: got.RegistryRevision, Lookup: got}
	for _, b := range got.Bindings {
		out.Bindings = append(out.Bindings, txreview.ReviewBinding{Operation: b.Operation, Market: b.Market, BindingDigest: b.BindingDigest})
	}
	return out
}
