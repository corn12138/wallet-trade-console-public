package indexer

import (
	"context"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"
)

// Perp projection: PerpMarket's IncreasePosition / DecreasePosition /
// LiquidatePosition events become perp_trades rows (the durable trade log
// that candles, trade history and volume aggregate over) and drive the
// perp_positions read model that /trade renders.
//
// Idempotency: the trade ledger is keyed by (chain_id, txHash, log_index), and
// positions are rebuilt from that ledger. This repairs a legacy trade-only row
// without guessing whether its size delta already reached perp_positions.
//
// perp tables store the market SYMBOL (e.g. ETH-USD), not the index-token
// address the event carries — the wired SymbolResolver (markets catalog ×
// deployments registry) translates. Missing mappings are retryable projection
// failures because a raw-only row is not a completed read model.

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
// when a required field is missing so the caller rejects the projection.
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

// handlePerpEvent upserts the event ledger and rebuilds the affected position
// lifecycle inside the caller's transaction.
func (s *DBSink) handlePerpEvent(ctx context.Context, projection *eventProjection, ev ParsedEvent, kind string) error {
	if s.symbols == nil {
		return fmt.Errorf("perp symbol resolver unavailable")
	}
	r, ok := perpEventRowFrom(ev, kind)
	if !ok {
		return fmt.Errorf("%s missing required args (tx %s)", kind, ev.TxHash)
	}
	symbol := s.symbols.SymbolForIndexToken(ev.ChainID, r.indexToken)
	if symbol == "" {
		return fmt.Errorf("perp symbol dependency missing for index token %s on chain %d", r.indexToken, ev.ChainID)
	}

	tradePrice := r.price
	if tradePrice == nil {
		tradePrice = big.NewInt(0)
	}

	var pnlArg any
	if r.pnl != nil {
		pnlArg = r.pnl.String()
	}
	_, err := projection.store.Exec(ctx, `
		INSERT INTO perp_trades
			(id, chain_id, account, token, "isLong", "sizeDelta", collateral_delta, price, fee, pnl,
			 type, "txHash", log_index, "blockNumber", "createdAt")
		VALUES
			(gen_random_uuid()::text, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, NOW())
		ON CONFLICT (chain_id, "txHash", log_index) DO UPDATE SET
			account = EXCLUDED.account,
			token = EXCLUDED.token,
			"isLong" = EXCLUDED."isLong",
			"sizeDelta" = EXCLUDED."sizeDelta",
			collateral_delta = EXCLUDED.collateral_delta,
			price = EXCLUDED.price,
			fee = EXCLUDED.fee,
			pnl = EXCLUDED.pnl,
			type = EXCLUDED.type,
			"blockNumber" = EXCLUDED."blockNumber"
	`,
		ev.ChainID, r.account, symbol, r.isLong, r.sizeDelta.String(),
		r.collateralDelta.String(), tradePrice.String(), r.fee.String(), pnlArg, kind,
		ev.TxHash, ev.LogIndex, ev.BlockNumber,
	)
	if err != nil {
		return fmt.Errorf("insert perp_trade %s#%d: %w", ev.TxHash, ev.LogIndex, err)
	}
	return s.rebuildPerpPositions(ctx, projection.store, ev.ChainID, r.account, symbol, r.isLong)
}

type perpLedgerEntry struct {
	id, kind, sizeDelta, collateralDelta, price, fee, txHash string
	pnl                                                      *string
	logIndex                                                 int
	blockNumber                                              int64
	createdAt                                                time.Time
}

type rebuiltPerpPosition struct {
	id, txHash string
	state      positionState
	createdAt  time.Time
	updatedAt  time.Time
}

func (s *DBSink) rebuildPerpPositions(ctx context.Context, store projectionStore,
	chainID int, account, symbol string, isLong bool) error {
	rows, err := store.Query(ctx, `
		SELECT id, type, "sizeDelta", collateral_delta, price, fee, pnl,
		       "txHash", log_index, "blockNumber", "createdAt"
		FROM perp_trades
		WHERE chain_id = $1 AND account = $2 AND token = $3
		  AND "isLong" = $4 AND log_index IS NOT NULL
		ORDER BY "blockNumber", log_index, "txHash"
	`, chainID, account, symbol, isLong)
	if err != nil {
		return fmt.Errorf("load perp repair ledger (%s %s): %w", account, symbol, err)
	}
	entries := []perpLedgerEntry{}
	for rows.Next() {
		var entry perpLedgerEntry
		if err := rows.Scan(&entry.id, &entry.kind, &entry.sizeDelta, &entry.collateralDelta,
			&entry.price, &entry.fee, &entry.pnl, &entry.txHash, &entry.logIndex,
			&entry.blockNumber, &entry.createdAt); err != nil {
			rows.Close()
			return fmt.Errorf("scan perp repair ledger: %w", err)
		}
		entries = append(entries, entry)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate perp repair ledger: %w", err)
	}
	if len(entries) == 0 {
		return fmt.Errorf("perp repair ledger empty for %s/%s", account, symbol)
	}

	positions, liquidationPrices, err := rebuildPerpState(chainID, account, symbol, isLong, entries)
	if err != nil {
		return err
	}
	for id, price := range liquidationPrices {
		if _, err := store.Exec(ctx, `UPDATE perp_trades SET price = $2 WHERE id = $1`, id, price); err != nil {
			return fmt.Errorf("repair liquidation price %s: %w", id, err)
		}
	}
	if _, err := store.Exec(ctx, `
		DELETE FROM perp_positions
		WHERE chain_id = $1 AND account = $2 AND token = $3 AND "isLong" = $4
	`, chainID, account, symbol, isLong); err != nil {
		return fmt.Errorf("clear perp position projection (%s %s): %w", account, symbol, err)
	}
	for _, position := range positions {
		if _, err := store.Exec(ctx, `
			INSERT INTO perp_positions
				(id, chain_id, account, token, "isLong", size, collateral,
				 "entryPrice", "markPrice", pnl, status, "txHash", "createdAt", "updatedAt")
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		`, position.id, chainID, account, symbol, isLong,
			position.state.size.String(), position.state.collateral.String(),
			position.state.entryPrice.String(), position.state.markPrice.String(),
			position.state.pnl.String(), position.state.status, position.txHash,
			position.createdAt, position.updatedAt); err != nil {
			return fmt.Errorf("insert rebuilt perp_position (%s %s): %w", account, symbol, err)
		}
	}
	return nil
}

func rebuildPerpState(chainID int, account, symbol string, isLong bool,
	entries []perpLedgerEntry) ([]rebuiltPerpPosition, map[string]string, error) {
	positions := []rebuiltPerpPosition{}
	liquidationPrices := map[string]string{}
	var current *rebuiltPerpPosition
	for _, entry := range entries {
		size, err := parseLedgerInteger("sizeDelta", entry.sizeDelta)
		if err != nil {
			return nil, nil, err
		}
		collateral, err := parseLedgerInteger("collateralDelta", entry.collateralDelta)
		if err != nil {
			return nil, nil, err
		}
		price, err := parseLedgerInteger("price", entry.price)
		if err != nil {
			return nil, nil, err
		}
		fee, err := parseLedgerInteger("fee", entry.fee)
		if err != nil {
			return nil, nil, err
		}
		var pnl *big.Int
		if entry.pnl != nil {
			pnl, err = parseLedgerInteger("pnl", *entry.pnl)
			if err != nil {
				return nil, nil, err
			}
		}
		if entry.kind == "LIQUIDATION" && current != nil {
			price = new(big.Int).Set(current.state.markPrice)
			liquidationPrices[entry.id] = price.String()
		}
		mutation := perpEventRow{
			kind: entry.kind, account: account, isLong: isLong,
			sizeDelta: size, collateralDelta: collateral, price: price, fee: fee, pnl: pnl,
		}
		var state *positionState
		if current != nil {
			state = &current.state
		}
		next, applies := applyPerpMutation(state, mutation)
		if !applies {
			return nil, nil, fmt.Errorf("perp %s has no open predecessor at block %d log %d",
				entry.kind, entry.blockNumber, entry.logIndex)
		}
		if current == nil {
			current = &rebuiltPerpPosition{
				id: projectionStableID("perp-position", strconv.Itoa(chainID), account, symbol,
					strconv.FormatBool(isLong), entry.txHash, strconv.Itoa(entry.logIndex)),
				createdAt: entry.createdAt,
			}
		}
		current.state = next
		current.txHash = entry.txHash
		current.updatedAt = entry.createdAt
		if next.status != "OPEN" {
			positions = append(positions, *current)
			current = nil
		}
	}
	if current != nil {
		positions = append(positions, *current)
	}
	return positions, liquidationPrices, nil
}

func parseLedgerInteger(field, raw string) (*big.Int, error) {
	value, ok := new(big.Int).SetString(strings.TrimSpace(raw), 10)
	if !ok {
		return nil, fmt.Errorf("invalid perp ledger %s %q", field, raw)
	}
	return value, nil
}
