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

// DBSink persists parsed events into web3_events. It is the Phase 6a.6
// replacement for CountingSink: HandleEvent mirrors the web3Event.upsert in
// persistEvent() (indexer.service.ts) — an INSERT … ON CONFLICT keyed by the
// (chainId, contractAddress, txHash, logIndex) unique constraint, so replays
// (a backfill re-scan, a reorg rewind, a retry) update the row in place
// instead of duplicating it.
//
// After the raw record, HandleEvent runs the business projection
// (processEvent): TokenCreated → tokens upsert, and Buy/Sell → token_trades +
// token_holders. Buy/Sell come from per-token bonding-curve contracts created at
// runtime, so on each TokenCreated the sink calls its CurveWatcher (the worker)
// to add the new curve to the live watch set — parity with NestJS startWatching.
type DBSink struct {
	pool    *pgxpool.Pool
	watcher CurveWatcher
	events  TokenEventPublisher
	symbols SymbolResolver
}

// TokenEventPublisher receives realtime notifications AFTER a projection
// committed. eventbus.PgPublisher satisfies it; nil disables publishing.
type TokenEventPublisher interface {
	PublishTokenEvent(ctx context.Context, ev eventbus.TokenEvent) error
}

// SymbolResolver maps a perp market's index-token address to its market
// symbol (perp_trades/perp_positions store SYMBOLS, chain events carry
// addresses). cmd/indexer wires one from the markets catalog + deployments
// registry; nil makes perp events log-and-skip.
type SymbolResolver interface {
	SymbolForIndexToken(chainID int, indexToken string) string
}

// SetEventPublisher wires the post-commit realtime publisher (nil = off).
func (s *DBSink) SetEventPublisher(p TokenEventPublisher) { s.events = p }

// SetSymbolResolver wires the perp index-token→symbol lookup (nil = perp
// events are recorded raw but not projected).
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
func NewDBSink(pool *pgxpool.Pool) *DBSink { return &DBSink{pool: pool} }

// SetCurveWatcher wires the worker that should start scanning newly created
// bonding curves. It's a post-construction setter because the sink and worker
// reference each other (the worker's runner holds the sink; the sink holds the
// worker as a CurveWatcher). nil is fine — the sink simply won't request new
// watches, and Buy/Sell only arrive for statically-watched curves.
func (s *DBSink) SetCurveWatcher(w CurveWatcher) { s.watcher = w }

// HandleEvent upserts one parsed event. The block/tx coordinates come from the
// fetched log; eventName/actorAddress/args from the parser. occurred_at is left
// NULL — the block timestamp isn't fetched yet (NestJS only sets it when a
// chain context with a timestamp is supplied), so on conflict we deliberately
// don't touch it.
func (s *DBSink) HandleEvent(ctx context.Context, ev ParsedEvent) error {
	if s == nil || s.pool == nil {
		return ErrSinkPoolNil
	}
	args, err := encodeArgs(ev.Parsed.PersistedArgs)
	if err != nil {
		return fmt.Errorf("indexer: encode args for %s#%d: %w", ev.TxHash, ev.LogIndex, err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO web3_events
			(chain_id, contract_address, event_name, tx_hash, log_index,
			 block_number, actor_address, args, occurred_at, created_at)
		VALUES
			($1, $2, $3, $4, $5, $6, $7, $8::jsonb, NULL, NOW())
		ON CONFLICT (chain_id, contract_address, tx_hash, log_index) DO UPDATE
		SET event_name    = EXCLUDED.event_name,
		    actor_address = EXCLUDED.actor_address,
		    args          = EXCLUDED.args
	`,
		ev.ChainID,
		ev.ContractAddress, // already lower-cased by toParsedEvent
		ev.Parsed.EventName,
		ev.TxHash,
		int(ev.LogIndex),
		int64(ev.BlockNumber),
		actorOrNil(ev.Parsed.ActorAddress),
		args,
	)
	if err != nil {
		return fmt.Errorf("indexer: upsert web3_events %s#%d: %w", ev.TxHash, ev.LogIndex, err)
	}

	// Business projection (token/trade/holder tables). Mirrors NestJS
	// processEvent(): wrapped so a handler failure is logged but does NOT fail
	// the event — the raw record is already saved and the checkpoint must still
	// advance (parity with the try/catch around processEvent).
	if err := s.processEvent(ctx, ev); err != nil {
		slog.WarnContext(ctx, "indexer: business handler failed",
			"event", ev.Parsed.EventName, "tx", ev.TxHash, "err", err)
	}
	return nil
}

// processEvent dispatches an event to its business handler. Mirrors the switch
// in indexer.service.ts processEvent().
//
// TokenCreated is emitted by the watched TokenFactory, so it always reaches the
// sink. Buy/Sell come from per-token bonding-curve contracts; their handlers are
// wired here but only fire once the worker is watching that curve (runtime
// curve-watching is the remaining follow-on — see the DBSink doc).
func (s *DBSink) processEvent(ctx context.Context, ev ParsedEvent) error {
	switch ev.Parsed.EventName {
	case "TokenCreated":
		return s.handleTokenCreated(ctx, ev)
	case "Buy":
		return s.handleTrade(ctx, ev, "BUY")
	case "Sell":
		return s.handleTrade(ctx, ev, "SELL")
	case "Graduated":
		return s.handleGraduated(ctx, ev)
	case "IncreasePosition":
		return s.handlePerpEvent(ctx, ev, "INCREASE")
	case "DecreasePosition":
		return s.handlePerpEvent(ctx, ev, "DECREASE")
	case "LiquidatePosition":
		return s.handlePerpEvent(ctx, ev, "LIQUIDATION")
	case "Staked":
		return s.handleStakingEvent(ctx, ev, "STAKE")
	case "Unstaked":
		return s.handleStakingEvent(ctx, ev, "UNSTAKE")
	case "RewardClaimed":
		return s.handleStakingEvent(ctx, ev, "CLAIM")
	case "EmergencyWithdraw":
		return s.handleStakingEvent(ctx, ev, "EMERGENCY")
	default:
		return nil
	}
}

// handleGraduated marks the token whose bonding curve emitted Graduated as
// GRADUATED and publishes the realtime graduation event. Idempotent by
// construction (status set is absorbing).
func (s *DBSink) handleGraduated(ctx context.Context, ev ParsedEvent) error {
	curve := strings.ToLower(ev.ContractAddress)
	a := ev.Parsed.RuntimeArgs

	var tokenAddr *string
	err := s.pool.QueryRow(ctx, `
		UPDATE tokens SET status = 'GRADUATED', updated_at = NOW()
		WHERE bonding_curve = $1
		RETURNING address
	`, curve).Scan(&tokenAddr)
	if errors.Is(err, pgx.ErrNoRows) {
		slog.WarnContext(ctx, "indexer: Graduated on unknown bonding curve", "curve", curve, "tx", ev.TxHash)
		return nil
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
		s.publish(ctx, eventbus.KindGraduation, *tokenAddr, payload)
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
// required field (token address, creator) is missing, so the caller skips the
// write rather than inserting a half-row.
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

// handleTokenCreated upserts the tokens row. Mirrors token.upsert in
// handleTokenCreated(): create with status LAUNCHED, or on an existing address
// update bonding_curve + status. id/created_at/updated_at follow the
// gen_random_uuid()::text + NOW() convention (Prisma client-side defaults).
func (s *DBSink) handleTokenCreated(ctx context.Context, ev ParsedEvent) error {
	r, ok := tokenCreatedRow(ev)
	if !ok {
		return fmt.Errorf("TokenCreated missing token/creator address (tx %s)", ev.TxHash)
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO tokens
			(id, address, chain_id, symbol, name, status, bonding_curve, creator_address, created_at, updated_at)
		VALUES
			(gen_random_uuid()::text, $1, $2, $3, $4, 'LAUNCHED', $5, $6, NOW(), NOW())
		ON CONFLICT (address) DO UPDATE
		SET bonding_curve = EXCLUDED.bonding_curve,
		    status        = 'LAUNCHED',
		    updated_at    = NOW()
	`,
		r.address, r.chainID, r.symbol, r.name, nullIfEmpty(r.bondingCurve), r.creator,
	)
	if err != nil {
		return fmt.Errorf("upsert tokens %s: %w", r.address, err)
	}

	// Start watching the new bonding curve so its Buy/Sell events are scanned
	// from the next tick (parity with NestJS startWatching). No-op when no
	// watcher is wired or the curve address is empty.
	if s.watcher != nil && r.bondingCurve != "" {
		s.watcher.WatchCurve(r.bondingCurve)
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
// skips the write rather than recording a half-row. A missing eth amount
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
		blockNumber: int64(ev.BlockNumber),
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
// trade idempotently (unique transaction_hash), and ONLY on a genuinely new row
// apply the two non-idempotent follow-ups — set market_cap and move the holder
// balance. A trade on a curve with no token row is logged and skipped (parity
// with the NestJS "unknown bonding curve" warn).
func (s *DBSink) handleTrade(ctx context.Context, ev ParsedEvent, side string) error {
	r, ok := tradeRowFrom(ev, side)
	if !ok {
		return fmt.Errorf("%s missing trader/amount/price (tx %s)", side, ev.TxHash)
	}

	var (
		tokenID   string
		tokenAddr *string
	)
	err := s.pool.QueryRow(ctx, `SELECT id, address FROM tokens WHERE bonding_curve = $1`, r.curve).Scan(&tokenID, &tokenAddr)
	if errors.Is(err, pgx.ErrNoRows) {
		slog.WarnContext(ctx, "indexer: trade on unknown bonding curve", "curve", r.curve, "tx", r.txHash)
		return nil
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

	// Idempotent insert keyed by the unique transaction_hash. ON CONFLICT DO
	// NOTHING + RowsAffected==0 means a replay (backfill re-scan / reorg / retry)
	// already recorded this trade, so we must NOT re-apply the holder delta.
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO token_trades
			(id, "tokenId", user_address, type, "tokenAmount", "ethAmount", price,
			 block_number, transaction_hash, "timestamp")
		VALUES
			(gen_random_uuid()::text, $1, $2, $3, $4, $5, $6, $7, $8, NOW())
		ON CONFLICT (transaction_hash) DO NOTHING
	`,
		tokenID, r.user, side, r.tokenAmount.String(), r.ethAmount.String(), price,
		r.blockNumber, r.txHash,
	)
	if err != nil {
		return fmt.Errorf("insert token_trade %s: %w", r.txHash, err)
	}
	if tag.RowsAffected() == 0 {
		return nil // already recorded
	}

	// market_cap = normalized price × fixed 1e9 supply (exact decimal).
	if _, err := s.pool.Exec(ctx,
		`UPDATE tokens SET market_cap = $1::numeric, updated_at = NOW() WHERE id = $2`,
		marketCap, tokenID,
	); err != nil {
		return fmt.Errorf("update market_cap %s: %w", tokenID, err)
	}

	// Holder balance: BUY adds the token amount, SELL subtracts it.
	delta := new(big.Int).Set(r.tokenAmount)
	if side == "SELL" {
		delta.Neg(delta)
	}
	if err := s.updateHolderBalance(ctx, tokenID, r.user, delta); err != nil {
		return fmt.Errorf("update holder balance %s/%s: %w", tokenID, r.user, err)
	}

	// Realtime fan-out — only for a genuinely NEW trade (replays returned
	// above), and only after the durable writes: this is the production
	// producer for the /token-events namespace.
	if tokenAddr != nil && *tokenAddr != "" {
		now := time.Now().UTC().Format(time.RFC3339)
		s.publish(ctx, eventbus.KindTrade, *tokenAddr, eventbus.TradePayload{
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
		s.publish(ctx, eventbus.KindPriceUpdate, *tokenAddr, eventbus.PricePayload{
			TokenAddress: strings.ToLower(*tokenAddr),
			Price:        price,
			MarketCap:    marketCap,
			Timestamp:    now,
		})
	}
	return nil
}

// updateHolderBalance applies a signed token-amount delta to the holder row in a
// single locked statement (parity with the NestJS GREATEST upsert). The balance
// is stored as a decimal string and clamped at zero on both the insert and the
// update sides, so a SELL against an unseen holder lands at 0 rather than going
// negative. ON CONFLICT keys the unique (tokenId, user_address) so concurrent
// trades against one holder can't lose a delta.
func (s *DBSink) updateHolderBalance(ctx context.Context, tokenID, user string, delta *big.Int) error {
	d := delta.String()
	_, err := s.pool.Exec(ctx, `
		INSERT INTO token_holders (id, "tokenId", user_address, balance, "updatedAt")
		VALUES (gen_random_uuid()::text, $1, $2, GREATEST(0::numeric, $3::numeric)::text, NOW())
		ON CONFLICT ("tokenId", user_address) DO UPDATE
		SET balance = GREATEST(0::numeric, token_holders.balance::numeric + $3::numeric)::text,
		    "updatedAt" = NOW()
	`, tokenID, user, d)
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
