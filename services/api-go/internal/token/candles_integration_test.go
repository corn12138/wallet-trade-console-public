package token

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestStreamTradeCandlesPostgres(t *testing.T) {
	dsn := os.Getenv("TOKEN_CANDLES_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TOKEN_CANDLES_TEST_DATABASE_URL is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse integration database URL: %v", err)
	}
	// CI reuses the Prisma DATABASE_URL, whose schema query parameter is not a
	// PostgreSQL startup parameter. Strip it for both direct pgx connections.
	delete(config.ConnConfig.RuntimeParams, "schema")
	admin, err := pgx.ConnectConfig(ctx, config.ConnConfig.Copy())
	if err != nil {
		t.Fatalf("connect integration database: %v", err)
	}
	defer admin.Close(ctx)

	// A unique schema keeps this opt-in test isolated even when developers share
	// one disposable Postgres instance across multiple test processes.
	schema := fmt.Sprintf("token_candles_it_%d", time.Now().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		t.Fatalf("create integration schema: %v", err)
	}
	defer func() {
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop integration schema: %v", err)
		}
	}()
	if _, err := admin.Exec(ctx, `CREATE TABLE `+quotedSchema+`.token_trades (
		"tokenId" text NOT NULL,
		price numeric(38, 18) NOT NULL,
		"ethAmount" text NOT NULL,
		block_number bigint NOT NULL,
		transaction_hash text NOT NULL,
		timestamp timestamp(3) without time zone NOT NULL
	)`); err != nil {
		t.Fatalf("create token_trades: %v", err)
	}

	config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET search_path TO "+quotedSchema)
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("open integration pool: %v", err)
	}
	defer pool.Close()

	// MaxInt64 cannot be represented safely by PostgreSQL's timestamp wire
	// encoding. ListCandles must reject this future-only range before its first
	// persisted-candle query; this test schema intentionally has no
	// token_candles table so any accidental query is observable as an error.
	const maxInt64 = int64(1<<63 - 1)
	future, err := NewRepository(pool).ListCandles(
		ctx,
		"tok-future",
		Resolution1m,
		1,
		maxInt64,
		maxInt64,
	)
	if err != nil {
		t.Fatalf("list overflowing future window: %v", err)
	}
	if len(future) != 0 {
		t.Fatalf("future candles = %d, want 0", len(future))
	}

	if _, err := pool.Exec(ctx, `CREATE TABLE token_candles (
		"tokenId" text NOT NULL,
		resolution text NOT NULL,
		time timestamp(3) without time zone NOT NULL,
		open numeric(38, 18) NOT NULL,
		high numeric(38, 18) NOT NULL,
		low numeric(38, 18) NOT NULL,
		close numeric(38, 18) NOT NULL,
		volume numeric(38, 18) NOT NULL
	)`); err != nil {
		t.Fatalf("create token_candles: %v", err)
	}
	oldCandleTime := time.Now().UTC().Add(-72 * time.Hour).Truncate(time.Minute)
	if _, err := pool.Exec(ctx, `INSERT INTO token_candles
		("tokenId", resolution, time, open, high, low, close, volume)
		VALUES ('tok-old', '15m', $1, 1, 2, 0.5, 1.5, 3)`, oldCandleTime); err != nil {
		t.Fatalf("insert old persisted candle: %v", err)
	}
	persisted, err := NewRepository(pool).ListCandles(ctx, "tok-old", Resolution15m, 200, 0, 0)
	if err != nil {
		t.Fatalf("list old persisted candle: %v", err)
	}
	if len(persisted) != 1 || persisted[0].Timestamp != oldCandleTime.UnixMilli() {
		t.Fatalf("old persisted candles = %+v, want latest historical row", persisted)
	}

	for _, row := range []struct {
		price     string
		ethAmount string
		at        int64
	}{
		{price: "1.5", ethAmount: "1000000000000000000", at: 61_000},
		{price: "2", ethAmount: "250000000000000000", at: 62_000},
		{price: "1.25", ethAmount: "500000000000000000", at: 63_000},
		{price: "3", ethAmount: "2000000000000000000", at: 121_000},
	} {
		if _, err := pool.Exec(ctx, `INSERT INTO token_trades ("tokenId", price, "ethAmount", block_number, transaction_hash, timestamp) VALUES ($1, $2, $3, $4, $5, $6)`,
			"tok-1", row.price, row.ethAmount, row.at, fmt.Sprintf("0x%064x", row.at), time.UnixMilli(row.at).UTC()); err != nil {
			t.Fatalf("insert trade: %v", err)
		}
	}

	got, err := NewRepository(pool).queryTradeCandles(ctx, "tok-1", resolutionMS[Resolution1m], 2, 1, 0)
	if err != nil {
		t.Fatalf("stream candles: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("candles = %d, want 2", len(got))
	}
	if got[0].Timestamp != 120_000 || got[0].Open != "3" || got[0].Close != "3" || got[0].Volume != "2" || got[0].Trades != 1 {
		t.Fatalf("newest candle = %+v", got[0])
	}
	if got[1].Timestamp != 60_000 || got[1].Open != "1.5" || got[1].High != "2" || got[1].Low != "1.25" || got[1].Close != "1.25" || got[1].Volume != "1.75" || got[1].Trades != 3 {
		t.Fatalf("older candle = %+v", got[1])
	}

	// A busy bucket must aggregate completely instead of reproducing the old
	// 10k-row truncation/error behavior.
	if _, err := pool.Exec(ctx, `
		INSERT INTO token_trades ("tokenId", price, "ethAmount", block_number, transaction_hash, timestamp)
		SELECT 'tok-busy', 1, '1', n, 'busy-' || n::text,
		       timestamp '1970-01-01 00:03:01' + n * interval '1 microsecond'
		FROM generate_series(1, 10001) AS n
	`); err != nil {
		t.Fatalf("insert busy bucket: %v", err)
	}
	busy, err := NewRepository(pool).queryTradeCandles(ctx, "tok-busy", resolutionMS[Resolution1m], 1, 1, 0)
	if err != nil {
		t.Fatalf("aggregate busy bucket: %v", err)
	}
	if len(busy) != 1 || busy[0].Trades != 10_001 || busy[0].Volume != "0.000000000000010001" {
		t.Fatalf("busy candle = %+v", busy)
	}
}
