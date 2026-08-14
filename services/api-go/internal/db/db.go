// Package db owns the pgxpool lifecycle. Callers receive *pgxpool.Pool via
// Open and must call Close() on shutdown.
package db

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultConnectTimeout matches PRISMA_CONNECT_TIMEOUT_MS=10000 from the
// NestJS side.
const DefaultConnectTimeout = 10 * time.Second

// Open creates a pool and verifies connectivity with a Ping.
//
// A non-empty databaseURL is required; resolving DATABASE_URL/compound env
// vars lives in the runtimeenv package so this layer stays pure.
func Open(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	if databaseURL == "" {
		return nil, fmt.Errorf("db.Open: empty databaseURL")
	}

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("db.Open: parse config: %w", err)
	}
	stripPrismaParams(config)

	pingCtx, cancel := context.WithTimeout(ctx, DefaultConnectTimeout)
	defer cancel()

	pool, err := pgxpool.NewWithConfig(pingCtx, config)
	if err != nil {
		return nil, fmt.Errorf("db.Open: new pool: %w", err)
	}
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db.Open: ping: %w", err)
	}
	return pool, nil
}

// stripPrismaParams removes connection-string query params that Prisma uses but
// PostgreSQL does not understand as startup parameters. pgx forwards unknown
// query params verbatim as startup parameters, so `?schema=public` (a Prisma-ism
// that runtimeenv.Resolve appends for the compound-env path) makes a direct
// Postgres reject the connection with `unrecognized configuration parameter
// "schema"`. PgBouncer happens to tolerate it, which is why production (markets
// on Go) connects fine — but a direct Postgres (local remote-DB testing, or a
// remote without PgBouncer) does not. Prisma only uses `schema` to choose the
// search path, which already defaults to `public`, so dropping it is safe.
func stripPrismaParams(config *pgxpool.Config) {
	if config == nil || config.ConnConfig == nil || config.ConnConfig.RuntimeParams == nil {
		return
	}
	delete(config.ConnConfig.RuntimeParams, "schema")
}

// Ping wraps Pool.Ping with a per-call timeout so health handlers can't hang.
func Ping(ctx context.Context, pool *pgxpool.Pool) error {
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return pool.Ping(pingCtx)
}
