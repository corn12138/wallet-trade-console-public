package markets

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/bigsum"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/trading"
	"golang.org/x/sync/errgroup"
)

// Ticker mirrors TradingMarketTickerView. bestBid/bestAsk/spread are
// nullable in the JSON; we use *string for accurate null preservation.
type Ticker struct {
	Symbol            string  `json:"symbol"`
	ChainID           int     `json:"chainId"`
	Price             string  `json:"price"`
	BestBid           *string `json:"bestBid"`
	BestAsk           *string `json:"bestAsk"`
	Spread            *string `json:"spread"`
	Volume24h         string  `json:"volume24h"`
	FundingRate       string  `json:"fundingRate"`
	LongOpenInterest  string  `json:"longOpenInterest"`
	ShortOpenInterest string  `json:"shortOpenInterest"`
	UpdatedAt         string  `json:"updatedAt"`
}

// OrderbookLevel is one [price, sizeDelta] pair as a JSON tuple.
type OrderbookLevel [2]string

// Orderbook mirrors TradingOrderbookView.
type Orderbook struct {
	Bids []OrderbookLevel `json:"bids"`
	Asks []OrderbookLevel `json:"asks"`
}

// TradeHistoryEntry mirrors TradingTradeHistoryView. PNL is nullable.
type TradeHistoryEntry struct {
	ID        string  `json:"id"`
	Token     string  `json:"token"`
	IsLong    bool    `json:"isLong"`
	TradeType string  `json:"tradeType"`
	SizeDelta string  `json:"sizeDelta"`
	Price     string  `json:"price"`
	Fee       string  `json:"fee"`
	PNL       *string `json:"pnl"`
	TxHash    string  `json:"txHash"`
	CreatedAt string  `json:"createdAt"`
}

// RealtimeSnapshot is the GET /markets/snapshot/{symbol} payload.
// chainId is optional — omitted when neither the resolved ticker nor
// the request query supplies one (matches NestJS JSON.stringify drop).
type RealtimeSnapshot struct {
	Symbol    string              `json:"symbol"`
	ChainID   *int                `json:"chainId,omitempty"`
	Ticker    *Ticker             `json:"ticker"`
	Orderbook Orderbook           `json:"orderbook"`
	Trades    []TradeHistoryEntry `json:"trades"`
	UpdatedAt string              `json:"updatedAt"`
}

// GetRealtimeSnapshot serves the REST fallback of the realtime market
// stream. Mirrors MarketStreamSnapshotService.getRealtimeSnapshot.
//
// Symbol must already be normalized (trim + uppercase) by the caller —
// the handler runs the same validation NestJS does before dispatching.
func (s *Service) GetRealtimeSnapshot(ctx context.Context, symbol string, chainIDFilter *int) (RealtimeSnapshot, error) {
	deployed, err := s.deployedMarkets(chainIDFilter)
	if err != nil {
		return RealtimeSnapshot{}, err
	}

	var market *deployedMarket
	for i := range deployed {
		if deployed[i].def.Symbol == symbol {
			market = &deployed[i]
			break
		}
	}

	// Symbol unknown for the (chain, symbol) pair → return the same
	// degraded shape NestJS emits (ticker:null, empty orderbook, empty
	// trades). The chainId field falls back to the request value.
	updatedAt := s.clock().UTC().Format(time.RFC3339Nano)
	if market == nil {
		return RealtimeSnapshot{
			Symbol:    symbol,
			ChainID:   chainIDFilter,
			Ticker:    nil,
			Orderbook: Orderbook{Bids: []OrderbookLevel{}, Asks: []OrderbookLevel{}},
			Trades:    []TradeHistoryEntry{},
			UpdatedAt: updatedAt,
		}, nil
	}

	marketChainID := market.def.ChainID
	chainPtr := &marketChainID

	var (
		positions []trading.PerpPosition
		trades24h []trading.PerpTrade
		orders    []trading.PerpOrder
		recent    []trading.PerpTradeFull
	)

	if s.trading != nil {
		g, gctx := errgroup.WithContext(ctx)
		since := s.clock().Add(-24 * time.Hour)
		g.Go(func() error {
			res, err := s.trading.ListOpenPositions(gctx, []string{symbol}, chainPtr)
			if err != nil {
				return err
			}
			positions = res
			return nil
		})
		g.Go(func() error {
			res, err := s.trading.ListRecentTrades(gctx, []string{symbol}, chainPtr, since)
			if err != nil {
				return err
			}
			trades24h = res
			return nil
		})
		g.Go(func() error {
			res, err := s.trading.ListOrderbookOrders(gctx, symbol, chainPtr)
			if err != nil {
				return err
			}
			orders = res
			return nil
		})
		g.Go(func() error {
			res, err := s.trading.ListRecentTradesForSymbol(gctx, symbol, chainPtr, 12)
			if err != nil {
				return err
			}
			recent = res
			return nil
		})
		if err := g.Wait(); err != nil {
			if !errors.Is(err, trading.ErrPoolUnavailable) {
				return RealtimeSnapshot{}, err
			}
		}
	}

	orderbook, err := buildOrderbook(orders)
	if err != nil {
		return RealtimeSnapshot{}, err
	}

	longOI, err := bigsum.Sum(extractLongSizes(positions))
	if err != nil {
		return RealtimeSnapshot{}, err
	}
	shortOI, err := bigsum.Sum(extractShortSizes(positions))
	if err != nil {
		return RealtimeSnapshot{}, err
	}
	vol24, err := bigsum.Sum(extractDeltas24(trades24h))
	if err != nil {
		return RealtimeSnapshot{}, err
	}

	var bestBid, bestAsk *string
	if len(orderbook.Bids) > 0 {
		v := orderbook.Bids[0][0]
		bestBid = &v
	}
	if len(orderbook.Asks) > 0 {
		v := orderbook.Asks[0][0]
		bestAsk = &v
	}

	price := derivePrice(recent, bestBid, bestAsk)

	ticker := &Ticker{
		Symbol:            market.def.Symbol,
		ChainID:           market.def.ChainID,
		Price:             price,
		BestBid:           bestBid,
		BestAsk:           bestAsk,
		Spread:            trading.DeriveSpread(bestBid, bestAsk),
		Volume24h:         vol24,
		FundingRate:       market.def.FundingRate,
		LongOpenInterest:  longOI,
		ShortOpenInterest: shortOI,
		UpdatedAt:         updatedAt,
	}

	return RealtimeSnapshot{
		Symbol:    market.def.Symbol,
		ChainID:   chainPtr,
		Ticker:    ticker,
		Orderbook: orderbook,
		Trades:    mapTrades(recent),
		UpdatedAt: updatedAt,
	}, nil
}

// buildOrderbook splits open orders into bids/asks and sorts via
// bigint comparison. Bids descend (best bid first); asks ascend.
func buildOrderbook(orders []trading.PerpOrder) (Orderbook, error) {
	bids := make([]OrderbookLevel, 0, len(orders))
	asks := make([]OrderbookLevel, 0, len(orders))
	for _, o := range orders {
		// triggerPrice is *string (nullable in Prisma); the orderbook
		// query filters NULL but the type stays pointer for parity
		// with ListPendingOrdersForAccount.
		if o.TriggerPrice == nil || *o.TriggerPrice == "" {
			continue
		}
		level := OrderbookLevel{*o.TriggerPrice, o.SizeDelta}
		if o.IsLong {
			bids = append(bids, level)
		} else {
			asks = append(asks, level)
		}
	}

	var sortErr error
	sort.SliceStable(bids, func(i, j int) bool {
		if sortErr != nil {
			return false
		}
		cmp, err := bigsum.Compare(bids[j][0], bids[i][0]) // descending
		if err != nil {
			sortErr = err
			return false
		}
		return cmp < 0
	})
	sort.SliceStable(asks, func(i, j int) bool {
		if sortErr != nil {
			return false
		}
		cmp, err := bigsum.Compare(asks[i][0], asks[j][0]) // ascending
		if err != nil {
			sortErr = err
			return false
		}
		return cmp < 0
	})
	if sortErr != nil {
		return Orderbook{}, sortErr
	}
	return Orderbook{Bids: bids, Asks: asks}, nil
}

func derivePrice(recent []trading.PerpTradeFull, bestBid, bestAsk *string) string {
	if len(recent) > 0 && recent[0].Price != "" {
		return recent[0].Price
	}
	if mid := trading.DeriveMidPrice(bestBid, bestAsk); mid != nil {
		return *mid
	}
	if bestBid != nil {
		return *bestBid
	}
	if bestAsk != nil {
		return *bestAsk
	}
	return "0"
}

func mapTrades(trades []trading.PerpTradeFull) []TradeHistoryEntry {
	out := make([]TradeHistoryEntry, 0, len(trades))
	for _, t := range trades {
		out = append(out, TradeHistoryEntry{
			ID:        t.ID,
			Token:     t.Token,
			IsLong:    t.IsLong,
			TradeType: t.Type,
			SizeDelta: t.SizeDelta,
			Price:     t.Price,
			Fee:       t.Fee,
			PNL:       t.PNL,
			TxHash:    t.TxHash,
			CreatedAt: t.CreatedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	return out
}

func extractLongSizes(positions []trading.PerpPosition) []string {
	out := make([]string, 0, len(positions))
	for _, p := range positions {
		if p.IsLong {
			out = append(out, p.Size)
		}
	}
	return out
}

func extractShortSizes(positions []trading.PerpPosition) []string {
	out := make([]string, 0, len(positions))
	for _, p := range positions {
		if !p.IsLong {
			out = append(out, p.Size)
		}
	}
	return out
}

func extractDeltas24(trades []trading.PerpTrade) []string {
	out := make([]string, len(trades))
	for i, t := range trades {
		out[i] = t.SizeDelta
	}
	return out
}
