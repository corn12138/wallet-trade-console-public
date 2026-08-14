// Package trading is the Go port of legacy NestJS trading/
// trading.service.ts data queries used by the Markets module.
//
// Ported read paths (by phase):
//   - 1b: ListOpenPositions, ListRecentTrades (aggregation feeds)
//   - 1c: ListOrderbookOrders, ListRecentTradesForSymbol
//     (REST realtime-snapshot fallback)
//
// The full TradingService (full history, account positions/orders) lands
// in later phases.
//
// Column names match the Prisma schema exactly: snake_case chain_id is
// the only @map'ped column; everything else is a quoted camelCase
// identifier in Postgres ("isLong", "sizeDelta", "triggerPrice",
// "txHash", "createdAt", etc.).
package trading

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PerpPosition is the minimal view of perp_positions needed for
// market open-interest aggregation.
type PerpPosition struct {
	Token  string
	IsLong bool
	Size   string
}

// PerpPositionForAccount is the wider perp_positions projection that
// portfolio.collectRawAssets consumes — same source table, more
// columns. Size / Collateral / EntryPrice / MarkPrice / PNL are
// BigInt-as-string in USD with 30 decimals; the FE converts on render
// via parseUsd30.
type PerpPositionForAccount struct {
	ID         string
	ChainID    int
	Account    string
	Token      string
	IsLong     bool
	Size       string
	Collateral string
	EntryPrice string
	MarkPrice  string
	PNL        string
	Status     string
	TxHash     string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// PerpTrade is the minimal view of perp_trades needed for 24h volume
// aggregation.
type PerpTrade struct {
	Token     string
	SizeDelta string
}

// PerpOrder is the projection of perp_orders needed for orderbook
// construction. Only orders with a triggerPrice contribute to the book.
// PerpOrder is the perp_orders row projection. ListOrderbookOrders
// populates only IsLong/SizeDelta/TriggerPrice (the orderbook view,
// which filters NULL triggerPrice anyway); ListPendingOrdersForAccount
// populates the full set. TriggerPrice is *string because the column
// is nullable in Prisma (Limit/Stop orders only).
type PerpOrder struct {
	ID           string
	Token        string
	IsLong       bool
	Type         string
	SizeDelta    string
	TriggerPrice *string
	Status       string
	CreatedAt    time.Time
}

// PerpTradeFull is the row projection used to render trade history in
// the REST realtime-snapshot fallback. Matches TradingTradeHistoryView
// in legacy NestJS trading/trading.types.ts.
type PerpTradeFull struct {
	ID        string
	Token     string
	IsLong    bool
	Type      string
	SizeDelta string
	Price     string
	Fee       string
	PNL       *string
	TxHash    string
	CreatedAt time.Time
}

// Repository wraps the pgx pool with the two perp-table queries.
type Repository struct {
	pool *pgxpool.Pool
	// includePaperOrders opts the ORDER read models into rows without a
	// txHash (the papertrade seeder's fake book). Default false: only
	// chain-evidenced orders reach the orderbook/pending-orders views.
	// cmd/api flips it from PAPER_TRADE_ENABLED for local demos.
	includePaperOrders bool
}

// SetIncludePaperOrders opts order reads into paper (txHash-less) rows.
func (r *Repository) SetIncludePaperOrders(include bool) {
	if r != nil {
		r.includePaperOrders = include
	}
}

// NewRepository binds the repo to a pgxpool. A nil pool yields a
// non-nil Repository whose methods all return ErrPoolUnavailable —
// matches the NestJS "skip on bootstrap" degradation path so callers
// can still serve degraded responses (e.g. empty snapshots) when DB
// envs aren't configured locally.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// ErrPoolUnavailable is returned when methods are called against a
// repo built with a nil pool. Callers may swallow this to emit a
// degraded response.
var ErrPoolUnavailable = fmt.Errorf("trading repository: database pool not configured")

// ListOpenPositions returns OPEN perp_positions filtered by chain and
// optional symbol allow-list. nil/empty symbols means "all symbols".
func (r *Repository) ListOpenPositions(ctx context.Context, symbols []string, chainID *int) ([]PerpPosition, error) {
	if r.pool == nil {
		return nil, ErrPoolUnavailable
	}
	query, args := buildPerpPositionsQuery(symbols, chainID)
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query perp_positions: %w", err)
	}
	defer rows.Close()

	out := make([]PerpPosition, 0)
	for rows.Next() {
		var p PerpPosition
		if err := rows.Scan(&p.Token, &p.IsLong, &p.Size); err != nil {
			return nil, fmt.Errorf("scan perp_positions: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate perp_positions: %w", err)
	}
	return out, nil
}

// ListOpenPositionsForAccount returns OPEN perp_positions for a given
// account address with optional chain filter. Used by portfolio's
// inventory collection. Ordered createdAt DESC to match the NestJS
// Prisma call.
func (r *Repository) ListOpenPositionsForAccount(ctx context.Context, account string, chainID *int) ([]PerpPositionForAccount, error) {
	if r.pool == nil {
		return nil, ErrPoolUnavailable
	}
	args := []any{account, "OPEN"}
	clauses := []string{`account = $1`, `status = $2`}
	if chainID != nil {
		args = append(args, *chainID)
		clauses = append(clauses, `chain_id = $3`)
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, chain_id, account, token, "isLong", size,
		       collateral, "entryPrice", "markPrice", pnl,
		       status, "txHash", "createdAt", "updatedAt"
		FROM perp_positions
		WHERE `+strings.Join(clauses, " AND ")+`
		ORDER BY "createdAt" DESC
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("query perp_positions (account): %w", err)
	}
	defer rows.Close()
	out := make([]PerpPositionForAccount, 0)
	for rows.Next() {
		var p PerpPositionForAccount
		if err := rows.Scan(&p.ID, &p.ChainID, &p.Account, &p.Token, &p.IsLong,
			&p.Size, &p.Collateral, &p.EntryPrice, &p.MarkPrice, &p.PNL,
			&p.Status, &p.TxHash, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan perp_positions (account): %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate perp_positions (account): %w", err)
	}
	return out, nil
}

// ListRecentTrades returns perp_trades since `since` filtered by chain
// and optional symbol allow-list.
func (r *Repository) ListRecentTrades(ctx context.Context, symbols []string, chainID *int, since time.Time) ([]PerpTrade, error) {
	if r.pool == nil {
		return nil, ErrPoolUnavailable
	}
	query, args := buildPerpTradesQuery(symbols, chainID, since)
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query perp_trades: %w", err)
	}
	defer rows.Close()

	out := make([]PerpTrade, 0)
	for rows.Next() {
		var t PerpTrade
		if err := rows.Scan(&t.Token, &t.SizeDelta); err != nil {
			return nil, fmt.Errorf("scan perp_trades: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate perp_trades: %w", err)
	}
	return out, nil
}

// ListOrderbookOrders returns up to 100 perp_orders eligible for the
// realtime orderbook view: PENDING/NEW/PARTIALLY_FILLED status with a
// triggerPrice set. Bids/asks ordering happens in the caller (uses
// math/big.Int comparison via bigsum.Compare).
func (r *Repository) ListOrderbookOrders(ctx context.Context, symbol string, chainID *int) ([]PerpOrder, error) {
	if r.pool == nil {
		return nil, ErrPoolUnavailable
	}
	query, args := buildOrderbookOrdersQuery(symbol, chainID, r.includePaperOrders)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query perp_orders: %w", err)
	}
	defer rows.Close()

	out := make([]PerpOrder, 0)
	for rows.Next() {
		var o PerpOrder
		if err := rows.Scan(&o.IsLong, &o.SizeDelta, &o.TriggerPrice); err != nil {
			return nil, fmt.Errorf("scan perp_orders: %w", err)
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate perp_orders: %w", err)
	}
	return out, nil
}

// ListRecentTradesForSymbol returns the most recent N trades for a
// single market, newest first. limit is clamped to [1, 200] — matches
// the service-level safety net in trading.service.ts getRecentMarketTrades.
func (r *Repository) ListRecentTradesForSymbol(ctx context.Context, symbol string, chainID *int, limit int) ([]PerpTradeFull, error) {
	if r.pool == nil {
		return nil, ErrPoolUnavailable
	}
	safeLimit := 20
	if limit > 0 {
		safeLimit = limit
	}
	if safeLimit > 200 {
		safeLimit = 200
	}

	b := newQueryBuilder()
	b.where("token = ?", symbol)
	if chainID != nil {
		b.where("chain_id = ?", *chainID)
	}
	query, args := b.build(`SELECT id, token, "isLong", type, "sizeDelta", price, fee, pnl, "txHash", "createdAt" FROM perp_trades`)
	query += fmt.Sprintf(` ORDER BY "createdAt" DESC LIMIT %d`, safeLimit)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query perp_trades (symbol): %w", err)
	}
	defer rows.Close()

	out := make([]PerpTradeFull, 0, safeLimit)
	for rows.Next() {
		var t PerpTradeFull
		if err := rows.Scan(&t.ID, &t.Token, &t.IsLong, &t.Type, &t.SizeDelta, &t.Price, &t.Fee, &t.PNL, &t.TxHash, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan perp_trades (symbol): %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate perp_trades (symbol): %w", err)
	}
	return out, nil
}

// ListPendingOrdersForAccount returns up to N pending/new/partially-
// filled orders for one account, newest first. Mirrors
// trading.service.ts getOrders. Status set matches the Prisma `in:`
// filter exactly so a future schema change shows up here.
func (r *Repository) ListPendingOrdersForAccount(ctx context.Context, account string, symbol string, chainID *int, limit int) ([]PerpOrder, error) {
	if r.pool == nil {
		return nil, ErrPoolUnavailable
	}
	if limit <= 0 {
		limit = 50
	}
	query, args := buildPendingOrdersQuery(account, symbol, chainID, limit, r.includePaperOrders)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query perp_orders (account): %w", err)
	}
	defer rows.Close()
	out := make([]PerpOrder, 0, limit)
	for rows.Next() {
		var o PerpOrder
		if err := rows.Scan(&o.ID, &o.Token, &o.IsLong, &o.Type, &o.SizeDelta, &o.TriggerPrice, &o.Status, &o.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan perp_orders (account): %w", err)
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate perp_orders (account): %w", err)
	}
	return out, nil
}

// ListTradesForAccount returns up to 50 perp trades for one account,
// newest first. Mirrors trading.service.ts getTradeHistory.
func (r *Repository) ListTradesForAccount(ctx context.Context, account string, symbol string, chainID *int, limit int) ([]PerpTradeFull, error) {
	if r.pool == nil {
		return nil, ErrPoolUnavailable
	}
	if limit <= 0 {
		limit = 50
	}
	b := newQueryBuilder()
	b.where("account = ?", strings.ToLower(account))
	if symbol != "" {
		b.where("token = ?", symbol)
	}
	if chainID != nil {
		b.where("chain_id = ?", *chainID)
	}
	query, args := b.build(`SELECT id, token, "isLong", type, "sizeDelta", price, fee, pnl, "txHash", "createdAt" FROM perp_trades`)
	query += fmt.Sprintf(` ORDER BY "createdAt" DESC LIMIT %d`, limit)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query perp_trades (account): %w", err)
	}
	defer rows.Close()
	out := make([]PerpTradeFull, 0, limit)
	for rows.Next() {
		var t PerpTradeFull
		if err := rows.Scan(&t.ID, &t.Token, &t.IsLong, &t.Type, &t.SizeDelta, &t.Price, &t.Fee, &t.PNL, &t.TxHash, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan perp_trades (account): %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate perp_trades (account): %w", err)
	}
	return out, nil
}

// ListAllOpenPositionSizes returns just the `size` column for every
// OPEN position. Used by GetMarketStats's totalOpenInterest sum.
func (r *Repository) ListAllOpenPositionSizes(ctx context.Context, chainID *int) ([]string, error) {
	if r.pool == nil {
		return nil, ErrPoolUnavailable
	}
	b := newQueryBuilder()
	b.where("status = ?", "OPEN")
	if chainID != nil {
		b.where("chain_id = ?", *chainID)
	}
	query, args := b.build(`SELECT size FROM perp_positions`)
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query perp_positions (sizes): %w", err)
	}
	defer rows.Close()
	out := make([]string, 0, 64)
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, fmt.Errorf("scan perp_positions (sizes): %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate perp_positions (sizes): %w", err)
	}
	return out, nil
}

// ListAllTradesSizeDeltas returns sizeDelta strings for every trade
// since `since`, optionally chain-filtered. Used by GetMarketStats's
// totalVolume sum.
func (r *Repository) ListAllTradesSizeDeltas(ctx context.Context, chainID *int, since time.Time) ([]string, error) {
	if r.pool == nil {
		return nil, ErrPoolUnavailable
	}
	b := newQueryBuilder()
	b.where(`"createdAt" >= ?`, since)
	if chainID != nil {
		b.where("chain_id = ?", *chainID)
	}
	query, args := b.build(`SELECT "sizeDelta" FROM perp_trades`)
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query perp_trades (deltas): %w", err)
	}
	defer rows.Close()
	out := make([]string, 0, 64)
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, fmt.Errorf("scan perp_trades (deltas): %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate perp_trades (deltas): %w", err)
	}
	return out, nil
}

// TradeCandleRow is the narrow projection ListTradesForCandles emits.
type TradeCandleRow struct {
	Price     string
	SizeDelta string
	CreatedAt time.Time
}

// ListTradesForCandles returns (price, sizeDelta, createdAt) over a
// time window for one symbol, ascending so the first/last trade in
// each bucket are open/close.
func (r *Repository) ListTradesForCandles(ctx context.Context, symbol string, chainID *int, from time.Time, to *time.Time) ([]TradeCandleRow, error) {
	if r.pool == nil {
		return nil, ErrPoolUnavailable
	}
	b := newQueryBuilder()
	b.where("token = ?", symbol)
	b.where(`"createdAt" >= ?`, from)
	if to != nil {
		b.where(`"createdAt" <= ?`, *to)
	}
	if chainID != nil {
		b.where("chain_id = ?", *chainID)
	}
	query, args := b.build(`SELECT price, "sizeDelta", "createdAt" FROM perp_trades`)
	query += ` ORDER BY "createdAt" ASC`
	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query perp_trades (candles): %w", err)
	}
	defer rows.Close()
	out := make([]TradeCandleRow, 0, 256)
	for rows.Next() {
		var t TradeCandleRow
		if err := rows.Scan(&t.Price, &t.SizeDelta, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan perp_trades (candles): %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate perp_trades (candles): %w", err)
	}
	return out, nil
}

// buildOrderbookOrdersQuery builds the orderbook read. Paper rows (no
// txHash — the dev seeder's fake book) are excluded unless explicitly
// opted in, so a fake order book can never feed the live market view.
func buildOrderbookOrdersQuery(symbol string, chainID *int, includePaper bool) (string, []any) {
	b := newQueryBuilder()
	b.where("token = ?", symbol)
	b.whereIn("status", []string{"PENDING", "NEW", "PARTIALLY_FILLED"})
	b.whereRaw(`"triggerPrice" IS NOT NULL`)
	if !includePaper {
		b.whereRaw(`"txHash" IS NOT NULL`)
	}
	if chainID != nil {
		b.where("chain_id = ?", *chainID)
	}
	query, args := b.build(`SELECT "isLong", "sizeDelta", "triggerPrice" FROM perp_orders`)
	return query + " LIMIT 100", args
}

// buildPendingOrdersQuery builds the account pending-orders read with the
// same paper-row exclusion as the orderbook.
func buildPendingOrdersQuery(account, symbol string, chainID *int, limit int, includePaper bool) (string, []any) {
	b := newQueryBuilder()
	b.where("account = ?", strings.ToLower(account))
	b.whereIn("status", []string{"PENDING", "NEW", "PARTIALLY_FILLED"})
	if !includePaper {
		b.whereRaw(`"txHash" IS NOT NULL`)
	}
	if symbol != "" {
		b.where("token = ?", symbol)
	}
	if chainID != nil {
		b.where("chain_id = ?", *chainID)
	}
	query, args := b.build(`SELECT id, token, "isLong", type, "sizeDelta", "triggerPrice", status, "createdAt" FROM perp_orders`)
	return query + fmt.Sprintf(` ORDER BY "createdAt" DESC LIMIT %d`, limit), args
}

// buildPerpPositionsQuery is split out so the SQL is testable without
// a live database. Returns parameterized SQL — no user input is
// interpolated; `symbols` becomes a `$N` placeholder list.
func buildPerpPositionsQuery(symbols []string, chainID *int) (string, []any) {
	b := newQueryBuilder()
	b.where("status = ?", "OPEN")
	if chainID != nil {
		b.where("chain_id = ?", *chainID)
	}
	if len(symbols) > 0 {
		b.whereIn("token", symbols)
	}
	return b.build(`SELECT token, "isLong", size FROM perp_positions`)
}

func buildPerpTradesQuery(symbols []string, chainID *int, since time.Time) (string, []any) {
	b := newQueryBuilder()
	b.where(`"createdAt" >= ?`, since)
	if chainID != nil {
		b.where("chain_id = ?", *chainID)
	}
	if len(symbols) > 0 {
		b.whereIn("token", symbols)
	}
	return b.build(`SELECT token, "sizeDelta" FROM perp_trades`)
}

// queryBuilder accumulates WHERE clauses with positional $N placeholders,
// replacing `?` markers in caller-supplied fragments. Keeps the
// builders above readable without pulling in squirrel/sqlx.
type queryBuilder struct {
	conds []string
	args  []any
}

func newQueryBuilder() *queryBuilder { return &queryBuilder{} }

func (b *queryBuilder) where(fragment string, arg any) {
	placeholder := fmt.Sprintf("$%d", len(b.args)+1)
	b.conds = append(b.conds, strings.Replace(fragment, "?", placeholder, 1))
	b.args = append(b.args, arg)
}

// whereRaw appends a literal condition with no bound parameters.
// Useful for "IS NOT NULL"-style predicates.
func (b *queryBuilder) whereRaw(fragment string) {
	b.conds = append(b.conds, fragment)
}

func (b *queryBuilder) whereIn(column string, values []string) {
	placeholders := make([]string, len(values))
	for i, v := range values {
		placeholders[i] = fmt.Sprintf("$%d", len(b.args)+1)
		b.args = append(b.args, v)
	}
	b.conds = append(b.conds, fmt.Sprintf("%s IN (%s)", column, strings.Join(placeholders, ", ")))
}

func (b *queryBuilder) build(selectClause string) (string, []any) {
	if len(b.conds) == 0 {
		return selectClause, b.args
	}
	return selectClause + " WHERE " + strings.Join(b.conds, " AND "), b.args
}
