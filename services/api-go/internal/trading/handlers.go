// Package trading's HTTP surface mirrors
// legacy NestJS trading/trading.controller.ts. Eight read endpoints
// land in Phase 4s:
//
//	GET /api/trading/markets             (public)
//	GET /api/trading/positions/{account}
//	GET /api/trading/orders/{account}
//	GET /api/trading/history/{account}
//	GET /api/trading/orderbook           (public)
//	GET /api/trading/market-trades       (public)
//	GET /api/trading/stats               (public)
//	GET /api/trading/candles             (public)
//
// The account-scoped routes carry wallet-private product data. The Go product
// uses the SIWE wallet session for these routes and owner-pins each path account;
// the market-wide reads stay public.
package trading

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/bigsum"
	"github.com/go-chi/chi/v5"
)

// MarketsProvider supplies the GetMarketsFull aggregation the trading
// /markets endpoint returns. The trading package can't import internal/
// markets directly (markets already imports trading — import cycle), so
// the dependency lives behind this interface. cmd/api passes a thin
// adapter over markets.Service.
type MarketsProvider interface {
	GetMarketsFull(ctx context.Context, chainID *int) ([]MarketSnapshotRow, error)
}

// MarketSnapshotRow is the trading.Markets response shape. Field-for-
// field parity with markets.MarketFullView, but redeclared here so the
// trading package doesn't depend on internal/markets's types.
type MarketSnapshotRow struct {
	Symbol            string `json:"symbol"`
	ChainID           int    `json:"chainId"`
	IndexToken        string `json:"indexToken"`
	CollateralToken   string `json:"collateralToken"`
	FundingRate       string `json:"fundingRate"`
	Volume24h         string `json:"volume24h"`
	LongOpenInterest  string `json:"longOpenInterest"`
	ShortOpenInterest string `json:"shortOpenInterest"`
}

// PositionView is the JSON shape /trading/positions/{account} returns
// per position. Fields mirror the raw Prisma serialization NestJS
// returns from `prisma.perpPosition.findMany` (no transform).
type PositionView struct {
	ID         string `json:"id"`
	ChainID    int    `json:"chainId"`
	Account    string `json:"account"`
	Token      string `json:"token"`
	IsLong     bool   `json:"isLong"`
	Size       string `json:"size"`
	Collateral string `json:"collateral"`
	EntryPrice string `json:"entryPrice"`
	MarkPrice  string `json:"markPrice"`
	PNL        string `json:"pnl"`
	Status     string `json:"status"`
	TxHash     string `json:"txHash"`
	CreatedAt  string `json:"createdAt"`
	UpdatedAt  string `json:"updatedAt"`
}

// PendingOrderView is the trading.types.ts TradingPendingOrderView shape.
// TriggerPrice is *string because the column is nullable (market orders
// have no trigger price); JSON null parity with Prisma.
type PendingOrderView struct {
	ID           string  `json:"id"`
	Token        string  `json:"token"`
	IsLong       bool    `json:"isLong"`
	OrderType    string  `json:"orderType"`
	SizeDelta    string  `json:"sizeDelta"`
	TriggerPrice *string `json:"triggerPrice"`
	Status       string  `json:"status"`
	CreatedAt    string  `json:"createdAt"`
}

// TradeHistoryView matches trading.types.ts TradingTradeHistoryView.
// Fee is non-null in Prisma (defaults to "0"); pnl is nullable.
type TradeHistoryView struct {
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

// OrderbookView is the {bids, asks} response of /trading/orderbook.
// Bids/asks are [[price, size], ...] pairs as strings (USD with 30
// decimals, sorted by price).
type OrderbookView struct {
	Bids [][2]string `json:"bids"`
	Asks [][2]string `json:"asks"`
}

// MarketStatsView is the /trading/stats response — two big-int sums.
type MarketStatsView struct {
	TotalVolume       string `json:"totalVolume"`
	TotalOpenInterest string `json:"totalOpenInterest"`
}

// Service holds the repo + the markets provider for the /markets
// endpoint. nil markets is tolerated; the /markets route degrades to
// [] when missing.
type Service struct {
	repo    *Repository
	markets MarketsProvider
}

// NewService binds a service. Passing a nil markets is fine for tests
// that don't need /trading/markets.
func NewService(repo *Repository, markets MarketsProvider) *Service {
	return &Service{repo: repo, markets: markets}
}

// GetMarkets delegates to the markets provider. Degrades to [] when
// the provider isn't wired (cmd/api will pass one when the deployments
// dir is configured).
func (s *Service) GetMarkets(ctx context.Context, chainID *int) ([]MarketSnapshotRow, error) {
	if s.markets == nil {
		return []MarketSnapshotRow{}, nil
	}
	return s.markets.GetMarketsFull(ctx, chainID)
}

// GetPositions for one account. Degrades to []; same pattern as
// trades/orders.
func (s *Service) GetPositions(ctx context.Context, account string, chainID *int) ([]PositionView, error) {
	rows, err := s.repo.ListOpenPositionsForAccount(ctx, account, chainID)
	if errors.Is(err, ErrPoolUnavailable) {
		return []PositionView{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]PositionView, 0, len(rows))
	for _, r := range rows {
		out = append(out, positionViewFromRow(r))
	}
	return out, nil
}

// GetPendingOrders for one account, optionally filtered by symbol.
// Limit is 50 (matches NestJS take: 50).
func (s *Service) GetPendingOrders(ctx context.Context, account, symbol string, chainID *int) ([]PendingOrderView, error) {
	rows, err := s.repo.ListPendingOrdersForAccount(ctx, account, NormalizeSymbol(symbol), chainID, 50)
	if errors.Is(err, ErrPoolUnavailable) {
		return []PendingOrderView{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]PendingOrderView, 0, len(rows))
	for _, o := range rows {
		out = append(out, PendingOrderView{
			ID:           o.ID,
			Token:        o.Token,
			IsLong:       o.IsLong,
			OrderType:    o.Type,
			SizeDelta:    o.SizeDelta,
			TriggerPrice: o.TriggerPrice,
			Status:       o.Status,
			CreatedAt:    o.CreatedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	return out, nil
}

// GetTradeHistory for one account, newest first.
func (s *Service) GetTradeHistory(ctx context.Context, account, symbol string, chainID *int) ([]TradeHistoryView, error) {
	rows, err := s.repo.ListTradesForAccount(ctx, account, NormalizeSymbol(symbol), chainID, 50)
	if errors.Is(err, ErrPoolUnavailable) {
		return []TradeHistoryView{}, nil
	}
	if err != nil {
		return nil, err
	}
	return mapTradeHistoryViews(rows), nil
}

// GetOrderbook returns the {bids, asks} pair for one market. Bids are
// sorted price-desc, asks price-asc — matches the NestJS sort using
// compareNumericStrings.
func (s *Service) GetOrderbook(ctx context.Context, symbol string, chainID *int) (OrderbookView, error) {
	rows, err := s.repo.ListOrderbookOrders(ctx, NormalizeSymbol(symbol), chainID)
	if errors.Is(err, ErrPoolUnavailable) {
		return OrderbookView{Bids: [][2]string{}, Asks: [][2]string{}}, nil
	}
	if err != nil {
		return OrderbookView{}, err
	}
	bids := make([][2]string, 0, len(rows))
	asks := make([][2]string, 0, len(rows))
	for _, o := range rows {
		// triggerPrice is *string (nullable); the orderbook SQL filters
		// NULL but the type is still pointer for ListPendingOrders.
		if o.TriggerPrice == nil || *o.TriggerPrice == "" {
			continue
		}
		pair := [2]string{*o.TriggerPrice, o.SizeDelta}
		if o.IsLong {
			bids = append(bids, pair)
		} else {
			asks = append(asks, pair)
		}
	}
	// Bids: price DESC (highest bid first). Use bigsum.Compare for
	// big-int safety since prices can be 30-decimal USD strings.
	sort.SliceStable(bids, func(i, j int) bool {
		cmp, _ := bigsum.Compare(bids[i][0], bids[j][0])
		return cmp > 0
	})
	// Asks: price ASC (lowest ask first).
	sort.SliceStable(asks, func(i, j int) bool {
		cmp, _ := bigsum.Compare(asks[i][0], asks[j][0])
		return cmp < 0
	})
	return OrderbookView{Bids: bids, Asks: asks}, nil
}

// GetRecentMarketTrades returns up to limit (clamped 1..200) recent
// trades for one symbol.
func (s *Service) GetRecentMarketTrades(ctx context.Context, symbol string, chainID *int, limit int) ([]TradeHistoryView, error) {
	rows, err := s.repo.ListRecentTradesForSymbol(ctx, NormalizeSymbol(symbol), chainID, limit)
	if errors.Is(err, ErrPoolUnavailable) {
		return []TradeHistoryView{}, nil
	}
	if err != nil {
		return nil, err
	}
	return mapTradeHistoryViews(rows), nil
}

// GetMarketStats returns {totalVolume, totalOpenInterest} as big-int
// sums. Both sub-queries run in parallel via two goroutines + channels;
// errgroup adds an import we don't need for two queries.
func (s *Service) GetMarketStats(ctx context.Context, chainID *int) (MarketStatsView, error) {
	since := time.Now().Add(-24 * time.Hour)

	type sizesResult struct {
		sizes []string
		err   error
	}
	posCh := make(chan sizesResult, 1)
	tradesCh := make(chan sizesResult, 1)
	go func() {
		sizes, err := s.repo.ListAllOpenPositionSizes(ctx, chainID)
		if errors.Is(err, ErrPoolUnavailable) {
			posCh <- sizesResult{sizes: nil}
			return
		}
		posCh <- sizesResult{sizes: sizes, err: err}
	}()
	go func() {
		sizes, err := s.repo.ListAllTradesSizeDeltas(ctx, chainID, since)
		if errors.Is(err, ErrPoolUnavailable) {
			tradesCh <- sizesResult{sizes: nil}
			return
		}
		tradesCh <- sizesResult{sizes: sizes, err: err}
	}()
	posRes := <-posCh
	tradesRes := <-tradesCh
	if posRes.err != nil {
		return MarketStatsView{}, posRes.err
	}
	if tradesRes.err != nil {
		return MarketStatsView{}, tradesRes.err
	}
	totalOI, err := bigsum.Sum(posRes.sizes)
	if err != nil {
		return MarketStatsView{}, err
	}
	totalVol, err := bigsum.Sum(tradesRes.sizes)
	if err != nil {
		return MarketStatsView{}, err
	}
	return MarketStatsView{TotalVolume: totalVol, TotalOpenInterest: totalOI}, nil
}

// positionViewFromRow maps the wider Repository projection to the
// JSON-facing PositionView. CreatedAt/UpdatedAt are ISO-8601.
func positionViewFromRow(r PerpPositionForAccount) PositionView {
	return PositionView{
		ID:         r.ID,
		ChainID:    r.ChainID,
		Account:    r.Account,
		Token:      r.Token,
		IsLong:     r.IsLong,
		Size:       r.Size,
		Collateral: r.Collateral,
		EntryPrice: r.EntryPrice,
		MarkPrice:  r.MarkPrice,
		PNL:        r.PNL,
		Status:     r.Status,
		TxHash:     r.TxHash,
		CreatedAt:  r.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:  r.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func mapTradeHistoryViews(rows []PerpTradeFull) []TradeHistoryView {
	out := make([]TradeHistoryView, 0, len(rows))
	for _, t := range rows {
		out = append(out, TradeHistoryView{
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

// NormalizeSymbol is in helpers.go — re-export not needed.

// --- router ---

// Router mounts the eight read endpoints under /api/trading. The market-wide
// reads are public; positions/orders/history are wallet-private product views.
// They use the SIWE middleware and owner-pin the account path to the verified
// wallet. A nil middleware remains available for isolated handler tests.
func Router(svc *Service, walletMiddleware func(http.Handler) http.Handler) chi.Router {
	r := chi.NewRouter()
	r.Get("/markets", makeMarketsHandler(svc))
	r.Get("/orderbook", makeOrderbookHandler(svc))
	r.Get("/market-trades", makeMarketTradesHandler(svc))
	r.Get("/stats", makeStatsHandler(svc))
	r.Get("/candles", makeCandlesHandler(svc))
	r.Group(func(r chi.Router) {
		if walletMiddleware != nil {
			r.Use(walletMiddleware)
		}
		r.Get("/positions/{account}", makePositionsHandler(svc))
		r.Get("/orders/{account}", makeOrdersHandler(svc))
		r.Get("/history/{account}", makeHistoryHandler(svc))
	})
	return r
}

func makeMarketsHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		chainID := parseChainIDQuery(r.URL.Query().Get("chainId"))
		out, err := svc.GetMarkets(r.Context(), chainID)
		if err != nil {
			slog.ErrorContext(r.Context(), "trading markets failed", "err", err)
			http.Error(w, "failed to load markets", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func makePositionsHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		account, ok := resolveTradingAccount(w, r)
		if !ok {
			return
		}
		chainID := parseChainIDQuery(r.URL.Query().Get("chainId"))
		out, err := svc.GetPositions(r.Context(), account, chainID)
		if err != nil {
			slog.ErrorContext(r.Context(), "trading positions failed", "err", err, "account", account)
			http.Error(w, "failed to load positions", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func makeOrdersHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		account, ok := resolveTradingAccount(w, r)
		if !ok {
			return
		}
		q := r.URL.Query()
		symbol := q.Get("symbol")
		chainID := parseChainIDQuery(q.Get("chainId"))
		out, err := svc.GetPendingOrders(r.Context(), account, symbol, chainID)
		if err != nil {
			slog.ErrorContext(r.Context(), "trading orders failed", "err", err, "account", account)
			http.Error(w, "failed to load orders", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func makeHistoryHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		account, ok := resolveTradingAccount(w, r)
		if !ok {
			return
		}
		q := r.URL.Query()
		symbol := q.Get("symbol")
		chainID := parseChainIDQuery(q.Get("chainId"))
		out, err := svc.GetTradeHistory(r.Context(), account, symbol, chainID)
		if err != nil {
			slog.ErrorContext(r.Context(), "trading history failed", "err", err, "account", account)
			http.Error(w, "failed to load trade history", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func resolveTradingAccount(w http.ResponseWriter, r *http.Request) (string, bool) {
	account, err := auth.ResolveOwner(
		auth.AddressFromContext(r.Context()),
		chi.URLParam(r, "account"),
	)
	if err == nil {
		return account, true
	}

	status := http.StatusUnauthorized
	message := "authenticated wallet required"
	switch {
	case errors.Is(err, auth.ErrInvalidRequestedAddress):
		status = http.StatusBadRequest
		message = "invalid account address"
	case errors.Is(err, auth.ErrOwnerMismatch):
		status = http.StatusForbidden
		message = "account does not match authenticated wallet"
	}
	writeJSON(w, status, map[string]any{"message": message, "statusCode": status})
	return "", false
}

func makeOrderbookHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		symbol := q.Get("symbol")
		if strings.TrimSpace(symbol) == "" {
			http.Error(w, "symbol required", http.StatusBadRequest)
			return
		}
		chainID := parseChainIDQuery(q.Get("chainId"))
		out, err := svc.GetOrderbook(r.Context(), symbol, chainID)
		if err != nil {
			slog.ErrorContext(r.Context(), "trading orderbook failed", "err", err, "symbol", symbol)
			http.Error(w, "failed to load orderbook", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func makeMarketTradesHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		symbol := q.Get("symbol")
		if strings.TrimSpace(symbol) == "" {
			http.Error(w, "symbol required", http.StatusBadRequest)
			return
		}
		chainID := parseChainIDQuery(q.Get("chainId"))
		limit := clampListLimit(q.Get("limit"), 20, 200)
		out, err := svc.GetRecentMarketTrades(r.Context(), symbol, chainID, limit)
		if err != nil {
			slog.ErrorContext(r.Context(), "trading market-trades failed", "err", err, "symbol", symbol)
			http.Error(w, "failed to load market trades", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func makeStatsHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		chainID := parseChainIDQuery(r.URL.Query().Get("chainId"))
		out, err := svc.GetMarketStats(r.Context(), chainID)
		if err != nil {
			slog.ErrorContext(r.Context(), "trading stats failed", "err", err)
			http.Error(w, "failed to load stats", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// makeCandlesHandler delegates to GetCandles in candles.go.
func makeCandlesHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		symbol := q.Get("symbol")
		if strings.TrimSpace(symbol) == "" {
			// NestJS returns [] for empty symbol; do the same so the
			// chart can render an empty state.
			writeJSON(w, http.StatusOK, []TradingCandleView{})
			return
		}
		resolution := ParseTradingCandleResolution(q.Get("resolution"))
		limit := clampListLimit(q.Get("limit"), 200, 500)
		from := parseInt64Query(q.Get("from"))
		to := parseInt64Query(q.Get("to"))
		chainID := parseChainIDQuery(q.Get("chainId"))
		out, err := svc.GetCandles(r.Context(), symbol, resolution, limit, from, to, chainID)
		if err != nil {
			slog.ErrorContext(r.Context(), "trading candles failed", "err", err, "symbol", symbol)
			http.Error(w, "failed to load candles", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// parseChainIDQuery mirrors the NestJS Number(value) ? ... : undefined
// coercion: empty / non-numeric / zero → no filter; positive int →
// filter pointer.
func parseChainIDQuery(raw string) *int {
	if raw == "" {
		return nil
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v == 0 {
		return nil
	}
	return &v
}

// parseInt64Query returns a pointer-int64 mirroring NestJS Number(...)
// semantics: empty / non-numeric → 0; valid → positive.
func parseInt64Query(raw string) int64 {
	if raw == "" {
		return 0
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// clampListLimit mirrors the NestJS clampListLimit helper: never lets
// the controller-supplied limit hit Postgres unbounded.
func clampListLimit(raw string, defaultLimit, max int) int {
	if raw == "" {
		return defaultLimit
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		return defaultLimit
	}
	if v > max {
		return max
	}
	return v
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Error("trading response encode failed", "err", err)
	}
}
