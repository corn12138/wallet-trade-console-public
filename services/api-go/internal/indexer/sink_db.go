package indexer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/eventbus"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/num"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrSinkPoolNil is returned by DBSink when it has no database pool.
var ErrSinkPoolNil = errors.New("indexer: db sink pool nil")

// DBSink is a drop-in EventSink (same interface CountingSink satisfies).
var _ EventSink = (*DBSink)(nil)

// DBSink persists a raw event and its business projection in one transaction.
// The raw INSERT uses the (chainId, contractAddress, txHash, logIndex) key, so a
// backfill re-scan, reorg rewind, or retry repairs the same event without
// duplicating it. TokenCreated adds its bonding curve to the worker's watch set
// only after that transaction commits.
type DBSink struct {
	pool            *pgxpool.Pool
	beginProjection beginProjectionTx
	repairStore     projectionStore
	watcher         CurveWatcher
	events          TokenEventPublisher
	symbols         SymbolResolver
}

// TokenEventPublisher receives realtime notifications AFTER a projection
// committed. eventbus.PgPublisher satisfies it; nil disables publishing.
type TokenEventPublisher interface {
	PublishTokenEvent(ctx context.Context, ev eventbus.TokenEvent) error
}

// SymbolResolver maps a perp market's index-token address to its market
// symbol (perp_trades/perp_positions store SYMBOLS, chain events carry
// addresses). cmd/indexer wires one from the markets catalog + deployments
// registry; a missing resolver keeps the projection retryable.
type SymbolResolver interface {
	SymbolForIndexToken(chainID int, indexToken string) string
}

// SetEventPublisher wires the post-commit realtime publisher (nil = off).
func (s *DBSink) SetEventPublisher(p TokenEventPublisher) { s.events = p }

// SetSymbolResolver wires the perp index-token→symbol lookup.
func (s *DBSink) SetSymbolResolver(r SymbolResolver) { s.symbols = r }

// publish fans one event to the bus, logging (never failing) on error — the
// DB projection is already durable; realtime is best-effort delivery.
func (s *DBSink) publish(ctx context.Context, kind, tokenAddr string, payload any) {
	if s.events == nil || tokenAddr == "" {
		return
	}
	ev, err := eventbus.NewTokenEvent(kind, tokenAddr, payload)
	if err != nil {
		slog.WarnContext(ctx, "indexer: build realtime event failed", "kind", kind, "err", err)
		return
	}
	if err := s.events.PublishTokenEvent(ctx, ev); err != nil {
		slog.WarnContext(ctx, "indexer: publish realtime event failed", "kind", kind, "err", err)
	}
}

// NewDBSink binds the sink to a pool. A nil pool makes HandleEvent return
// ErrSinkPoolNil (the worker logs it and retries next tick) rather than
// silently dropping events.
func NewDBSink(pool *pgxpool.Pool) *DBSink {
	sink := &DBSink{pool: pool}
	if pool != nil {
		sink.beginProjection = func(ctx context.Context) (projectionTx, error) {
			return pool.Begin(ctx)
		}
		sink.repairStore = pool
	}
	return sink
}

// SetCurveWatcher wires the worker that should start scanning newly created
// bonding curves. It's a post-construction setter because the sink and worker
// reference each other (the worker's runner holds the sink; the sink holds the
// worker as a CurveWatcher). nil is fine — the sink simply won't request new
// watches, and Buy/Sell only arrive for statically-watched curves.
func (s *DBSink) SetCurveWatcher(w CurveWatcher) { s.watcher = w }

// handleGraduated marks the token whose bonding curve emitted Graduated as
// GRADUATED and publishes the realtime graduation event. Idempotent by
// construction (status set is absorbing).
func (s *DBSink) handleGraduated(ctx context.Context, projection *eventProjection, ev ParsedEvent) error {
	curve := strings.ToLower(ev.ContractAddress)
	a := ev.Parsed.RuntimeArgs

	var tokenAddr *string
	err := projection.store.QueryRow(ctx, `
		UPDATE tokens SET status = 'GRADUATED', updated_at = NOW()
		WHERE bonding_curve = $1
		RETURNING address
	`, curve).Scan(&tokenAddr)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("graduated token dependency missing for curve %s", curve)
	}
	if err != nil {
		return fmt.Errorf("mark token graduated (curve %s): %w", curve, err)
	}

	if tokenAddr != nil && *tokenAddr != "" {
		payload := eventbus.GraduationPayload{
			TokenAddress: strings.ToLower(*tokenAddr),
			Timestamp:    time.Now().UTC().Format(time.RFC3339),
		}
		if mc := argBigInt(a["marketCap"]); mc != nil {
			payload.MarketCap = mc.String()
		}
		payload.LPToken = lowerAddr(a["lpToken"])
		if lp := argBigInt(a["lpAmount"]); lp != nil {
			payload.LPAmount = lp.String()
		}
		projection.publishAfterCommit(ctx, s, eventbus.KindGraduation, *tokenAddr, payload)
	}
	return nil
}

// tokenRow is the extracted, validated TokenCreated projection.
type tokenRow struct {
	address      string
	bondingCurve string
	creator      string
	symbol       string
	name         string
	chainID      int
}

// tokenCreatedRow pulls the TokenCreated args (keyed by Solidity input name) and
// lower-cases the addresses, mirroring handleTokenCreated. ok is false when a
// required field (token address, creator) is missing, so the caller rejects the
// projection rather than inserting a half-row.
func tokenCreatedRow(ev ParsedEvent) (tokenRow, bool) {
	a := ev.Parsed.RuntimeArgs
	r := tokenRow{
		address:      lowerAddr(a["token"]),
		bondingCurve: lowerAddr(a["bondingCurve"]),
		creator:      lowerAddr(a["creator"]),
		symbol:       argString(a["symbol"]),
		name:         argString(a["name"]),
		chainID:      ev.ChainID,
	}
	if r.address == "" || r.creator == "" {
		return tokenRow{}, false
	}
	return r, true
}

// handleTokenCreated upserts the tokens row. id/created_at/updated_at follow
// the gen_random_uuid()::text + NOW() convention (Prisma client-side defaults).
func (s *DBSink) handleTokenCreated(ctx context.Context, projection *eventProjection, ev ParsedEvent) error {
	r, ok := tokenCreatedRow(ev)
	if !ok {
		return fmt.Errorf("TokenCreated missing token/creator address (tx %s)", ev.TxHash)
	}
	_, err := projection.store.Exec(ctx, `
		INSERT INTO tokens
			(id, address, chain_id, symbol, name, status, bonding_curve, creator_address, created_at, updated_at)
		VALUES
			(gen_random_uuid()::text, $1, $2, $3, $4, 'LAUNCHED', $5, $6, NOW(), NOW())
		ON CONFLICT (address) DO UPDATE
		SET bonding_curve = EXCLUDED.bonding_curve,
		    status        = CASE
		        WHEN tokens.status = 'GRADUATED' THEN tokens.status
		        ELSE EXCLUDED.status
		    END,
		    updated_at    = NOW()
	`,
		r.address, r.chainID, r.symbol, r.name, nullIfEmpty(r.bondingCurve), r.creator,
	)
	if err != nil {
		return fmt.Errorf("upsert tokens %s: %w", r.address, err)
	}

	// The worker must not scan a curve until the token row it resolves through
	// is committed; otherwise a concurrent tick can persist trades as unknown.
	if s.watcher != nil && r.bondingCurve != "" {
		watcher := s.watcher
		projection.deferUntilCommit(func() {
			watcher.WatchCurve(r.bondingCurve)
		})
	}
	return nil
}

// KnownCurves returns the distinct, non-empty bonding-curve addresses already in
// the tokens table. cmd/indexer seeds the worker's watch set with these at boot
// (via SeedWatchset) so a restart resumes scanning every curve from earlier runs
// instead of waiting for a fresh TokenCreated. Satisfies CurveSource.
func (s *DBSink) KnownCurves(ctx context.Context) ([]string, error) {
	if s == nil || s.pool == nil {
		return nil, ErrSinkPoolNil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT bonding_curve FROM tokens
		WHERE bonding_curve IS NOT NULL AND bonding_curve <> ''
	`)
	if err != nil {
		return nil, fmt.Errorf("indexer: list known curves: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, fmt.Errorf("indexer: scan curve: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// tradeRow is the extracted, validated Buy/Sell projection.
type tradeRow struct {
	user        string   // lower-cased trader (buyer/seller)
	tokenAmount *big.Int // tokensOut (BUY) / tokensIn (SELL)
	ethAmount   *big.Int // ethIn (BUY) / ethOut (SELL)
	newPrice    *big.Int // post-trade price (wei)
	curve       string   // bonding-curve address that emitted the event
	txHash      string
	blockNumber int64
}

// tradeRowFrom extracts a Buy/Sell trade from the runtime args, mirroring the
// arg mapping in handleTrade() (indexer.service.ts). uint256 args arrive as
// *big.Int and addresses as lower-case strings (set by the parser). ok is false
// when a required field (trader, token amount, price) is missing so the caller
// rejects the projection rather than recording a half-row. A missing eth amount
// defaults to 0 (it's informational, not a balance input).
func tradeRowFrom(ev ParsedEvent, side string) (tradeRow, bool) {
	a := ev.Parsed.RuntimeArgs
	var actor, tokenAmt, ethAmt any
	if side == "BUY" {
		actor, tokenAmt, ethAmt = a["buyer"], a["tokensOut"], a["ethIn"]
	} else {
		actor, tokenAmt, ethAmt = a["seller"], a["tokensIn"], a["ethOut"]
	}
	r := tradeRow{
		user:        lowerAddr(actor),
		tokenAmount: argBigInt(tokenAmt),
		ethAmount:   argBigInt(ethAmt),
		newPrice:    argBigInt(a["newPrice"]),
		curve:       strings.ToLower(ev.ContractAddress),
		txHash:      ev.TxHash,
		blockNumber: ev.BlockNumber,
	}
	if r.user == "" || r.tokenAmount == nil || r.newPrice == nil {
		return tradeRow{}, false
	}
	if r.ethAmount == nil {
		r.ethAmount = big.NewInt(0)
	}
	return r, true
}

// handleTrade records a Buy/Sell against the token whose bonding curve emitted
// it. Mirrors handleTrade(): resolve the token by bonding_curve, insert the
// trade idempotently by (chain_id, transaction_hash, log_index), then rebuild
// the two aggregates from the trade ledger. Rebuilding instead of applying a
// delta makes a legacy trade-row-only write repairable without double-crediting.
func (s *DBSink) handleTrade(ctx context.Context, projection *eventProjection, ev ParsedEvent, side string) error {
	r, ok := tradeRowFrom(ev, side)
	if !ok {
		return fmt.Errorf("%s missing trader/amount/price (tx %s)", side, ev.TxHash)
	}

	var (
		tokenID   string
		tokenAddr *string
	)
	err := projection.store.QueryRow(ctx, `SELECT id, address FROM tokens WHERE bonding_curve = $1`, r.curve).Scan(&tokenID, &tokenAddr)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("trade token dependency missing for curve %s", r.curve)
	}
	if err != nil {
		return fmt.Errorf("lookup token for curve %s: %w", r.curve, err)
	}

	// Canonical unit contract (2026-07-10): the raw wei price is normalized to
	// an exact NATIVE/token decimal string ONCE, here at the indexer boundary.
	// No float64 ever holds the value — token_trades.price and
	// tokens.market_cap are NUMERIC(38,18) columns.
	price := num.WeiToDecimal(r.newPrice)
	// Bonding-curve tokens have a fixed 1e9 supply, so market cap =
	// price × 1e9 — computed with integer math (wei × 1e9 → normalize).
	marketCap := num.MulWeiInt(r.newPrice, 1_000_000_000)

	// The former ledger kept one row per transaction without a log coordinate.
	// Chronological repair claims that row before inserting any additional logs
	// from the same transaction, preserving its identity without losing events.
	legacyTag, err := projection.store.Exec(ctx, `
		UPDATE token_trades
		SET chain_id = $1, "tokenId" = $2, user_address = $3, type = $4,
		    "tokenAmount" = $5, "ethAmount" = $6, price = $7,
		    block_number = $8, log_index = $9
		WHERE transaction_hash = $10 AND log_index IS NULL
	`, ev.ChainID, tokenID, r.user, side, r.tokenAmount.String(), r.ethAmount.String(),
		price, r.blockNumber, ev.LogIndex, r.txHash)
	if err != nil {
		return fmt.Errorf("claim legacy token_trade %s: %w", r.txHash, err)
	}
	if legacyTag.RowsAffected() > 1 {
		return fmt.Errorf("ambiguous legacy token_trades for transaction %s", r.txHash)
	}

	isNew := false
	if legacyTag.RowsAffected() == 0 {
		tag, err := projection.store.Exec(ctx, `
		INSERT INTO token_trades
			(id, chain_id, "tokenId", user_address, type, "tokenAmount", "ethAmount", price,
			 block_number, log_index, transaction_hash, "timestamp")
		VALUES
			(gen_random_uuid()::text, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NOW())
		ON CONFLICT (chain_id, transaction_hash, log_index) DO NOTHING
		`, ev.ChainID, tokenID, r.user, side, r.tokenAmount.String(), r.ethAmount.String(), price,
			r.blockNumber, ev.LogIndex, r.txHash)
		if err != nil {
			return fmt.Errorf("insert token_trade %s#%d: %w", r.txHash, ev.LogIndex, err)
		}
		isNew = tag.RowsAffected() > 0
		if !isNew {
			if _, err := projection.store.Exec(ctx, `
			UPDATE token_trades
			SET "tokenId" = $4, user_address = $5, type = $6,
			    "tokenAmount" = $7, "ethAmount" = $8, price = $9,
			    block_number = $10
			WHERE chain_id = $1 AND transaction_hash = $2 AND log_index = $3
			`, ev.ChainID, r.txHash, ev.LogIndex, tokenID, r.user, side,
				r.tokenAmount.String(), r.ethAmount.String(), price, r.blockNumber); err != nil {
				return fmt.Errorf("repair token_trade %s#%d: %w", r.txHash, ev.LogIndex, err)
			}
		}
	}

	// An older event can be repaired after newer events, so market cap comes from
	// the latest durable coordinate rather than whichever event is replayed now.
	if _, err := projection.store.Exec(ctx, `
		UPDATE tokens AS token
		SET market_cap = latest.price * 1000000000::numeric, updated_at = NOW()
		FROM (
			SELECT price FROM token_trades
			WHERE chain_id = $1 AND "tokenId" = $2
			ORDER BY block_number DESC, log_index DESC NULLS LAST, transaction_hash DESC
			LIMIT 1
		) AS latest
		WHERE token.id = $2
	`, ev.ChainID, tokenID); err != nil {
		return fmt.Errorf("update market_cap %s: %w", tokenID, err)
	}

	if err := s.rebuildHolderBalance(ctx, projection.store, ev.ChainID, tokenID, r.user); err != nil {
		return fmt.Errorf("update holder balance %s/%s: %w", tokenID, r.user, err)
	}

	// Realtime fan-out is queued only for a genuinely new trade and runs after
	// commit, so subscribers never observe a projection that later rolls back.
	if isNew && tokenAddr != nil && *tokenAddr != "" {
		now := time.Now().UTC().Format(time.RFC3339)
		projection.publishAfterCommit(ctx, s, eventbus.KindTrade, *tokenAddr, eventbus.TradePayload{
			TokenAddress: strings.ToLower(*tokenAddr),
			TokenID:      tokenID,
			Type:         side,
			UserAddress:  r.user,
			TokenAmount:  r.tokenAmount.String(),
			EthAmount:    r.ethAmount.String(),
			Price:        price,
			TxHash:       r.txHash,
			BlockNumber:  r.blockNumber,
			Timestamp:    now,
		})
		projection.publishAfterCommit(ctx, s, eventbus.KindPriceUpdate, *tokenAddr, eventbus.PricePayload{
			TokenAddress: strings.ToLower(*tokenAddr),
			Price:        price,
			MarketCap:    marketCap,
			Timestamp:    now,
		})
	}
	return nil
}

// rebuildHolderBalance derives the aggregate from its append-only evidence so a
// replay repairs both a missing row and a legacy row that may already include
// the event. No caller has to guess whether a prior delta reached the aggregate.
func (s *DBSink) rebuildHolderBalance(ctx context.Context, store projectionStore, chainID int, tokenID, user string) error {
	_, err := store.Exec(ctx, `
		INSERT INTO token_holders (id, "tokenId", user_address, balance, "updatedAt")
		SELECT gen_random_uuid()::text, $1, $2,
		       GREATEST(0::numeric, COALESCE(SUM(
		           CASE type
		               WHEN 'BUY' THEN "tokenAmount"::numeric
		               WHEN 'SELL' THEN -"tokenAmount"::numeric
		               ELSE 0::numeric
		           END
		       ), 0::numeric))::text,
		       NOW()
		FROM token_trades
		WHERE chain_id = $3 AND "tokenId" = $1 AND user_address = $2
		ON CONFLICT ("tokenId", user_address) DO UPDATE
		SET balance = EXCLUDED.balance,
		    "updatedAt" = NOW()
	`, tokenID, user, chainID)
	return err
}

// argBigInt type-asserts a runtime arg to *big.Int (nil if absent or not a
// *big.Int — uint256 args are stored as *big.Int by the parser).
func argBigInt(v any) *big.Int {
	b, _ := v.(*big.Int)
	return b
}

// lowerAddr type-asserts an arg to a string and lower-cases it ("" if absent or
// not a string). RuntimeArgs already stores addresses lower-case; this matches
// the NestJS `.toLowerCase()` defensively.
func lowerAddr(v any) string {
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return strings.ToLower(s)
}

// argString type-asserts an arg to a string ("" if absent or not a string).
func argString(v any) string {
	s, _ := v.(string)
	return s
}

// nullIfEmpty maps "" to a SQL NULL for nullable columns.
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// encodeArgs marshals the persisted args to a JSON string for the jsonb
// column, defaulting to "{}" (the column's own default) when there are none.
func encodeArgs(m map[string]any) (string, error) {
	if len(m) == 0 {
		return "{}", nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// actorOrNil lower-cases the actor address, returning nil for the empty string
// so the nullable actor_address column stores NULL — parity with the NestJS
// `parsed.actorAddress?.toLowerCase() ?? null`.
func actorOrNil(addr string) any {
	if addr == "" {
		return nil
	}
	return strings.ToLower(addr)
}
