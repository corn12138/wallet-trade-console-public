package outboxrun

import (
	"context"
	"errors"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/outboxwire"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/durableoutbox"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Run owns its pool and joins the worker before closing it. Diagnostics are
// closed codes because connection errors may contain credentials or addresses.
func Run(ctx context.Context, databaseURL string, cfg Config, observe func(durableoutbox.PassResult)) error {
	if !cfg.Enabled {
		return nil
	}
	if cfg.Delivery.Source != "campaign" || cfg.Delivery.Batch < 1 || cfg.Delivery.Batch > 4 || databaseURL == "" {
		return ErrConfig
	}
	pc, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return ErrConfig
	}
	delete(pc.ConnConfig.RuntimeParams, "schema")
	pc.ConnConfig.RuntimeParams["search_path"] = "pg_catalog,public,pg_temp"
	pc.ConnConfig.ConnectTimeout = 5 * time.Second
	pc.MaxConns, pc.MinConns = 4, 0
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return ErrStore
	}
	defer pool.Close()
	startup, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err = pool.Ping(startup); err != nil {
		return ErrStore
	}
	if err = CheckRole(startup, pool); err != nil {
		return err
	}
	// An unmigrated schema must fail before a long-running process can appear
	// healthy. This probes the columns used by claiming and publishing.
	rows, err := pool.Query(startup, `SELECT id,source,aggregate_id,aggregate_version,topic,owner_scope,payload_version,payload,occurred_at,available_at,published_at,lease_owner,lease_until,attempts,error_code,dead_lettered_at FROM public.durable_outbox LIMIT 0`)
	if err != nil {
		return ErrStore
	}
	rows.Close()
	if rows.Err() != nil {
		return ErrStore
	}
	cancel()
	err = durableoutbox.NewRepository(pool).Run(ctx, cfg.Delivery, cfg.Worker, outboxwire.New(pool), observe)
	if errors.Is(err, context.Canceled) && ctx.Err() != nil {
		return nil
	}
	return err
}
