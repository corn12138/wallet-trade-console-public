package indexer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Perp projection: PerpMarket's IncreasePosition / DecreasePosition /
// LiquidatePosition events become perp_trades rows (the durable trade log
// that candles, trade history and volume aggregate over) and drive the
// perp_positions read model that /trade renders.
//
// Idempotency: the trade INSERT is keyed by (chain_id, txHash, log_index) —
// one chain event, one row. The position mutation runs ONLY when that insert
// was genuinely new, so a backfill replay / reorg re-scan / retry can never
// double-apply a size delta (same pattern as the token_trades holder update).
//
// perp tables store the market SYMBOL (e.g. ETH-USD), not the index-token
// address the event carries — the wired SymbolResolver (markets catalog ×
// deployments registry) translates; events on unknown index tokens are
// logged and skipped while the raw web3_events row is still recorded.

// perpEventRow is the extracted, validated PerpMarket event.
type perpEventRow struct {
	kind            string // INCREASE | DECREASE | LIQUIDATION
	account         string
	indexToken      string
	isLong          bool
	sizeDelta       *big.Int // `size` for LIQUIDATION
	collateralDelta *big.Int // `collateral` for LIQUIDATION; may be zero
	price           *big.Int // 30-dec USD; nil for LIQUIDATION (mark keeps last)
	fee             *big.Int // zero when absent
	pnl             *big.Int // signed; nil for INCREASE
}

// perpEventRowFrom maps the runtime args for one perp event kind. ok=false
// when a required field is missing so the caller records-and-skips.
func perpEventRowFrom(ev ParsedEvent, kind string) (perpEventRow, bool) {
	a := ev.Parsed.RuntimeArgs
	r := perpEventRow{
		kind:       kind,
		account:    lowerAddr(a["account"]),
		indexToken: lowerAddr(a["indexToken"]),
		fee:        argBigInt(a["fee"]),
		pnl:        argBigInt(a["pnl"]),
	}
	if b, ok := a["isLong"].(bool); ok {
		r.isLong = b
	} else {
		return perpEventRow{}, false
	}
	switch kind {
	case "LIQUIDATION":
		r.sizeDelta = argBigInt(a["size"])
		r.collateralDelta = argBigInt(a["collateral"])
	default:
		r.sizeDelta = argBigInt(a["sizeDelta"])
		r.collateralDelta = argBigInt(a["collateralDelta"])
		r.price = argBigInt(a["price"])
	}
	if r.account == "" || r.indexToken == "" || r.sizeDelta == nil {
		return perpEventRow{}, false
	}
	if kind != "LIQUIDATION" && r.price == nil {
		return perpEventRow{}, false
	}
	if r.collateralDelta == nil {
		r.collateralDelta = big.NewInt(0)
	}
	if r.fee == nil {
		r.fee = big.NewInt(0)
	}
	return r, true
}

// positionState is the perp_positions row projection the mutation runs on.
type positionState struct {
	size       *big.Int
	collateral *big.Int
	entryPrice *big.Int
	markPrice  *big.Int
	pnl        *big.Int
	status     string // OPEN | CLOSED | LIQUIDATED
}

// applyPerpMutation computes the next position state from the current one
// (nil = no open position) and one event. Pure function — fully unit-tested
// without a database. Returns ok=false when the event cannot apply (e.g. a
// decrease with no open position).
func applyPerpMutation(current *positionState, r perpEventRow) (positionState, bool) {
	zero := big.NewInt(0)
	switch r.kind {
	case "INCREASE":
		if current == nil {
			return positionState{
				size:       new(big.Int).Set(r.sizeDelta),
				collateral: new(big.Int).Set(r.collateralDelta),
				entryPrice: new(big.Int).Set(r.price),
				markPrice:  new(big.Int).Set(r.price),
				pnl:        zero,
				status:     "OPEN",
			}, true
		}
		newSize := new(big.Int).Add(current.size, r.sizeDelta)
		// Size-weighted average entry: (size·entry + delta·price) / newSize.
		entry := new(big.Int).Set(current.entryPrice)
		if newSize.Sign() > 0 {
			num := new(big.Int).Mul(current.size, current.entryPrice)
			num.Add(num, new(big.Int).Mul(r.sizeDelta, r.price))
			entry = num.Quo(num, newSize)
		}
		return positionState{
			size:       newSize,
			collateral: new(big.Int).Add(current.collateral, r.collateralDelta),
			entryPrice: entry,
			markPrice:  new(big.Int).Set(r.price),
			pnl:        new(big.Int).Set(current.pnl),
			status:     "OPEN",
		}, true

	case "DECREASE":
		if current == nil {
			return positionState{}, false
		}
		newSize := new(big.Int).Sub(current.size, r.sizeDelta)
		if newSize.Sign() < 0 {
			newSize = big.NewInt(0)
		}
		newCollateral := new(big.Int).Sub(current.collateral, r.collateralDelta)
		if newCollateral.Sign() < 0 {
			newCollateral = big.NewInt(0)
		}
		pnl := new(big.Int).Set(current.pnl)
		if r.pnl != nil {
			pnl.Add(pnl, r.pnl)
		}
		status := "OPEN"
		if newSize.Sign() == 0 {
			status = "CLOSED"
		}
		return positionState{
			size:       newSize,
			collateral: newCollateral,
			entryPrice: new(big.Int).Set(current.entryPrice),
			markPrice:  new(big.Int).Set(r.price),
			pnl:        pnl,
			status:     status,
		}, true

	case "LIQUIDATION":
		if current == nil {
			return positionState{}, false
		}
		pnl := new(big.Int).Set(current.pnl)
		if r.pnl != nil {
			pnl.Add(pnl, r.pnl)
		}
		return positionState{
			size:       big.NewInt(0),
			collateral: big.NewInt(0),
			entryPrice: new(big.Int).Set(current.entryPrice),
			markPrice:  new(big.Int).Set(current.markPrice), // no price arg on liquidation
			pnl:        pnl,
			status:     "LIQUIDATED",
		}, true
	}
	return positionState{}, false
}

// handlePerpEvent projects one PerpMarket event. Order per event:
//  1. resolve the market symbol from the index token;
//  2. idempotent perp_trades insert (the durable evidence);
//  3. only if the insert was NEW, mutate perp_positions.
func (s *DBSink) handlePerpEvent(ctx context.Context, ev ParsedEvent, kind string) error {
	if s.symbols == nil {
		slog.WarnContext(ctx, "indexer: perp event without symbol resolver; raw event recorded only",
			"event", ev.Parsed.EventName, "tx", ev.TxHash)
		return nil
	}
	r, ok := perpEventRowFrom(ev, kind)
	if !ok {
		return fmt.Errorf("%s missing required args (tx %s)", kind, ev.TxHash)
	}
	symbol := s.symbols.SymbolForIndexToken(ev.ChainID, r.indexToken)
	if symbol == "" {
		slog.WarnContext(ctx, "indexer: perp event on unknown index token",
			"indexToken", r.indexToken, "chain", ev.ChainID, "tx", ev.TxHash)
		return nil
	}

	// Trade-row price: liquidations carry no price arg — record the last
	// mark from the open position (fetched below) or 0.
	tradePrice := r.price

	current, positionID, err := s.loadOpenPosition(ctx, ev.ChainID, r.account, symbol, r.isLong)
	if err != nil {
		return err
	}
	if tradePrice == nil {
		if current != nil {
			tradePrice = current.markPrice
		} else {
			tradePrice = big.NewInt(0)
		}
	}

	var pnlArg any
	if r.pnl != nil {
		pnlArg = r.pnl.String()
	}
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO perp_trades
			(id, chain_id, account, token, "isLong", "sizeDelta", price, fee, pnl,
			 type, "txHash", log_index, "blockNumber", "createdAt")
		VALUES
			(gen_random_uuid()::text, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, NOW())
		ON CONFLICT (chain_id, "txHash", log_index) DO NOTHING
	`,
		ev.ChainID, r.account, symbol, r.isLong, r.sizeDelta.String(),
		tradePrice.String(), r.fee.String(), pnlArg, kind,
		ev.TxHash, int(ev.LogIndex), int64(ev.BlockNumber),
	)
	if err != nil {
		return fmt.Errorf("insert perp_trade %s#%d: %w", ev.TxHash, ev.LogIndex, err)
	}
	if tag.RowsAffected() == 0 {
		return nil // replay — position already reflects this event
	}

	next, applies := applyPerpMutation(current, r)
	if !applies {
		slog.WarnContext(ctx, "indexer: perp event with no open position; trade recorded, position skipped",
			"kind", kind, "account", r.account, "symbol", symbol, "tx", ev.TxHash)
		return nil
	}
	if current == nil {
		_, err = s.pool.Exec(ctx, `
			INSERT INTO perp_positions
				(id, chain_id, account, token, "isLong", size, collateral,
				 "entryPrice", "markPrice", pnl, status, "txHash", "createdAt", "updatedAt")
			VALUES
				(gen_random_uuid()::text, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, NOW(), NOW())
		`,
			ev.ChainID, r.account, symbol, r.isLong,
			next.size.String(), next.collateral.String(),
			next.entryPrice.String(), next.markPrice.String(), next.pnl.String(),
			next.status, ev.TxHash,
		)
		if err != nil {
			return fmt.Errorf("insert perp_position (%s %s): %w", r.account, symbol, err)
		}
		return nil
	}
	_, err = s.pool.Exec(ctx, `
		UPDATE perp_positions
		SET size = $1, collateral = $2, "entryPrice" = $3, "markPrice" = $4,
		    pnl = $5, status = $6, "txHash" = $7, "updatedAt" = NOW()
		WHERE id = $8
	`,
		next.size.String(), next.collateral.String(), next.entryPrice.String(),
		next.markPrice.String(), next.pnl.String(), next.status, ev.TxHash, positionID,
	)
	if err != nil {
		return fmt.Errorf("update perp_position %s: %w", positionID, err)
	}
	return nil
}

// loadOpenPosition fetches the OPEN position for (chain, account, symbol,
// side), or nil when none exists.
func (s *DBSink) loadOpenPosition(ctx context.Context, chainID int, account, symbol string, isLong bool) (*positionState, string, error) {
	var (
		id                                 string
		size, collateral, entry, mark, pnl string
	)
	err := s.pool.QueryRow(ctx, `
		SELECT id, size, collateral, "entryPrice", "markPrice", pnl
		FROM perp_positions
		WHERE chain_id = $1 AND account = $2 AND token = $3 AND "isLong" = $4 AND status = 'OPEN'
		ORDER BY "createdAt" DESC
		LIMIT 1
	`, chainID, account, symbol, isLong).Scan(&id, &size, &collateral, &entry, &mark, &pnl)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("load open perp_position (%s %s): %w", account, symbol, err)
	}
	state := &positionState{
		size:       bigFromDecimal(size),
		collateral: bigFromDecimal(collateral),
		entryPrice: bigFromDecimal(entry),
		markPrice:  bigFromDecimal(mark),
		pnl:        bigFromDecimal(pnl),
		status:     "OPEN",
	}
	return state, id, nil
}

// bigFromDecimal parses a stored decimal string, treating junk/empty as 0 so
// one malformed legacy row cannot wedge the projection.
func bigFromDecimal(s string) *big.Int {
	v, ok := new(big.Int).SetString(strings.TrimSpace(s), 10)
	if !ok {
		return big.NewInt(0)
	}
	return v
}
