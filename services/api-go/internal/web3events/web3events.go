// Package web3events is the Go port of legacy NestJS web3-events/
// web3-events.service.ts and web3-transactions.service.ts.
//
// Phase 3a added GetStats + CountByActor + CountByContractAndName for
// discover/portfolio consumers. Phase 4t adds the 5 read paths the
// HTTP controller (web3-events.controller.ts) exposes: paged events
// list, by-tx, by-user, recent transactions, and the full stats shape
// (with eventCounts breakdown).
package web3events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Stats matches the fields of StatsResponse that /api/discover/home
// consumes: totalEvents (count of web3_events for chain) and
// indexerBlock (the catch-up cursor stored in web3_indexer_state).
type Stats struct {
	TotalEvents  int64
	IndexerBlock string
}

// EventCount mirrors the `{event, count}` shape returned by the NestJS
// /web3-events/stats endpoint.
type EventCount struct {
	Event string `json:"event"`
	Count int64  `json:"count"`
}

// FullStats is the full /web3-events/stats response (eventCounts +
// latestBlock + indexerBlock + totalEvents). Used by the HTTP handler;
// discover keeps using the lighter Stats shape via GetStats.
type FullStats struct {
	EventCounts  []EventCount `json:"eventCounts"`
	LatestBlock  string       `json:"latestBlock"`
	IndexerBlock string       `json:"indexerBlock"`
	TotalEvents  int64        `json:"totalEvents"`
}

// EventRow mirrors the Prisma Web3Event row shape, with BigInt block
// number serialized as string (matching the NestJS response).
type EventRow struct {
	ID              int64           `json:"id"`
	ChainID         int             `json:"chainId"`
	ContractAddress string          `json:"contractAddress"`
	EventName       string          `json:"eventName"`
	TxHash          string          `json:"txHash"`
	LogIndex        int             `json:"logIndex"`
	BlockNumber     string          `json:"blockNumber"`
	ActorAddress    *string         `json:"actorAddress"`
	Args            json.RawMessage `json:"args"`
	OccurredAt      *time.Time      `json:"occurredAt"`
	CreatedAt       time.Time       `json:"createdAt"`
}

// TransactionRow mirrors Web3Transaction; gasUsed/gasPrice/blockNumber
// are stringified to keep BigInt-safety.
type TransactionRow struct {
	ID              int64           `json:"id"`
	ChainID         int             `json:"chainId"`
	TxHash          string          `json:"txHash"`
	FromAddress     string          `json:"fromAddress"`
	ToAddress       *string         `json:"toAddress"`
	ContractAddress *string         `json:"contractAddress"`
	Value           *string         `json:"value"`
	GasUsed         *string         `json:"gasUsed"`
	GasPrice        *string         `json:"gasPrice"`
	BlockNumber     string          `json:"blockNumber"`
	Status          string          `json:"status"`
	TxType          *string         `json:"txType"`
	Metadata        json.RawMessage `json:"metadata"`
	CreatedAt       time.Time       `json:"createdAt"`
}

// ListEventsQuery matches GetEventsDto with sensible defaults applied.
// The caller is responsible for clamping page/limit upfront.
type ListEventsQuery struct {
	Page            int
	Limit           int
	EventName       string
	ActorAddress    string
	ContractAddress string
	ChainID         int
	FromBlock       *int64
	ToBlock         *int64
}

// ListEventsResult mirrors the {data, pagination} response.
type ListEventsResult struct {
	Data       []EventRow `json:"data"`
	Pagination Pagination `json:"pagination"`
}

// Pagination matches the NestJS payload exactly.
type Pagination struct {
	Page       int   `json:"page"`
	Limit      int   `json:"limit"`
	Total      int64 `json:"total"`
	TotalPages int64 `json:"totalPages"`
}

// Repository wraps the pgx pool.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository binds the repo to a pgxpool.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// ErrPoolUnavailable mirrors the pattern from sibling internal/* repos.
var ErrPoolUnavailable = fmt.Errorf("web3events repository: database pool not configured")

// GetStats returns the counters discover/home cares about.
func (r *Repository) GetStats(ctx context.Context, chainID int) (Stats, error) {
	if r.pool == nil {
		return Stats{}, ErrPoolUnavailable
	}

	var total int64
	if err := r.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM web3_events WHERE chain_id = $1
	`, chainID).Scan(&total); err != nil {
		return Stats{}, fmt.Errorf("count web3_events: %w", err)
	}

	indexerBlock, err := r.queryIndexerBlock(ctx, chainID)
	if err != nil {
		return Stats{}, err
	}

	return Stats{
		TotalEvents:  total,
		IndexerBlock: indexerBlock,
	}, nil
}

// GetFullStats returns the full /web3-events/stats response shape.
func (r *Repository) GetFullStats(ctx context.Context, chainID int) (FullStats, error) {
	if r.pool == nil {
		return FullStats{}, ErrPoolUnavailable
	}

	rows, err := r.pool.Query(ctx, `
		SELECT event_name, COUNT(*)::bigint
		FROM web3_events
		WHERE chain_id = $1
		GROUP BY event_name
	`, chainID)
	if err != nil {
		return FullStats{}, fmt.Errorf("group web3_events: %w", err)
	}
	defer rows.Close()

	counts := make([]EventCount, 0)
	var total int64
	for rows.Next() {
		var ec EventCount
		if err := rows.Scan(&ec.Event, &ec.Count); err != nil {
			return FullStats{}, fmt.Errorf("scan event count: %w", err)
		}
		counts = append(counts, ec)
		total += ec.Count
	}
	if err := rows.Err(); err != nil {
		return FullStats{}, fmt.Errorf("iterate event counts: %w", err)
	}

	var latestBlock int64
	err = r.pool.QueryRow(ctx, `
		SELECT block_number FROM web3_events
		WHERE chain_id = $1
		ORDER BY block_number DESC
		LIMIT 1
	`, chainID).Scan(&latestBlock)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return FullStats{}, fmt.Errorf("query latest event block: %w", err)
	}

	indexerBlock, err := r.queryIndexerBlock(ctx, chainID)
	if err != nil {
		return FullStats{}, err
	}

	return FullStats{
		EventCounts:  counts,
		LatestBlock:  strconv.FormatInt(latestBlock, 10),
		IndexerBlock: indexerBlock,
		TotalEvents:  total,
	}, nil
}

func (r *Repository) queryIndexerBlock(ctx context.Context, chainID int) (string, error) {
	var indexerBlock int64
	err := r.pool.QueryRow(ctx, `
		SELECT last_processed_block FROM web3_indexer_state
		WHERE chain_id = $1
		ORDER BY id ASC
		LIMIT 1
	`, chainID).Scan(&indexerBlock)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("query web3_indexer_state: %w", err)
	}
	return strconv.FormatInt(indexerBlock, 10), nil
}

// CountByActor returns the count of web3_events rows authored by
// actorAddress, optionally filtered by chain. Used by
// portfolio.GetSummary's `recentActivityCount` floor.
func (r *Repository) CountByActor(ctx context.Context, actorAddress string, chainID *int) (int64, error) {
	if r.pool == nil {
		return 0, ErrPoolUnavailable
	}
	args := []any{actorAddress}
	clauses := []string{"actor_address = $1"}
	if chainID != nil {
		args = append(args, *chainID)
		clauses = append(clauses, "chain_id = $2")
	}
	var n int64
	if err := r.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM web3_events
		WHERE `+joinAnd(clauses)+`
	`, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count web3_events (by actor): %w", err)
	}
	return n, nil
}

func joinAnd(parts []string) string {
	out := parts[0]
	for _, p := range parts[1:] {
		out += " AND " + p
	}
	return out
}

// CountByContractAndName returns the count of web3_events rows that
// match a contract address (case-insensitive) and event name. Used by
// portfolio.getAssetDetail to surface the per-token approval count.
func (r *Repository) CountByContractAndName(ctx context.Context, contractAddress, eventName string) (int64, error) {
	if r.pool == nil {
		return 0, ErrPoolUnavailable
	}
	var n int64
	if err := r.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM web3_events
		WHERE LOWER(contract_address) = LOWER($1) AND event_name = $2
	`, contractAddress, eventName).Scan(&n); err != nil {
		return 0, fmt.Errorf("count web3_events (by contract+name): %w", err)
	}
	return n, nil
}

// ListEvents paginates the web3_events table, mirroring the NestJS
// getEvents method (filter on eventName / actorAddress / contractAddress
// / chainId / fromBlock / toBlock, order by blockNumber desc).
func (r *Repository) ListEvents(ctx context.Context, q ListEventsQuery) (ListEventsResult, error) {
	if r.pool == nil {
		return ListEventsResult{}, ErrPoolUnavailable
	}

	clauses := []string{"chain_id = $1"}
	args := []any{q.ChainID}
	addArg := func(value any) string {
		args = append(args, value)
		return "$" + strconv.Itoa(len(args))
	}

	if q.EventName != "" {
		clauses = append(clauses, "event_name = "+addArg(q.EventName))
	}
	if q.ActorAddress != "" {
		clauses = append(clauses, "actor_address = "+addArg(strings.ToLower(q.ActorAddress)))
	}
	if q.ContractAddress != "" {
		clauses = append(clauses, "contract_address = "+addArg(strings.ToLower(q.ContractAddress)))
	}
	if q.FromBlock != nil {
		clauses = append(clauses, "block_number >= "+addArg(*q.FromBlock))
	}
	if q.ToBlock != nil {
		clauses = append(clauses, "block_number <= "+addArg(*q.ToBlock))
	}

	where := joinAnd(clauses)

	var total int64
	if err := r.pool.QueryRow(ctx, "SELECT COUNT(*) FROM web3_events WHERE "+where, args...).Scan(&total); err != nil {
		return ListEventsResult{}, fmt.Errorf("count web3_events: %w", err)
	}

	limit := boundedLimit(q.Limit, defaultEventListLimit, maxEventListLimit)
	page := boundedPage(q.Page, defaultEventListPage, maxEventListPage)
	offset := (page - 1) * limit

	listArgs := append(args, limit, offset)
	limitPlaceholder := "$" + strconv.Itoa(len(args)+1)
	offsetPlaceholder := "$" + strconv.Itoa(len(args)+2)

	rows, err := r.pool.Query(ctx, `
		SELECT id, chain_id, contract_address, event_name, tx_hash,
		       log_index, block_number, actor_address, args,
		       occurred_at, created_at
		FROM web3_events
		WHERE `+where+`
		ORDER BY block_number DESC
		LIMIT `+limitPlaceholder+` OFFSET `+offsetPlaceholder+`
	`, listArgs...)
	if err != nil {
		return ListEventsResult{}, fmt.Errorf("query web3_events: %w", err)
	}
	defer rows.Close()

	events, err := scanEventRows(rows)
	if err != nil {
		return ListEventsResult{}, err
	}

	totalPages := int64(0)
	if total > 0 && limit > 0 {
		totalPages = (total + int64(limit) - 1) / int64(limit)
	}

	return ListEventsResult{
		Data: events,
		Pagination: Pagination{
			Page:       page,
			Limit:      limit,
			Total:      total,
			TotalPages: totalPages,
		},
	}, nil
}

// EventsByTxHash mirrors getEventsByTxHash — lowercases the hash and
// orders by logIndex asc.
func (r *Repository) EventsByTxHash(ctx context.Context, txHash string, chainID int) ([]EventRow, error) {
	if r.pool == nil {
		return nil, ErrPoolUnavailable
	}

	rows, err := r.pool.Query(ctx, `
		SELECT id, chain_id, contract_address, event_name, tx_hash,
		       log_index, block_number, actor_address, args,
		       occurred_at, created_at
		FROM web3_events
		WHERE tx_hash = $1 AND chain_id = $2
		ORDER BY log_index ASC
	`, strings.ToLower(txHash), chainID)
	if err != nil {
		return nil, fmt.Errorf("query web3_events by tx_hash: %w", err)
	}
	defer rows.Close()
	return scanEventRows(rows)
}

// UserEvents mirrors getUserEvents — filters by actorAddress (lower)
// and chainId, orders by blockNumber desc, applies a limit.
func (r *Repository) UserEvents(ctx context.Context, address string, chainID, limit int) ([]EventRow, error) {
	if r.pool == nil {
		return nil, ErrPoolUnavailable
	}
	limit = boundedLimit(limit, defaultUserEventLimit, maxUserEventLimit)

	rows, err := r.pool.Query(ctx, `
		SELECT id, chain_id, contract_address, event_name, tx_hash,
		       log_index, block_number, actor_address, args,
		       occurred_at, created_at
		FROM web3_events
		WHERE chain_id = $1 AND actor_address = $2
		ORDER BY block_number DESC
		LIMIT $3
	`, chainID, strings.ToLower(address), limit)
	if err != nil {
		return nil, fmt.Errorf("query web3_events by user: %w", err)
	}
	defer rows.Close()
	return scanEventRows(rows)
}

// RecentTransactions mirrors getRecentTransactions — newest first by
// blockNumber, capped by limit.
func (r *Repository) RecentTransactions(ctx context.Context, chainID, limit int) ([]TransactionRow, error) {
	if r.pool == nil {
		return nil, ErrPoolUnavailable
	}
	limit = boundedLimit(limit, defaultRecentTransactionCap, maxRecentTransactionCap)

	rows, err := r.pool.Query(ctx, `
		SELECT id, chain_id, tx_hash, from_address, to_address,
		       contract_address, value, gas_used, gas_price,
		       block_number, status, tx_type, metadata, created_at
		FROM web3_transactions
		WHERE chain_id = $1
		ORDER BY block_number DESC
		LIMIT $2
	`, chainID, limit)
	if err != nil {
		return nil, fmt.Errorf("query web3_transactions: %w", err)
	}
	defer rows.Close()

	out := make([]TransactionRow, 0, limit)
	for rows.Next() {
		var (
			tx          TransactionRow
			blockNumber int64
			gasUsed     *int64
			gasPrice    *int64
			metadata    []byte
		)
		if err := rows.Scan(
			&tx.ID, &tx.ChainID, &tx.TxHash, &tx.FromAddress, &tx.ToAddress,
			&tx.ContractAddress, &tx.Value, &gasUsed, &gasPrice,
			&blockNumber, &tx.Status, &tx.TxType, &metadata, &tx.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan web3_transactions: %w", err)
		}
		tx.BlockNumber = strconv.FormatInt(blockNumber, 10)
		if gasUsed != nil {
			s := strconv.FormatInt(*gasUsed, 10)
			tx.GasUsed = &s
		}
		if gasPrice != nil {
			s := strconv.FormatInt(*gasPrice, 10)
			tx.GasPrice = &s
		}
		if len(metadata) > 0 {
			tx.Metadata = metadata
		}
		out = append(out, tx)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate web3_transactions: %w", err)
	}
	return out, nil
}

// SubmitTxInput is the normalized payload for reportSubmittedTransaction
// (Go port of web3-transactions.service.ts normalizeSubmittedInput). nil
// pointers / empty metadata mean "not provided" (kept on conflict).
type SubmitTxInput struct {
	ChainID         int
	TxHash          string
	FromAddress     string
	ToAddress       *string
	ContractAddress *string
	Value           *string
	TxType          *string
	Metadata        json.RawMessage
}

// ReceiptTxInput is the normalized payload for reportTransactionReceipt.
type ReceiptTxInput struct {
	ChainID         int
	TxHash          string
	FromAddress     string
	ToAddress       *string
	ContractAddress *string
	Value           *string
	TxType          *string
	Metadata        json.RawMessage
	Status          string
	BlockNumber     int64
	GasUsed         *int64
	GasPrice        *int64
}

// metaArg returns a jsonb-encodable value (a JSON string) or nil so an absent
// metadata payload inserts NULL / keeps the existing row value on conflict.
func metaArg(m json.RawMessage) any {
	if len(m) == 0 || string(m) == "null" {
		return nil
	}
	return string(m)
}

// UpsertSubmittedTransaction mirrors reportSubmittedTransaction: upsert by
// (chainId, txHash) — create with status='pending' and blockNumber=0; on
// conflict refresh the address/value/type/metadata (keeping existing where the
// new value is absent) and reset status to 'pending'.
func (r *Repository) UpsertSubmittedTransaction(ctx context.Context, in SubmitTxInput) (TransactionRow, error) {
	if r.pool == nil {
		return TransactionRow{}, ErrPoolUnavailable
	}
	row := r.pool.QueryRow(ctx, `
		INSERT INTO web3_transactions
			(chain_id, tx_hash, from_address, to_address, contract_address,
			 value, gas_used, gas_price, block_number, status, tx_type, metadata, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,NULL,NULL,0,'pending',$7,$8, now())
		ON CONFLICT (chain_id, tx_hash) DO UPDATE SET
			from_address     = EXCLUDED.from_address,
			to_address       = COALESCE(EXCLUDED.to_address, web3_transactions.to_address),
			contract_address = COALESCE(EXCLUDED.contract_address, web3_transactions.contract_address),
			value            = COALESCE(EXCLUDED.value, web3_transactions.value),
			tx_type          = COALESCE(EXCLUDED.tx_type, web3_transactions.tx_type),
			metadata         = COALESCE(EXCLUDED.metadata, web3_transactions.metadata),
			status           = 'pending'
		RETURNING id, chain_id, tx_hash, from_address, to_address, contract_address,
		          value, gas_used, gas_price, block_number, status, tx_type, metadata, created_at
	`, in.ChainID, in.TxHash, in.FromAddress, in.ToAddress, in.ContractAddress,
		in.Value, in.TxType, metaArg(in.Metadata))
	return scanTxRow(row)
}

// UpsertTransactionReceipt mirrors reportTransactionReceipt: same upsert keyed
// on (chainId, txHash), additionally writing the confirmed block/gas/status.
func (r *Repository) UpsertTransactionReceipt(ctx context.Context, in ReceiptTxInput) (TransactionRow, error) {
	if r.pool == nil {
		return TransactionRow{}, ErrPoolUnavailable
	}
	row := r.pool.QueryRow(ctx, `
		INSERT INTO web3_transactions
			(chain_id, tx_hash, from_address, to_address, contract_address,
			 value, gas_used, gas_price, block_number, status, tx_type, metadata, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12, now())
		ON CONFLICT (chain_id, tx_hash) DO UPDATE SET
			from_address     = EXCLUDED.from_address,
			to_address       = COALESCE(EXCLUDED.to_address, web3_transactions.to_address),
			contract_address = COALESCE(EXCLUDED.contract_address, web3_transactions.contract_address),
			value            = COALESCE(EXCLUDED.value, web3_transactions.value),
			tx_type          = COALESCE(EXCLUDED.tx_type, web3_transactions.tx_type),
			metadata         = COALESCE(EXCLUDED.metadata, web3_transactions.metadata),
			block_number     = EXCLUDED.block_number,
			gas_used         = EXCLUDED.gas_used,
			gas_price        = EXCLUDED.gas_price,
			status           = EXCLUDED.status
		RETURNING id, chain_id, tx_hash, from_address, to_address, contract_address,
		          value, gas_used, gas_price, block_number, status, tx_type, metadata, created_at
	`, in.ChainID, in.TxHash, in.FromAddress, in.ToAddress, in.ContractAddress,
		in.Value, in.GasUsed, in.GasPrice, in.BlockNumber, in.Status, in.TxType, metaArg(in.Metadata))
	return scanTxRow(row)
}

func scanTxRow(row pgx.Row) (TransactionRow, error) {
	var (
		tx          TransactionRow
		blockNumber int64
		gasUsed     *int64
		gasPrice    *int64
		metadata    []byte
	)
	if err := row.Scan(
		&tx.ID, &tx.ChainID, &tx.TxHash, &tx.FromAddress, &tx.ToAddress,
		&tx.ContractAddress, &tx.Value, &gasUsed, &gasPrice,
		&blockNumber, &tx.Status, &tx.TxType, &metadata, &tx.CreatedAt,
	); err != nil {
		return TransactionRow{}, fmt.Errorf("scan web3_transactions upsert: %w", err)
	}
	tx.BlockNumber = strconv.FormatInt(blockNumber, 10)
	if gasUsed != nil {
		s := strconv.FormatInt(*gasUsed, 10)
		tx.GasUsed = &s
	}
	if gasPrice != nil {
		s := strconv.FormatInt(*gasPrice, 10)
		tx.GasPrice = &s
	}
	if len(metadata) > 0 {
		tx.Metadata = metadata
	}
	return tx, nil
}

func scanEventRows(rows pgx.Rows) ([]EventRow, error) {
	out := make([]EventRow, 0)
	for rows.Next() {
		var (
			ev          EventRow
			blockNumber int64
			args        []byte
		)
		if err := rows.Scan(
			&ev.ID, &ev.ChainID, &ev.ContractAddress, &ev.EventName, &ev.TxHash,
			&ev.LogIndex, &blockNumber, &ev.ActorAddress, &args,
			&ev.OccurredAt, &ev.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan web3_events: %w", err)
		}
		ev.BlockNumber = strconv.FormatInt(blockNumber, 10)
		if len(args) > 0 {
			ev.Args = args
		}
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate web3_events: %w", err)
	}
	return out, nil
}
