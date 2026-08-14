// Package activity is the Go port of legacy NestJS activity/
// activity.service.ts. Phase 4a ports the GET /api/activity read path:
// merge five sources (web3_events, web3_transactions, perp_orders,
// perp_trades, user_stakes JOIN staking_pools) into one timeline.
//
// NestJS exposes the endpoint as @Public() @UseGuards(Web3AuthGuard):
// @Public only bypasses the global access guard — Web3AuthGuard still
// requires a web3 JWT, and web-request-owner pins the address to the
// authenticated wallet (no ?address= → JWT address; mismatch → 403).
// The Go mount is web3-guarded in httpx and the handler mirrors the
// pinning via auth.ResolveOwner (2026-06-13, pre-cut parity fix — the
// earlier port honored the raw ?address= only, which served the
// UNFILTERED feed to authenticated callers that omit ?address= and any
// address's filter to any token holder).
package activity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
)

// Item is one row of the merged activity feed. Field order matches the
// NestJS map(): id/source/type/title/subtitle/status/chainId/txHash/
// timestamp. TxHash is nullable for `earn:*` items (UserStake has no
// txHash column).
type Item struct {
	ID        string  `json:"id"`
	Source    string  `json:"source"`
	Type      string  `json:"type"`
	Title     string  `json:"title"`
	Subtitle  string  `json:"subtitle"`
	Status    string  `json:"status"`
	ChainID   int     `json:"chainId"`
	TxHash    *string `json:"txHash"`
	Timestamp string  `json:"timestamp"`
}

// Response is the {items, total} envelope NestJS returns.
type Response struct {
	Items []Item `json:"items"`
	Total int    `json:"total"`
}

// ErrPoolUnavailable mirrors the sibling pattern.
var ErrPoolUnavailable = errors.New("activity repository: database pool not configured")

// Query is the public parameter bag. Empty Address means "no address
// filter" (events still query, transactions/stakes skip per NestJS).
type Query struct {
	Address string
	ChainID *int
	Type    string
	Limit   int
}

// Repository owns the SQL surface. A nil pool yields ErrPoolUnavailable
// from every method.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository binds a repo over the pool. nil is tolerated.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// --- raw row types (kept package-private; Service maps to Item) ---

type web3EventRow struct {
	id              int64
	chainID         int
	contractAddress string
	eventName       string
	txHash          string
	occurredAt      *time.Time
	createdAt       time.Time
}

type web3TxRow struct {
	id              int64
	chainID         int
	txHash          string
	toAddress       *string
	contractAddress *string
	status          string
	txType          *string
	metadata        []byte
	createdAt       time.Time
}

type perpOrderRow struct {
	id        string
	chainID   int
	orderType string
	isLong    bool
	token     string
	sizeDelta string
	status    string
	txHash    *string
	createdAt time.Time
}

type perpTradeRow struct {
	id        string
	chainID   int
	tradeType string
	token     string
	sizeDelta string
	price     string
	txHash    *string
	createdAt time.Time
}

type userStakeRow struct {
	id         string
	amount     string
	stakedAt   time.Time
	unstakedAt *time.Time
	poolName   string
	poolChain  int
}

// --- queries ---

func (r *Repository) listEvents(ctx context.Context, q Query) ([]web3EventRow, error) {
	if r.pool == nil {
		return nil, ErrPoolUnavailable
	}
	limit := clampLimit(q.Limit)
	addr := normalizeAddress(q.Address)

	args := []any{}
	clauses := []string{}
	if q.ChainID != nil {
		args = append(args, *q.ChainID)
		clauses = append(clauses, fmt.Sprintf("chain_id = $%d", len(args)))
	}
	if addr != "" {
		args = append(args, addr)
		clauses = append(clauses, fmt.Sprintf("actor_address = $%d", len(args)))
	}
	where := ""
	if len(clauses) > 0 {
		where = "WHERE " + strings.Join(clauses, " AND ")
	}
	args = append(args, limit)
	sqlText := `SELECT id, chain_id, contract_address, event_name, tx_hash, occurred_at, created_at
		FROM web3_events ` + where + `
		ORDER BY block_number DESC, log_index DESC
		LIMIT $` + strconv.Itoa(len(args))

	rows, err := r.pool.Query(ctx, sqlText, args...)
	if err != nil {
		return nil, fmt.Errorf("query web3_events: %w", err)
	}
	defer rows.Close()
	out := make([]web3EventRow, 0)
	for rows.Next() {
		var row web3EventRow
		if err := rows.Scan(&row.id, &row.chainID, &row.contractAddress, &row.eventName,
			&row.txHash, &row.occurredAt, &row.createdAt); err != nil {
			return nil, fmt.Errorf("scan web3_events: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate web3_events: %w", err)
	}
	return out, nil
}

// CountTransactionsByFromAddress returns the count of web3_transactions
// rows where from_address matches, optionally filtered by chain and
// status. Empty status means "no status filter". Used by
// portfolio.GetSummary for the trackedTransactions + pendingTxCount
// scalars.
func (r *Repository) CountTransactionsByFromAddress(ctx context.Context, fromAddress string, chainID *int, status string) (int, error) {
	if r.pool == nil {
		return 0, ErrPoolUnavailable
	}
	args := []any{fromAddress}
	clauses := []string{"from_address = $1"}
	if chainID != nil {
		args = append(args, *chainID)
		clauses = append(clauses, fmt.Sprintf("chain_id = $%d", len(args)))
	}
	if status != "" {
		args = append(args, status)
		clauses = append(clauses, fmt.Sprintf("status = $%d", len(args)))
	}
	var n int
	if err := r.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM web3_transactions
		WHERE `+strings.Join(clauses, " AND ")+`
	`, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count web3_transactions: %w", err)
	}
	return n, nil
}

// listTransactions returns nothing when address is empty — matches the
// NestJS `normalizedAddress ? prisma.findMany : Promise.resolve([])`
// short-circuit. Without an address filter we'd scan the whole table.
func (r *Repository) listTransactions(ctx context.Context, q Query) ([]web3TxRow, error) {
	addr := normalizeAddress(q.Address)
	if addr == "" {
		return []web3TxRow{}, nil
	}
	if r.pool == nil {
		return nil, ErrPoolUnavailable
	}
	limit := clampLimit(q.Limit)

	args := []any{addr}
	clauses := []string{"from_address = $1"}
	if q.ChainID != nil {
		args = append(args, *q.ChainID)
		clauses = append(clauses, fmt.Sprintf("chain_id = $%d", len(args)))
	}
	args = append(args, limit)
	sqlText := `SELECT id, chain_id, tx_hash, to_address, contract_address, status, tx_type, metadata, created_at
		FROM web3_transactions
		WHERE ` + strings.Join(clauses, " AND ") + `
		ORDER BY block_number DESC, created_at DESC
		LIMIT $` + strconv.Itoa(len(args))

	rows, err := r.pool.Query(ctx, sqlText, args...)
	if err != nil {
		return nil, fmt.Errorf("query web3_transactions: %w", err)
	}
	defer rows.Close()
	out := make([]web3TxRow, 0)
	for rows.Next() {
		var row web3TxRow
		if err := rows.Scan(&row.id, &row.chainID, &row.txHash, &row.toAddress, &row.contractAddress,
			&row.status, &row.txType, &row.metadata, &row.createdAt); err != nil {
			return nil, fmt.Errorf("scan web3_transactions: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate web3_transactions: %w", err)
	}
	return out, nil
}

func (r *Repository) listPerpOrders(ctx context.Context, q Query) ([]perpOrderRow, error) {
	if r.pool == nil {
		return nil, ErrPoolUnavailable
	}
	limit := clampLimit(q.Limit)
	addr := normalizeAddress(q.Address)

	args := []any{}
	clauses := []string{}
	if q.ChainID != nil {
		args = append(args, *q.ChainID)
		clauses = append(clauses, fmt.Sprintf("chain_id = $%d", len(args)))
	}
	if addr != "" {
		args = append(args, addr)
		clauses = append(clauses, fmt.Sprintf("account = $%d", len(args)))
	}
	where := ""
	if len(clauses) > 0 {
		where = "WHERE " + strings.Join(clauses, " AND ")
	}
	args = append(args, limit)
	sqlText := `SELECT id, chain_id, type, "isLong", token, "sizeDelta", status, "txHash", "createdAt"
		FROM perp_orders ` + where + `
		ORDER BY "createdAt" DESC
		LIMIT $` + strconv.Itoa(len(args))

	rows, err := r.pool.Query(ctx, sqlText, args...)
	if err != nil {
		return nil, fmt.Errorf("query perp_orders: %w", err)
	}
	defer rows.Close()
	out := make([]perpOrderRow, 0)
	for rows.Next() {
		var row perpOrderRow
		if err := rows.Scan(&row.id, &row.chainID, &row.orderType, &row.isLong, &row.token,
			&row.sizeDelta, &row.status, &row.txHash, &row.createdAt); err != nil {
			return nil, fmt.Errorf("scan perp_orders: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate perp_orders: %w", err)
	}
	return out, nil
}

func (r *Repository) listPerpTrades(ctx context.Context, q Query) ([]perpTradeRow, error) {
	if r.pool == nil {
		return nil, ErrPoolUnavailable
	}
	limit := clampLimit(q.Limit)
	addr := normalizeAddress(q.Address)

	args := []any{}
	clauses := []string{}
	if q.ChainID != nil {
		args = append(args, *q.ChainID)
		clauses = append(clauses, fmt.Sprintf("chain_id = $%d", len(args)))
	}
	if addr != "" {
		args = append(args, addr)
		clauses = append(clauses, fmt.Sprintf("account = $%d", len(args)))
	}
	where := ""
	if len(clauses) > 0 {
		where = "WHERE " + strings.Join(clauses, " AND ")
	}
	args = append(args, limit)
	sqlText := `SELECT id, chain_id, type, token, "sizeDelta", price, "txHash", "createdAt"
		FROM perp_trades ` + where + `
		ORDER BY "createdAt" DESC
		LIMIT $` + strconv.Itoa(len(args))

	rows, err := r.pool.Query(ctx, sqlText, args...)
	if err != nil {
		return nil, fmt.Errorf("query perp_trades: %w", err)
	}
	defer rows.Close()
	out := make([]perpTradeRow, 0)
	for rows.Next() {
		var row perpTradeRow
		if err := rows.Scan(&row.id, &row.chainID, &row.tradeType, &row.token,
			&row.sizeDelta, &row.price, &row.txHash, &row.createdAt); err != nil {
			return nil, fmt.Errorf("scan perp_trades: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate perp_trades: %w", err)
	}
	return out, nil
}

// listUserStakes is also short-circuited when address is empty —
// NestJS skips the query in that case to avoid scanning every user's
// stakes for an anonymous request.
func (r *Repository) listUserStakes(ctx context.Context, q Query) ([]userStakeRow, error) {
	addr := normalizeAddress(q.Address)
	if addr == "" {
		return []userStakeRow{}, nil
	}
	if r.pool == nil {
		return nil, ErrPoolUnavailable
	}
	limit := clampLimit(q.Limit)

	rows, err := r.pool.Query(ctx, `
		SELECT s.id, s.amount::text, s.staked_at, s.unstaked_at, p.name, p.chain_id
		FROM user_stakes s
		JOIN staking_pools p ON p.id = s.pool_id
		WHERE s.user_address = $1
		ORDER BY s.staked_at DESC
		LIMIT $2
	`, addr, limit)
	if err != nil {
		return nil, fmt.Errorf("query user_stakes: %w", err)
	}
	defer rows.Close()
	out := make([]userStakeRow, 0)
	for rows.Next() {
		var row userStakeRow
		if err := rows.Scan(&row.id, &row.amount, &row.stakedAt, &row.unstakedAt,
			&row.poolName, &row.poolChain); err != nil {
			return nil, fmt.Errorf("scan user_stakes: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate user_stakes: %w", err)
	}
	return out, nil
}

// --- service ---

// Service orchestrates the five parallel queries and merges them. A
// pool-unavailable error from any source degrades to an empty feed,
// matching the sibling discover/campaign/markets pattern.
type Service struct {
	repo *Repository
}

// NewService binds a service over a repo.
func NewService(repo *Repository) *Service {
	return &Service{repo: repo}
}

// GetActivity merges the five sources and returns the {items, total}
// envelope. Degraded mode returns an empty result instead of 500.
func (s *Service) GetActivity(ctx context.Context, q Query) (Response, error) {
	limit := clampLimit(q.Limit)
	q.Limit = limit

	g, gctx := errgroup.WithContext(ctx)
	var (
		events []web3EventRow
		txs    []web3TxRow
		orders []perpOrderRow
		trades []perpTradeRow
		stakes []userStakeRow
	)
	g.Go(func() error { var e error; events, e = s.repo.listEvents(gctx, q); return e })
	g.Go(func() error { var e error; txs, e = s.repo.listTransactions(gctx, q); return e })
	g.Go(func() error { var e error; orders, e = s.repo.listPerpOrders(gctx, q); return e })
	g.Go(func() error { var e error; trades, e = s.repo.listPerpTrades(gctx, q); return e })
	g.Go(func() error { var e error; stakes, e = s.repo.listUserStakes(gctx, q); return e })

	if err := g.Wait(); err != nil {
		if errors.Is(err, ErrPoolUnavailable) {
			return Response{Items: []Item{}, Total: 0}, nil
		}
		return Response{}, err
	}

	indexedTx := make(map[string]struct{}, len(events))
	for _, ev := range events {
		indexedTx[strings.ToLower(ev.txHash)] = struct{}{}
	}

	items := make([]Item, 0, len(events)+len(txs)+len(orders)+len(trades)+len(stakes))

	// Transactions first to match NestJS ordering before the sort
	// (the final sort is by timestamp DESC anyway, so insertion order
	// only matters for stable-sort tie-breaking).
	for _, tx := range txs {
		if tx.status == "confirmed" {
			if _, dup := indexedTx[strings.ToLower(tx.txHash)]; dup {
				continue
			}
		}
		subtitle := ""
		switch {
		case tx.toAddress != nil:
			subtitle = *tx.toAddress
		case tx.contractAddress != nil:
			subtitle = *tx.contractAddress
		default:
			subtitle = tx.txHash
		}
		hash := tx.txHash
		items = append(items, Item{
			ID:        fmt.Sprintf("tx:%d", tx.id),
			Source:    "onchain",
			Type:      normalizeTransactionType(tx.txType),
			Title:     buildTransactionTitle(tx.txType),
			Subtitle:  subtitle,
			Status:    deriveTxDisplayStatus(tx.status, tx.metadata),
			ChainID:   tx.chainID,
			TxHash:    &hash,
			Timestamp: tx.createdAt.UTC().Format(time.RFC3339Nano),
		})
	}

	for _, ev := range events {
		ts := ev.createdAt
		if ev.occurredAt != nil {
			ts = *ev.occurredAt
		}
		hash := ev.txHash
		items = append(items, Item{
			ID:        fmt.Sprintf("event:%d", ev.id),
			Source:    "onchain",
			Type:      normalizeEventType(ev.eventName),
			Title:     ev.eventName,
			Subtitle:  ev.contractAddress,
			Status:    "confirmed",
			ChainID:   ev.chainID,
			TxHash:    &hash,
			Timestamp: ts.UTC().Format(time.RFC3339Nano),
		})
	}

	for _, order := range orders {
		side := "Short"
		if order.isLong {
			side = "Long"
		}
		var hashPtr *string
		if order.txHash != nil {
			h := *order.txHash
			hashPtr = &h
		}
		items = append(items, Item{
			ID:        "paper-order:" + order.id,
			Source:    "paper-trade",
			Type:      "paper-order",
			Title:     fmt.Sprintf("%s %s %s", order.orderType, side, order.token),
			Subtitle:  "Size " + order.sizeDelta,
			Status:    order.status,
			ChainID:   order.chainID,
			TxHash:    hashPtr,
			Timestamp: order.createdAt.UTC().Format(time.RFC3339Nano),
		})
	}

	for _, trade := range trades {
		var hashPtr *string
		if trade.txHash != nil {
			h := *trade.txHash
			hashPtr = &h
		}
		items = append(items, Item{
			ID:        "paper-fill:" + trade.id,
			Source:    "paper-trade",
			Type:      "paper-fill",
			Title:     fmt.Sprintf("%s %s", trade.tradeType, trade.token),
			Subtitle:  fmt.Sprintf("Price %s / Size %s", trade.price, trade.sizeDelta),
			Status:    "filled",
			ChainID:   trade.chainID,
			TxHash:    hashPtr,
			Timestamp: trade.createdAt.UTC().Format(time.RFC3339Nano),
		})
	}

	for _, stake := range stakes {
		title := "Stake " + stake.poolName
		typ := "stake"
		status := "active"
		ts := stake.stakedAt
		if stake.unstakedAt != nil {
			title = "Unstake " + stake.poolName
			typ = "unstake"
			status = "closed"
			ts = *stake.unstakedAt
		}
		items = append(items, Item{
			ID:        "earn:" + stake.id,
			Source:    "earn",
			Type:      typ,
			Title:     title,
			Subtitle:  "Amount " + stake.amount,
			Status:    status,
			ChainID:   stake.poolChain,
			TxHash:    nil,
			Timestamp: ts.UTC().Format(time.RFC3339Nano),
		})
	}

	if q.Type != "" {
		filtered := items[:0]
		for _, it := range items {
			if it.Type == q.Type || it.Source == q.Type {
				filtered = append(filtered, it)
			}
		}
		items = filtered
	}

	sort.SliceStable(items, func(i, j int) bool {
		return items[i].Timestamp > items[j].Timestamp
	})

	if len(items) > limit {
		items = items[:limit]
	}

	return Response{Items: items, Total: len(items)}, nil
}

// --- helpers ---

func clampLimit(limit int) int {
	if limit <= 0 {
		return 30
	}
	if limit < 1 {
		return 1
	}
	if limit > 100 {
		return 100
	}
	return limit
}

func normalizeAddress(addr string) string {
	if addr == "" {
		return ""
	}
	return strings.ToLower(addr)
}

func normalizeEventType(name string) string {
	return strings.Join(strings.Fields(strings.ToLower(name)), "-")
}

func normalizeTransactionType(txType *string) string {
	if txType == nil || *txType == "" {
		return "transaction"
	}
	return strings.Join(strings.Fields(strings.ToLower(*txType)), "-")
}

func buildTransactionTitle(txType *string) string {
	switch normalizeTransactionType(txType) {
	case "approve", "approval":
		return "Token approval submitted"
	case "swap":
		return "Swap submitted"
	case "open-position":
		return "Open position submitted"
	case "close-position":
		return "Close position submitted"
	case "mint":
		return "Mint submitted"
	case "create-token":
		return "Token creation submitted"
	case "faucet":
		return "Faucet claim submitted"
	default:
		return "Onchain transaction submitted"
	}
}

// deriveTxDisplayStatus mirrors hasIndexedMetadata: a confirmed tx
// whose metadata.indexing.indexedAt is set surfaces as "indexed" so
// the FE can mark it as fully processed (not just on-chain).
func deriveTxDisplayStatus(status string, metadata []byte) string {
	if hasIndexedMetadata(metadata) {
		return "indexed"
	}
	if status == "" {
		return "pending"
	}
	return strings.ToLower(status)
}

func hasIndexedMetadata(raw []byte) bool {
	if len(raw) == 0 {
		return false
	}
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		return false
	}
	indexing, ok := v["indexing"].(map[string]any)
	if !ok {
		return false
	}
	_, ok = indexing["indexedAt"].(string)
	return ok
}

// --- router ---

// Router mounts GET / on a chi sub-router. Mounted at /api/activity.
func Router(svc *Service) chi.Router {
	r := chi.NewRouter()
	r.Get("/", makeHandler(svc))
	return r
}

func makeHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		// Mirror NestJS web3-request-owner: default to the authenticated
		// wallet when no ?address= is given, and reject a query address
		// that doesn't match it (403). Same pinning as portfolio.
		address, err := auth.ResolveOwner(auth.AddressFromContext(r.Context()), q.Get("address"))
		if err != nil {
			mapResolveOwnerErr(w, err)
			return
		}
		query := Query{
			Address: address,
			Type:    q.Get("type"),
		}
		if raw := q.Get("chainId"); raw != "" {
			if v, err := strconv.Atoi(raw); err == nil {
				query.ChainID = &v
			}
		}
		if raw := q.Get("limit"); raw != "" {
			if v, err := strconv.Atoi(raw); err == nil {
				query.Limit = v
			}
		}

		out, err := svc.GetActivity(r.Context(), query)
		if err != nil {
			slog.ErrorContext(r.Context(), "activity get failed", "err", err)
			http.Error(w, "failed to load activity", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// mapResolveOwnerErr maps the auth.ResolveOwner sentinels to the same statuses
// the deployed NestJS web3-request-owner helper returns: 401 (no authenticated
// wallet), 403 (requested ≠ authenticated), 400 (malformed requested address).
func mapResolveOwnerErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrMissingAuthenticated):
		http.Error(w, err.Error(), http.StatusUnauthorized)
	case errors.Is(err, auth.ErrOwnerMismatch):
		http.Error(w, err.Error(), http.StatusForbidden)
	default:
		http.Error(w, err.Error(), http.StatusBadRequest)
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Error("activity response encode failed", "err", err)
	}
}
