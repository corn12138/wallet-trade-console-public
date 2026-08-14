// Package token is the Go port of legacy NestJS token/token.service.ts.
//
// All frontend-used token routes are implemented here: GET list/trending/
// symbol/address[/trades/holders/candles/stats]/by-id (token.go) and POST
// create (create.go). The GET reads serve PUBLIC market data (token metadata +
// on-chain price/trades/holders/candles) — a Go-canonical departure from the
// deployed NestJS, where the controller carries no @Public so the global
// JwtAuthGuard 401s them; the FE loads token detail/trending/sidebar with plain
// fetch and no auth, so they must be public (same rationale as
// GET /api/earn/products). POST create stays web3-guarded. See Router +
// guard-parity.md.
package token

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TrendingToken is the minimal view of `tokens` rows needed for
// /markets/snapshot topMovers.
//
// Address and PriceChange24h are nullable in the DB; the JSON shape
// preserves nulls (and JS-style `|| 0` coercion happens in the
// markets service mapping).
type TrendingToken struct {
	Symbol         string
	Address        *string
	PriceChange24h *float64
}

// TrendingTokenFull is the wider projection used by /api/discover/home.
// Same ORDER BY / WHERE as ListTrending; only the SELECT list grows.
type TrendingTokenFull struct {
	ID             string
	Symbol         string
	Name           string
	Address        *string
	PriceChange24h *float64
	Volume24h      *float64
	MarketCap      *float64
	IsOfficial     bool
	Status         string
}

// Repository wraps the pgx pool with the trending-token query.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository builds a token repo. A nil pool yields a repo whose
// methods all return ErrPoolUnavailable.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// ErrPoolUnavailable mirrors trading.ErrPoolUnavailable.
var ErrPoolUnavailable = fmt.Errorf("token repository: database pool not configured")

// ListTrending mirrors token.service.ts findTrending:
//
//	SELECT symbol, address, "price_change_24h"
//	FROM tokens
//	WHERE status = 'LAUNCHED' AND (chain_id = $? OR $? IS NULL)
//	ORDER BY "price_change_24h" DESC NULLS LAST, "volume_24h" DESC NULLS LAST
//	LIMIT $?
//
// Prisma generates DESC NULLS LAST by default; we match that explicitly
// so the ordering is identical regardless of Postgres defaults.
func (r *Repository) ListTrending(ctx context.Context, chainID *int, limit int) ([]TrendingToken, error) {
	limit = capPositiveLimit(limit, maxTrendingTokenLimit)
	if limit <= 0 {
		return []TrendingToken{}, nil
	}
	if r.pool == nil {
		return nil, ErrPoolUnavailable
	}

	var (
		rows pgx.Rows
		err  error
	)
	if chainID != nil {
		rows, err = r.pool.Query(ctx, `
			SELECT symbol, address, price_change_24h
			FROM tokens
			WHERE status = $1 AND chain_id = $2
			ORDER BY price_change_24h DESC NULLS LAST, volume_24h DESC NULLS LAST
			LIMIT $3
		`, "LAUNCHED", *chainID, limit)
	} else {
		rows, err = r.pool.Query(ctx, `
			SELECT symbol, address, price_change_24h
			FROM tokens
			WHERE status = $1
			ORDER BY price_change_24h DESC NULLS LAST, volume_24h DESC NULLS LAST
			LIMIT $2
		`, "LAUNCHED", limit)
	}
	if err != nil {
		return nil, fmt.Errorf("query tokens: %w", err)
	}
	defer rows.Close()

	out := make([]TrendingToken, 0, limit)
	for rows.Next() {
		var t TrendingToken
		if err := rows.Scan(&t.Symbol, &t.Address, &t.PriceChange24h); err != nil {
			return nil, fmt.Errorf("scan tokens: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tokens: %w", err)
	}
	return out, nil
}

// Token mirrors the public /api/token row shape. Field names match
// the camelCase Prisma serialization used by services/api so FE
// consumers don't notice the cutover. Nullable columns use pointers.
type Token struct {
	ID         string  `json:"id"`
	Address    *string `json:"address"`
	ChainID    int     `json:"chainId"`
	Symbol     string  `json:"symbol"`
	Name       string  `json:"name"`
	Image      *string `json:"image"`
	Banner     *string `json:"banner"`
	Tags       any     `json:"tags"`
	Status     string  `json:"status"`
	LaunchType string  `json:"launchType"`
	IsOfficial bool    `json:"isOfficial"`
	// Financial fields are normalized NATIVE decimal strings (NUMERIC::text)
	// per the 2026-07-10 unit contract; priceChange24h stays a display percent.
	MarketCap      *string    `json:"marketCap"`
	Volume24h      *string    `json:"volume24h"`
	PriceChange24h *float64   `json:"priceChange24h"`
	CreatorAddress string     `json:"creatorAddress"`
	LaunchedAt     *time.Time `json:"launchedAt"`
	CreatedAt      time.Time  `json:"createdAt"`
}

// ListQuery mirrors QueryTokensDto: status / launchType / chainId /
// creatorAddress / search / sortBy / page / limit. Empty strings and
// nil pointers mean "no filter".
type ListQuery struct {
	Status         string
	LaunchType     string
	ChainID        *int
	CreatorAddress string
	Search         string
	SortBy         string
	Page           int
	Limit          int
}

// ListResult is the {data, meta} envelope the NestJS findAll returns.
type ListResult struct {
	Data []Token  `json:"data"`
	Meta ListMeta `json:"meta"`
}

// ListMeta carries pagination metadata. totalPages matches the NestJS
// Math.ceil(total / limit); when limit is 0 we report 0 to avoid
// division-by-zero, even though the handler clamps limit ≥ 1.
type ListMeta struct {
	Total      int `json:"total"`
	Page       int `json:"page"`
	Limit      int `json:"limit"`
	TotalPages int `json:"totalPages"`
}

// ListTrendingFull is ListTrending's wider sibling. Same WHERE/ORDER BY;
// returns the columns /api/discover/home consumes.
func (r *Repository) ListTrendingFull(ctx context.Context, chainID *int, limit int) ([]TrendingTokenFull, error) {
	limit = capPositiveLimit(limit, maxTrendingTokenLimit)
	if limit <= 0 {
		return []TrendingTokenFull{}, nil
	}
	if r.pool == nil {
		return nil, ErrPoolUnavailable
	}

	var (
		rows pgx.Rows
		err  error
	)
	if chainID != nil {
		rows, err = r.pool.Query(ctx, `
			SELECT id, symbol, name, address, price_change_24h, volume_24h, market_cap, is_official, status
			FROM tokens
			WHERE status = $1 AND chain_id = $2
			ORDER BY price_change_24h DESC NULLS LAST, volume_24h DESC NULLS LAST
			LIMIT $3
		`, "LAUNCHED", *chainID, limit)
	} else {
		rows, err = r.pool.Query(ctx, `
			SELECT id, symbol, name, address, price_change_24h, volume_24h, market_cap, is_official, status
			FROM tokens
			WHERE status = $1
			ORDER BY price_change_24h DESC NULLS LAST, volume_24h DESC NULLS LAST
			LIMIT $2
		`, "LAUNCHED", limit)
	}
	if err != nil {
		return nil, fmt.Errorf("query tokens (full): %w", err)
	}
	defer rows.Close()

	out := make([]TrendingTokenFull, 0, limit)
	for rows.Next() {
		var t TrendingTokenFull
		if err := rows.Scan(&t.ID, &t.Symbol, &t.Name, &t.Address, &t.PriceChange24h, &t.Volume24h, &t.MarketCap, &t.IsOfficial, &t.Status); err != nil {
			return nil, fmt.Errorf("scan tokens (full): %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tokens (full): %w", err)
	}
	return out, nil
}

// ListAll mirrors token.service.ts findAll. Builds a parameterized
// query off ListQuery, returns rows + total count for the meta block.
// Empty filters are omitted from the WHERE clause; sortBy maps to the
// same ORDER BY Prisma generates.
func (r *Repository) ListAll(ctx context.Context, q ListQuery) ([]Token, int, error) {
	q.Limit = boundedLimit(q.Limit, defaultTokenListLimit, maxTokenListLimit)
	q.Page = boundedPage(q.Page)
	if r.pool == nil {
		return nil, 0, ErrPoolUnavailable
	}

	where, args := buildWhere(q)
	orderBy := buildOrderBy(q.SortBy)
	offset := (q.Page - 1) * q.Limit

	listSQL := `
		SELECT id, address, chain_id, symbol, name, image, banner, tags, status,
		       launch_type, is_official, market_cap::text, volume_24h::text, price_change_24h,
		       creator_address, launched_at, created_at
		FROM tokens
		` + where + `
		ORDER BY ` + orderBy + `
		LIMIT $` + strconv.Itoa(len(args)+1) + ` OFFSET $` + strconv.Itoa(len(args)+2)
	listArgs := append(append([]any{}, args...), q.Limit, offset)

	rows, err := r.pool.Query(ctx, listSQL, listArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("query tokens (list): %w", err)
	}
	defer rows.Close()

	out := make([]Token, 0, q.Limit)
	for rows.Next() {
		var t Token
		var tagsRaw []byte
		if err := rows.Scan(&t.ID, &t.Address, &t.ChainID, &t.Symbol, &t.Name, &t.Image, &t.Banner,
			&tagsRaw, &t.Status, &t.LaunchType, &t.IsOfficial, &t.MarketCap, &t.Volume24h,
			&t.PriceChange24h, &t.CreatorAddress, &t.LaunchedAt, &t.CreatedAt); err != nil {
			return nil, 0, fmt.Errorf("scan tokens (list): %w", err)
		}
		t.Tags = decodeTags(tagsRaw)
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate tokens (list): %w", err)
	}

	countSQL := `SELECT COUNT(*) FROM tokens ` + where
	var total int
	if err := r.pool.QueryRow(ctx, countSQL, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count tokens (list): %w", err)
	}

	return out, total, nil
}

// FindByID returns one token by primary-key id. Mirrors token.findById
// with the same NotFoundException semantics (caller propagates the 404).
func (r *Repository) FindByID(ctx context.Context, id string) (Token, error) {
	if r.pool == nil {
		return Token{}, ErrPoolUnavailable
	}
	var t Token
	var tagsRaw []byte
	err := r.pool.QueryRow(ctx, `
		SELECT id, address, chain_id, symbol, name, image, banner, tags, status,
		       launch_type, is_official, market_cap::text, volume_24h::text, price_change_24h,
		       creator_address, launched_at, created_at
		FROM tokens
		WHERE id = $1
		LIMIT 1
	`, id).Scan(&t.ID, &t.Address, &t.ChainID, &t.Symbol, &t.Name, &t.Image, &t.Banner,
		&tagsRaw, &t.Status, &t.LaunchType, &t.IsOfficial, &t.MarketCap, &t.Volume24h,
		&t.PriceChange24h, &t.CreatorAddress, &t.LaunchedAt, &t.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Token{}, ErrNotFound
	}
	if err != nil {
		return Token{}, fmt.Errorf("query tokens (by id): %w", err)
	}
	t.Tags = decodeTags(tagsRaw)
	return t, nil
}

// ListTrendingTokens mirrors token.service.ts findTrending — same WHERE
// + ORDER BY as ListTrending/ListTrendingFull, but returns the full
// Token row (the FE /trending consumer expects the public Token shape).
func (r *Repository) ListTrendingTokens(ctx context.Context, chainID *int, limit int) ([]Token, error) {
	limit = capPositiveLimit(limit, maxTrendingTokenLimit)
	if limit <= 0 {
		return []Token{}, nil
	}
	if r.pool == nil {
		return nil, ErrPoolUnavailable
	}
	var (
		rows pgx.Rows
		err  error
	)
	if chainID != nil {
		rows, err = r.pool.Query(ctx, `
			SELECT id, address, chain_id, symbol, name, image, banner, tags, status,
			       launch_type, is_official, market_cap::text, volume_24h::text, price_change_24h,
			       creator_address, launched_at, created_at
			FROM tokens
			WHERE status = $1 AND chain_id = $2
			ORDER BY price_change_24h DESC NULLS LAST, volume_24h DESC NULLS LAST
			LIMIT $3
		`, "LAUNCHED", *chainID, limit)
	} else {
		rows, err = r.pool.Query(ctx, `
			SELECT id, address, chain_id, symbol, name, image, banner, tags, status,
			       launch_type, is_official, market_cap::text, volume_24h::text, price_change_24h,
			       creator_address, launched_at, created_at
			FROM tokens
			WHERE status = $1
			ORDER BY price_change_24h DESC NULLS LAST, volume_24h DESC NULLS LAST
			LIMIT $2
		`, "LAUNCHED", limit)
	}
	if err != nil {
		return nil, fmt.Errorf("query tokens (trending): %w", err)
	}
	defer rows.Close()
	out := make([]Token, 0, limit)
	for rows.Next() {
		var t Token
		var tagsRaw []byte
		if err := rows.Scan(&t.ID, &t.Address, &t.ChainID, &t.Symbol, &t.Name, &t.Image, &t.Banner,
			&tagsRaw, &t.Status, &t.LaunchType, &t.IsOfficial, &t.MarketCap, &t.Volume24h,
			&t.PriceChange24h, &t.CreatorAddress, &t.LaunchedAt, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan tokens (trending): %w", err)
		}
		t.Tags = decodeTags(tagsRaw)
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tokens (trending): %w", err)
	}
	return out, nil
}

// FindByAddress returns one token by lowercase contract address.
// Mirrors token.findByAddress with NotFoundException.
func (r *Repository) FindByAddress(ctx context.Context, address string) (Token, error) {
	if r.pool == nil {
		return Token{}, ErrPoolUnavailable
	}
	var t Token
	var tagsRaw []byte
	err := r.pool.QueryRow(ctx, `
		SELECT id, address, chain_id, symbol, name, image, banner, tags, status,
		       launch_type, is_official, market_cap::text, volume_24h::text, price_change_24h,
		       creator_address, launched_at, created_at
		FROM tokens
		WHERE LOWER(address) = LOWER($1)
		LIMIT 1
	`, address).Scan(&t.ID, &t.Address, &t.ChainID, &t.Symbol, &t.Name, &t.Image, &t.Banner,
		&tagsRaw, &t.Status, &t.LaunchType, &t.IsOfficial, &t.MarketCap, &t.Volume24h,
		&t.PriceChange24h, &t.CreatorAddress, &t.LaunchedAt, &t.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Token{}, ErrNotFound
	}
	if err != nil {
		return Token{}, fmt.Errorf("query tokens (by address): %w", err)
	}
	t.Tags = decodeTags(tagsRaw)
	return t, nil
}

// Trade mirrors a token_trades row in the FE-emitted shape.
type Trade struct {
	ID              string    `json:"id"`
	TokenID         string    `json:"tokenId"`
	UserAddress     string    `json:"userAddress"`
	Type            string    `json:"type"`
	TokenAmount     string    `json:"tokenAmount"`
	ETHAmount       string    `json:"ethAmount"`
	Price           float64   `json:"price"`
	BlockNumber     int64     `json:"blockNumber"`
	TransactionHash string    `json:"transactionHash"`
	Timestamp       time.Time `json:"timestamp"`
}

// Holder mirrors a token_holders row.
type Holder struct {
	ID          string    `json:"id"`
	TokenID     string    `json:"tokenId"`
	UserAddress string    `json:"userAddress"`
	Balance     string    `json:"balance"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// ListTrades returns recent token_trades for tokenID, newest first.
func (r *Repository) ListTrades(ctx context.Context, tokenID string, limit int) ([]Trade, error) {
	if limit <= 0 {
		limit = 50
	}
	if r.pool == nil {
		return nil, ErrPoolUnavailable
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, "tokenId", user_address, type, "tokenAmount", "ethAmount", price,
		       block_number, transaction_hash, timestamp
		FROM token_trades
		WHERE "tokenId" = $1
		ORDER BY timestamp DESC
		LIMIT $2
	`, tokenID, limit)
	if err != nil {
		return nil, fmt.Errorf("query token_trades: %w", err)
	}
	defer rows.Close()
	out := make([]Trade, 0, limit)
	for rows.Next() {
		var tr Trade
		if err := rows.Scan(&tr.ID, &tr.TokenID, &tr.UserAddress, &tr.Type,
			&tr.TokenAmount, &tr.ETHAmount, &tr.Price, &tr.BlockNumber,
			&tr.TransactionHash, &tr.Timestamp); err != nil {
			return nil, fmt.Errorf("scan token_trades: %w", err)
		}
		out = append(out, tr)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate token_trades: %w", err)
	}
	return out, nil
}

// HoldingForOwner is a token_holders row joined with the token's
// identity columns. Portfolio.collectRawAssets uses this to surface
// every token the owner holds (created or discovered).
type HoldingForOwner struct {
	HolderID        string
	UserAddress     string
	Balance         string
	HolderUpdated   time.Time
	TokenID         string
	TokenSymbol     string
	TokenName       string
	TokenAddress    *string
	TokenChainID    int
	TokenStatus     string
	TokenIsOfficial bool
	TokenCreator    string
	TokenMarketCap  *float64
	TokenTags       any
	TokenUpdatedAt  time.Time
}

// ListHoldingsByOwner returns token_holders for ownerAddress joined
// with the parent tokens row, optionally filtered by chain. Ordered
// holders.updatedAt DESC, limit 40 — matches the NestJS Prisma call.
func (r *Repository) ListHoldingsByOwner(ctx context.Context, ownerAddress string, chainID *int) ([]HoldingForOwner, error) {
	if r.pool == nil {
		return nil, ErrPoolUnavailable
	}
	args := []any{ownerAddress}
	clauses := []string{`h.user_address = $1`}
	if chainID != nil {
		args = append(args, *chainID)
		clauses = append(clauses, `t.chain_id = $2`)
	}
	rows, err := r.pool.Query(ctx, `
		SELECT h.id, h.user_address, h.balance, h."updatedAt",
		       t.id, t.symbol, t.name, t.address, t.chain_id, t.status,
		       t.is_official, t.creator_address, t.market_cap, t.tags, t.updated_at
		FROM token_holders h
		JOIN tokens t ON t.id = h."tokenId"
		WHERE `+strings.Join(clauses, " AND ")+`
		ORDER BY h."updatedAt" DESC
		LIMIT 40
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("query token_holders (by owner): %w", err)
	}
	defer rows.Close()
	out := make([]HoldingForOwner, 0)
	for rows.Next() {
		var (
			h       HoldingForOwner
			tagsRaw []byte
		)
		if err := rows.Scan(&h.HolderID, &h.UserAddress, &h.Balance, &h.HolderUpdated,
			&h.TokenID, &h.TokenSymbol, &h.TokenName, &h.TokenAddress, &h.TokenChainID,
			&h.TokenStatus, &h.TokenIsOfficial, &h.TokenCreator, &h.TokenMarketCap,
			&tagsRaw, &h.TokenUpdatedAt); err != nil {
			return nil, fmt.Errorf("scan token_holders (by owner): %w", err)
		}
		h.TokenTags = decodeTags(tagsRaw)
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate token_holders (by owner): %w", err)
	}
	return out, nil
}

// ListHolders returns the top holders for tokenID. NestJS pulls up to
// 1000 rows then sorts by BigInt balance client-side; we push the sort
// down to Postgres via `balance::numeric` so the same ordering happens
// in a single pass with no client-side cost.
func (r *Repository) ListHolders(ctx context.Context, tokenID string, limit int) ([]Holder, error) {
	if limit <= 0 {
		limit = 50
	}
	if r.pool == nil {
		return nil, ErrPoolUnavailable
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, "tokenId", user_address, balance, "updatedAt"
		FROM token_holders
		WHERE "tokenId" = $1
		ORDER BY balance::numeric DESC
		LIMIT $2
	`, tokenID, limit)
	if err != nil {
		return nil, fmt.Errorf("query token_holders: %w", err)
	}
	defer rows.Close()
	out := make([]Holder, 0, limit)
	for rows.Next() {
		var h Holder
		if err := rows.Scan(&h.ID, &h.TokenID, &h.UserAddress, &h.Balance, &h.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan token_holders: %w", err)
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate token_holders: %w", err)
	}
	return out, nil
}

// FindBySymbol returns one token where lower(symbol) = lower($1).
// Used by the legacy /atlas/token/:sym route. Case-insensitive match
// because the FE URL preserves caller casing (`/HONEY` vs `/honey`),
// and the symbol column is plain text without a citext domain.
func (r *Repository) FindBySymbol(ctx context.Context, symbol string) (Token, error) {
	if r.pool == nil {
		return Token{}, ErrPoolUnavailable
	}
	var t Token
	var tagsRaw []byte
	err := r.pool.QueryRow(ctx, `
		SELECT id, address, chain_id, symbol, name, image, banner, tags, status,
		       launch_type, is_official, market_cap::text, volume_24h::text, price_change_24h,
		       creator_address, launched_at, created_at
		FROM tokens
		WHERE LOWER(symbol) = LOWER($1)
		ORDER BY created_at DESC
		LIMIT 1
	`, symbol).Scan(&t.ID, &t.Address, &t.ChainID, &t.Symbol, &t.Name, &t.Image, &t.Banner,
		&tagsRaw, &t.Status, &t.LaunchType, &t.IsOfficial, &t.MarketCap, &t.Volume24h,
		&t.PriceChange24h, &t.CreatorAddress, &t.LaunchedAt, &t.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Token{}, ErrNotFound
	}
	if err != nil {
		return Token{}, fmt.Errorf("query tokens (by symbol): %w", err)
	}
	t.Tags = decodeTags(tagsRaw)
	return t, nil
}

// buildWhere produces the WHERE clause and positional args. Param
// numbering is contiguous from $1 so the LIMIT/OFFSET placeholders the
// caller appends slot in cleanly.
func buildWhere(q ListQuery) (string, []any) {
	clauses := make([]string, 0, 6)
	args := make([]any, 0, 6)
	add := func(sql string, val any) {
		args = append(args, val)
		clauses = append(clauses, fmt.Sprintf(sql, len(args)))
	}
	if q.Status != "" {
		add("status = $%d", q.Status)
	}
	if q.LaunchType != "" {
		add("launch_type = $%d", q.LaunchType)
	}
	if q.ChainID != nil {
		add("chain_id = $%d", *q.ChainID)
	}
	if q.CreatorAddress != "" {
		// Rows store lowercase addresses (Create + indexer sink both normalize);
		// a checksummed /profile/0xAbC… URL must still match its created tokens.
		add("creator_address = $%d", strings.ToLower(q.CreatorAddress))
	}
	if q.Search != "" {
		// Prisma's `mode: 'insensitive'` compiles to ILIKE.
		args = append(args, "%"+q.Search+"%")
		idx := len(args)
		clauses = append(clauses, fmt.Sprintf("(symbol ILIKE $%d OR name ILIKE $%d)", idx, idx))
	}
	if len(clauses) == 0 {
		return "", args
	}
	return "WHERE " + join(clauses, " AND "), args
}

// buildOrderBy maps QueryTokensDto.sortBy values to SQL ORDER BY.
// Defaults to created_at DESC, matching NestJS findAll's fall-through.
func buildOrderBy(sortBy string) string {
	switch sortBy {
	case "marketCap":
		return "market_cap DESC NULLS LAST"
	case "volume":
		return "volume_24h DESC NULLS LAST"
	case "trending":
		return "price_change_24h DESC NULLS LAST"
	default:
		return "created_at DESC"
	}
}

func join(parts []string, sep string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	}
	out := parts[0]
	for _, p := range parts[1:] {
		out += sep + p
	}
	return out
}

// decodeTags handles the tags JSONB column. NULL or zero-length →
// empty slice (matches Prisma default "[]"). Decode failure surfaces
// as an empty slice too, since malformed JSON in this column is a
// data-corruption issue we don't want to translate into 500s here.
func decodeTags(raw []byte) any {
	if len(raw) == 0 {
		return []any{}
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return []any{}
	}
	return v
}

// ErrNotFound is reserved for future by-id handlers; defined here so
// the public Errors of this package are consistent with sibling packages.
var ErrNotFound = errors.New("token not found")

// Service wraps the repo with degraded-mode handling for handler use.
type Service struct {
	repo *Repository
}

// NewService binds a service over the existing token Repository.
func NewService(repo *Repository) *Service {
	return &Service{repo: repo}
}

// List proxies to repo.ListAll, degrading nil-pool to an empty result
// set with zero counts — matches the sibling discover/campaign/markets
// pattern of "service stays up; data returns empty."
func (s *Service) List(ctx context.Context, q ListQuery) (ListResult, error) {
	q.Limit = boundedLimit(q.Limit, defaultTokenListLimit, maxTokenListLimit)
	limit := q.Limit
	q.Page = boundedPage(q.Page)
	page := q.Page
	rows, total, err := s.repo.ListAll(ctx, q)
	if errors.Is(err, ErrPoolUnavailable) {
		return ListResult{
			Data: []Token{},
			Meta: ListMeta{Total: 0, Page: page, Limit: limit, TotalPages: 0},
		}, nil
	}
	if err != nil {
		return ListResult{}, err
	}
	return ListResult{
		Data: rows,
		Meta: ListMeta{
			Total:      total,
			Page:       page,
			Limit:      limit,
			TotalPages: int(math.Ceil(float64(total) / float64(limit))),
		},
	}, nil
}

// FindBySymbol proxies to the repo with degrade-to-not-found semantics.
// A nil pool surfaces as 404 from the handler, matching the
// campaign.FindByID pattern.
func (s *Service) FindBySymbol(ctx context.Context, symbol string) (Token, error) {
	out, err := s.repo.FindBySymbol(ctx, symbol)
	if errors.Is(err, ErrPoolUnavailable) {
		return Token{}, ErrNotFound
	}
	return out, err
}

// FindByAddress proxies to the repo. Pool-unavailable degrades to
// not-found so the handler returns 404 rather than 500.
func (s *Service) FindByAddress(ctx context.Context, address string) (Token, error) {
	out, err := s.repo.FindByAddress(ctx, address)
	if errors.Is(err, ErrPoolUnavailable) {
		return Token{}, ErrNotFound
	}
	return out, err
}

// FindByID mirrors FindByAddress's degraded contract.
func (s *Service) FindByID(ctx context.Context, id string) (Token, error) {
	out, err := s.repo.FindByID(ctx, id)
	if errors.Is(err, ErrPoolUnavailable) {
		return Token{}, ErrNotFound
	}
	return out, err
}

// FindTrending degrades to []. Matches sibling degraded-mode patterns
// where the frontend gets an empty list rather than a 5xx.
func (s *Service) FindTrending(ctx context.Context, chainID *int, limit int) ([]Token, error) {
	limit = boundedLimit(limit, defaultTrendingTokenLimit, maxTrendingTokenLimit)
	out, err := s.repo.ListTrendingTokens(ctx, chainID, limit)
	if errors.Is(err, ErrPoolUnavailable) {
		return []Token{}, nil
	}
	return out, err
}

// ListTradesForToken degrades to []. Handler call site already resolved
// the address to a token row, so an empty trade list isn't an error.
func (s *Service) ListTradesForToken(ctx context.Context, tokenID string, limit int) ([]Trade, error) {
	out, err := s.repo.ListTrades(ctx, tokenID, limit)
	if errors.Is(err, ErrPoolUnavailable) {
		return []Trade{}, nil
	}
	return out, err
}

// ListHoldersForToken degrades to [].
func (s *Service) ListHoldersForToken(ctx context.Context, tokenID string, limit int) ([]Holder, error) {
	out, err := s.repo.ListHolders(ctx, tokenID, limit)
	if errors.Is(err, ErrPoolUnavailable) {
		return []Holder{}, nil
	}
	return out, err
}

// Router mounts /api/token under a chi sub-router. Endpoints exposed:
//   - POST /                         (create, Phase 4o, auth-guarded)
//   - GET /                          (list, Phase 3c)
//   - GET /trending                  (Phase 4m)
//   - GET /symbol/{symbol}           (Phase 3d)
//   - GET /address/{address}         (Phase 4g)
//   - GET /address/{address}/trades  (Phase 4g)
//   - GET /address/{address}/holders (Phase 4g)
//   - GET /address/{address}/candles (Phase 4l)
//   - GET /address/{address}/stats   (Phase 4l)
//   - GET /{id}                      (Phase 4m, registered last)
//
// /trending is registered before /{id} for readability — chi resolves
// literal segments ahead of parameter segments regardless of order, but
// the order here matches the controller's @Get('trending') / @Get(':id')
// declaration order.
//
// Auth:
//   - POST / (create) is web3-guarded by authMiddleware — NestJS
//     @UseGuards(Web3AuthGuard). (The deployed NestJS create is ALSO under the
//     global JwtAuthGuard, making the access+web3 combo unsatisfiable — see
//     guard-parity.md addendum (b); Go's web3-only create is the satisfiable
//     Go-canonical form.) nil middleware → open (dev/tests); the create handler
//     also has a defense-in-depth 401 when no auth address is on the context.
//   - GET reads are PUBLIC — a Go-canonical departure from the deployed NestJS,
//     whose token controller carries no @Public so the global JwtAuthGuard
//     401s every read. The FE loads token detail/stats/trending/sidebar with
//     plain fetch and NO auth header (pre-login, public token pages), and the
//     data is public token metadata + on-chain market data (price/trades/
//     holders/candles), never per-viewer — so the reads must be public, same as
//     GET /api/earn/products. See guard-parity.md.
func Router(svc *Service, authMiddleware func(http.Handler) http.Handler) chi.Router {
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		if authMiddleware != nil {
			r.Use(authMiddleware)
		}
		r.Post("/", makeCreateHandler(svc))
	})
	// Public GET reads (no guard) — see the Auth note above.
	r.Get("/", makeListHandler(svc))
	r.Get("/trending", makeTrendingHandler(svc))
	r.Get("/symbol/{symbol}", makeBySymbolHandler(svc))
	r.Get("/address/{address}", makeByAddressHandler(svc))
	r.Get("/address/{address}/trades", makeTradesHandler(svc))
	r.Get("/address/{address}/holders", makeHoldersHandler(svc))
	r.Get("/address/{address}/candles", makeCandlesHandler(svc))
	r.Get("/address/{address}/stats", makeStatsHandler(svc))
	r.Get("/{id}", makeByIDHandler(svc))
	return r
}

func makeListHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		query := ListQuery{
			Status:         q.Get("status"),
			LaunchType:     q.Get("launchType"),
			CreatorAddress: q.Get("creatorAddress"),
			Search:         q.Get("search"),
			SortBy:         q.Get("sortBy"),
		}
		if raw := q.Get("chainId"); raw != "" {
			if v, err := strconv.Atoi(raw); err == nil {
				query.ChainID = &v
			}
		}
		query.Page = parseBoundedPage(q.Get("page"))
		query.Limit = parseBoundedLimit(q.Get("limit"), defaultTokenListLimit, maxTokenListLimit)

		out, err := svc.List(r.Context(), query)
		if err != nil {
			slog.ErrorContext(r.Context(), "token list failed", "err", err)
			http.Error(w, "failed to load tokens", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// makeTrendingHandler mirrors NestJS getTrending: optional chainId,
// limit defaults to 10. Returns the full Token shape (not the narrow
// TrendingToken projection used by /markets/snapshot's top movers).
func makeTrendingHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var chainID *int
		if raw := q.Get("chainId"); raw != "" {
			if v, err := strconv.Atoi(raw); err == nil {
				chainID = &v
			}
		}
		limit := parseBoundedLimit(q.Get("limit"), defaultTrendingTokenLimit, maxTrendingTokenLimit)
		out, err := svc.FindTrending(r.Context(), chainID, limit)
		if err != nil {
			slog.ErrorContext(r.Context(), "token trending failed", "err", err)
			http.Error(w, "failed to load trending tokens", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// makeByIDHandler mirrors NestJS findById: primary-key lookup, 404 on
// miss. Registered after the literal /symbol/, /address/, /trending
// routes so chi only dispatches genuinely id-shaped paths here.
func makeByIDHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		if id == "" {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		out, err := svc.FindByID(r.Context(), id)
		if errors.Is(err, ErrNotFound) {
			http.Error(w, "token not found", http.StatusNotFound)
			return
		}
		if err != nil {
			slog.ErrorContext(r.Context(), "token by id failed", "err", err, "id", id)
			http.Error(w, "failed to load token", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func makeByAddressHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		address := chi.URLParam(r, "address")
		if address == "" {
			http.Error(w, "address required", http.StatusBadRequest)
			return
		}
		out, err := svc.FindByAddress(r.Context(), address)
		if errors.Is(err, ErrNotFound) {
			http.Error(w, "token not found", http.StatusNotFound)
			return
		}
		if err != nil {
			slog.ErrorContext(r.Context(), "token by address failed", "err", err, "address", address)
			http.Error(w, "failed to load token", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// makeTradesHandler resolves address → tokenID first (same as NestJS:
// `const token = await this.tokenService.findByAddress(address);
// return this.tokenService.getTrades(token.id);`), then lists trades.
// 404 propagates from the address lookup.
func makeTradesHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		address := chi.URLParam(r, "address")
		tok, err := svc.FindByAddress(r.Context(), address)
		if errors.Is(err, ErrNotFound) {
			http.Error(w, "token not found", http.StatusNotFound)
			return
		}
		if err != nil {
			slog.ErrorContext(r.Context(), "token trades address lookup failed", "err", err, "address", address)
			http.Error(w, "failed to load token", http.StatusInternalServerError)
			return
		}
		out, err := svc.ListTradesForToken(r.Context(), tok.ID, 50)
		if err != nil {
			slog.ErrorContext(r.Context(), "token trades failed", "err", err, "tokenId", tok.ID)
			http.Error(w, "failed to load trades", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func makeHoldersHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		address := chi.URLParam(r, "address")
		tok, err := svc.FindByAddress(r.Context(), address)
		if errors.Is(err, ErrNotFound) {
			http.Error(w, "token not found", http.StatusNotFound)
			return
		}
		if err != nil {
			slog.ErrorContext(r.Context(), "token holders address lookup failed", "err", err, "address", address)
			http.Error(w, "failed to load token", http.StatusInternalServerError)
			return
		}
		out, err := svc.ListHoldersForToken(r.Context(), tok.ID, 50)
		if err != nil {
			slog.ErrorContext(r.Context(), "token holders failed", "err", err, "tokenId", tok.ID)
			http.Error(w, "failed to load holders", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// makeCandlesHandler mirrors NestJS getCandles: resolve address → token,
// validate resolution (unknown values fall through to 1h), parse optional
// limit/from/to query params, then return the candle list.
func makeCandlesHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		address := chi.URLParam(r, "address")
		tok, err := svc.FindByAddress(r.Context(), address)
		if errors.Is(err, ErrNotFound) {
			http.Error(w, "token not found", http.StatusNotFound)
			return
		}
		if err != nil {
			slog.ErrorContext(r.Context(), "token candles address lookup failed", "err", err, "address", address)
			http.Error(w, "failed to load token", http.StatusInternalServerError)
			return
		}
		q := r.URL.Query()
		resolution := ParseCandleResolution(q.Get("resolution"))
		limit := parseBoundedLimit(q.Get("limit"), defaultTokenCandleLimit, maxTokenCandleLimit)
		var fromTime, toTime int64
		if raw := q.Get("from"); raw != "" {
			if v, err := strconv.ParseInt(raw, 10, 64); err == nil {
				fromTime = v
			}
		}
		if raw := q.Get("to"); raw != "" {
			if v, err := strconv.ParseInt(raw, 10, 64); err == nil {
				toTime = v
			}
		}
		out, err := svc.GetCandles(r.Context(), tok.ID, resolution, limit, fromTime, toTime)
		if err != nil {
			slog.ErrorContext(r.Context(), "token candles failed", "err", err, "tokenId", tok.ID)
			http.Error(w, "failed to load candles", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// makeStatsHandler mirrors NestJS getStats: resolve address → token, run
// latestPrice/volume24h/priceChange24h in parallel, return the combined
// shape with `address` echoed from the token row (matches the NestJS
// `token.address` reference).
func makeStatsHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		address := chi.URLParam(r, "address")
		tok, err := svc.FindByAddress(r.Context(), address)
		if errors.Is(err, ErrNotFound) {
			http.Error(w, "token not found", http.StatusNotFound)
			return
		}
		if err != nil {
			slog.ErrorContext(r.Context(), "token stats address lookup failed", "err", err, "address", address)
			http.Error(w, "failed to load token", http.StatusInternalServerError)
			return
		}
		out, err := svc.GetStats(r.Context(), tok.ID, tok.Address)
		if err != nil {
			slog.ErrorContext(r.Context(), "token stats failed", "err", err, "tokenId", tok.ID)
			http.Error(w, "failed to load stats", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func makeBySymbolHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		symbol := chi.URLParam(r, "symbol")
		if symbol == "" {
			http.Error(w, "symbol required", http.StatusBadRequest)
			return
		}
		out, err := svc.FindBySymbol(r.Context(), symbol)
		if errors.Is(err, ErrNotFound) {
			http.Error(w, "token not found", http.StatusNotFound)
			return
		}
		if err != nil {
			slog.ErrorContext(r.Context(), "token by symbol failed", "err", err, "symbol", symbol)
			http.Error(w, "failed to load token", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Error("token response encode failed", "err", err)
	}
}
