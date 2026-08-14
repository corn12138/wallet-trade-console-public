package markets

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/bigsum"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/deployments"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/token"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/trading"
	"golang.org/x/sync/errgroup"
)

// SymbolView is the JSON shape returned by GET /markets/symbols.
//
// Field order and casing must match the NestJS response exactly — the
// frontend already consumes this contract. Don't reorder or rename
// without updating apps/web in the same PR.
type SymbolView struct {
	Symbol          string `json:"symbol"`
	ChainID         int    `json:"chainId"`
	BaseAsset       string `json:"baseAsset"`
	QuoteAsset      string `json:"quoteAsset"`
	IndexToken      string `json:"indexToken"`
	CollateralToken string `json:"collateralToken"`
	FundingRate     string `json:"fundingRate"`
}

// SnapshotSymbol is one entry under /markets/snapshot → symbols[].
type SnapshotSymbol struct {
	Symbol            string `json:"symbol"`
	ChainID           int    `json:"chainId"`
	Volume24h         string `json:"volume24h"`
	LongOpenInterest  string `json:"longOpenInterest"`
	ShortOpenInterest string `json:"shortOpenInterest"`
	FundingRate       string `json:"fundingRate"`
}

// MarketFullView is the Go equivalent of TradingMarketView from
// legacy NestJS trading/trading.types.ts — symbol metadata + per-chain
// addresses + DB-derived aggregations. Used by /api/discover/home's
// marketPulse feed and any other caller that needs the full picture.
type MarketFullView struct {
	Symbol            string `json:"symbol"`
	ChainID           int    `json:"chainId"`
	IndexToken        string `json:"indexToken"`
	CollateralToken   string `json:"collateralToken"`
	FundingRate       string `json:"fundingRate"`
	Volume24h         string `json:"volume24h"`
	LongOpenInterest  string `json:"longOpenInterest"`
	ShortOpenInterest string `json:"shortOpenInterest"`
}

// SnapshotSummary is the top-level rollup under /markets/snapshot → summary.
type SnapshotSummary struct {
	ActiveMarkets     int    `json:"activeMarkets"`
	TotalVolume       string `json:"totalVolume"`
	TotalOpenInterest string `json:"totalOpenInterest"`
}

// TopMover is one entry under /markets/snapshot → topMovers[].
type TopMover struct {
	Symbol    string  `json:"symbol"`
	Address   *string `json:"address"`
	Change24h float64 `json:"change24h"`
}

// Snapshot is the full payload of GET /markets/snapshot.
type Snapshot struct {
	GeneratedAt string           `json:"generatedAt"`
	Summary     SnapshotSummary  `json:"summary"`
	Symbols     []SnapshotSymbol `json:"symbols"`
	TopMovers   []TopMover       `json:"topMovers"`
}

// DeploymentsLoader abstracts the on-disk deployments registry so tests
// can inject fake deployment maps without touching the filesystem.
type DeploymentsLoader interface {
	Load() (map[int]deployments.ChainConfig, error)
}

// TradingRepository is the markets-side view of the trading data layer.
// Defined here (not in `internal/trading`) so this package can be tested
// without spinning Postgres.
type TradingRepository interface {
	ListOpenPositions(ctx context.Context, symbols []string, chainID *int) ([]trading.PerpPosition, error)
	ListRecentTrades(ctx context.Context, symbols []string, chainID *int, since time.Time) ([]trading.PerpTrade, error)
	ListOrderbookOrders(ctx context.Context, symbol string, chainID *int) ([]trading.PerpOrder, error)
	ListRecentTradesForSymbol(ctx context.Context, symbol string, chainID *int, limit int) ([]trading.PerpTradeFull, error)
}

// TokenRepository is the markets-side view of the token data layer.
type TokenRepository interface {
	ListTrending(ctx context.Context, chainID *int, limit int) ([]token.TrendingToken, error)
}

// Service is the markets read service. It resolves the per-chain
// contract addresses for each statically-configured market and skips
// markets whose contracts haven't been deployed to a given chain yet.
type Service struct {
	deploymentsLoader DeploymentsLoader
	trading           TradingRepository
	tokens            TokenRepository
	clock             func() time.Time
}

// NewService wires the markets service. tradingRepo and tokenRepo may
// be nil — GetSnapshot will skip the DB-backed sections gracefully (so
// /markets/snapshot keeps returning a valid empty-summary payload when
// the Go service is bootstrapped without database envs).
func NewService(loader DeploymentsLoader, tradingRepo TradingRepository, tokenRepo TokenRepository) *Service {
	return &Service{
		deploymentsLoader: loader,
		trading:           tradingRepo,
		tokens:            tokenRepo,
		clock:             time.Now,
	}
}

// SetClock overrides the time source. Tests use this to pin
// generatedAt; production never calls it.
func (s *Service) SetClock(now func() time.Time) {
	s.clock = now
}

// GetMarketsFull returns the deployed markets enriched with per-market
// DB-derived aggregations (volume24h, long/short open interest). Used
// by cross-module callers (e.g. /api/discover/home marketPulse).
//
// Degrades gracefully like GetSnapshot: a nil TradingRepository or a
// pool-unavailable error yields zeroed sums rather than failing.
func (s *Service) GetMarketsFull(ctx context.Context, chainIDFilter *int) ([]MarketFullView, error) {
	deployed, err := s.deployedMarkets(chainIDFilter)
	if err != nil {
		return nil, err
	}
	if len(deployed) == 0 {
		return []MarketFullView{}, nil
	}

	symbols := make([]string, len(deployed))
	for i, m := range deployed {
		symbols[i] = m.def.Symbol
	}

	var (
		positions []trading.PerpPosition
		trades    []trading.PerpTrade
	)
	if s.trading != nil {
		since := s.clock().Add(-24 * time.Hour)
		if p, err := s.trading.ListOpenPositions(ctx, symbols, chainIDFilter); err == nil {
			positions = p
		} else if !errors.Is(err, trading.ErrPoolUnavailable) {
			return nil, err
		}
		if tr, err := s.trading.ListRecentTrades(ctx, symbols, chainIDFilter, since); err == nil {
			trades = tr
		} else if !errors.Is(err, trading.ErrPoolUnavailable) {
			return nil, err
		}
	}

	longSizes := map[string][]string{}
	shortSizes := map[string][]string{}
	for _, p := range positions {
		if p.IsLong {
			longSizes[p.Token] = append(longSizes[p.Token], p.Size)
		} else {
			shortSizes[p.Token] = append(shortSizes[p.Token], p.Size)
		}
	}
	volumes := map[string][]string{}
	for _, t := range trades {
		volumes[t.Token] = append(volumes[t.Token], t.SizeDelta)
	}

	out := make([]MarketFullView, 0, len(deployed))
	for _, m := range deployed {
		long, err := bigsum.Sum(longSizes[m.def.Symbol])
		if err != nil {
			return nil, err
		}
		short, err := bigsum.Sum(shortSizes[m.def.Symbol])
		if err != nil {
			return nil, err
		}
		vol, err := bigsum.Sum(volumes[m.def.Symbol])
		if err != nil {
			return nil, err
		}
		out = append(out, MarketFullView{
			Symbol:            m.def.Symbol,
			ChainID:           m.def.ChainID,
			IndexToken:        m.indexToken,
			CollateralToken:   m.collateralToken,
			FundingRate:       m.def.FundingRate,
			Volume24h:         vol,
			LongOpenInterest:  long,
			ShortOpenInterest: short,
		})
	}
	return out, nil
}

// GetSymbols returns the deployed markets, optionally filtered to one
// chain. Markets whose index or collateral contract isn't deployed on
// the chain are dropped from the result — matches NestJS exactly.
func (s *Service) GetSymbols(chainIDFilter *int) ([]SymbolView, error) {
	deployed, err := s.deployedMarkets(chainIDFilter)
	if err != nil {
		return nil, err
	}
	out := make([]SymbolView, 0, len(deployed))
	for _, m := range deployed {
		base, quote := splitSymbol(m.def.Symbol)
		out = append(out, SymbolView{
			Symbol:          m.def.Symbol,
			ChainID:         m.def.ChainID,
			BaseAsset:       base,
			QuoteAsset:      quote,
			IndexToken:      m.indexToken,
			CollateralToken: m.collateralToken,
			FundingRate:     m.def.FundingRate,
		})
	}
	return out, nil
}

// GetSnapshot fans out the three downstream queries in parallel and
// assembles the snapshot payload. Mirrors MarketsService.getSnapshot.
// A nil TradingRepository / TokenRepository degrades to empty
// sums/lists rather than failing the request.
func (s *Service) GetSnapshot(ctx context.Context, chainIDFilter *int) (Snapshot, error) {
	deployed, err := s.deployedMarkets(chainIDFilter)
	if err != nil {
		return Snapshot{}, err
	}

	symbolList := make([]string, len(deployed))
	for i, m := range deployed {
		symbolList[i] = m.def.Symbol
	}

	since := s.clock().Add(-24 * time.Hour)

	var (
		positionsBySymbol []trading.PerpPosition
		tradesBySymbol    []trading.PerpTrade
		positionsAll      []trading.PerpPosition
		tradesAll         []trading.PerpTrade
		trendingTokens    []token.TrendingToken
	)

	g, gctx := errgroup.WithContext(ctx)

	if s.trading != nil && len(symbolList) > 0 {
		g.Go(func() error {
			res, err := s.trading.ListOpenPositions(gctx, symbolList, chainIDFilter)
			if err != nil {
				return err
			}
			positionsBySymbol = res
			return nil
		})
		g.Go(func() error {
			res, err := s.trading.ListRecentTrades(gctx, symbolList, chainIDFilter, since)
			if err != nil {
				return err
			}
			tradesBySymbol = res
			return nil
		})
	}
	if s.trading != nil {
		g.Go(func() error {
			res, err := s.trading.ListOpenPositions(gctx, nil, chainIDFilter)
			if err != nil {
				return err
			}
			positionsAll = res
			return nil
		})
		g.Go(func() error {
			res, err := s.trading.ListRecentTrades(gctx, nil, chainIDFilter, since)
			if err != nil {
				return err
			}
			tradesAll = res
			return nil
		})
	}
	if s.tokens != nil {
		g.Go(func() error {
			res, err := s.tokens.ListTrending(gctx, chainIDFilter, 5)
			if err != nil {
				return err
			}
			trendingTokens = res
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		// Pool-unavailable errors degrade to empty data; anything else
		// is a real failure that should 500.
		if !errors.Is(err, trading.ErrPoolUnavailable) && !errors.Is(err, token.ErrPoolUnavailable) {
			return Snapshot{}, err
		}
	}

	symbolEntries, err := buildSnapshotSymbols(deployed, positionsBySymbol, tradesBySymbol)
	if err != nil {
		return Snapshot{}, err
	}

	totalOI, err := bigsum.Sum(extractSizes(positionsAll))
	if err != nil {
		return Snapshot{}, err
	}
	totalVol, err := bigsum.Sum(extractDeltas(tradesAll))
	if err != nil {
		return Snapshot{}, err
	}

	return Snapshot{
		GeneratedAt: s.clock().UTC().Format(time.RFC3339Nano),
		Summary: SnapshotSummary{
			ActiveMarkets:     len(deployed),
			TotalVolume:       totalVol,
			TotalOpenInterest: totalOI,
		},
		Symbols:   symbolEntries,
		TopMovers: mapTopMovers(trendingTokens),
	}, nil
}

type deployedMarket struct {
	def             Definition
	indexToken      string
	collateralToken string
}

// deployedMarkets is the shared filter used by both GetSymbols and
// GetSnapshot: drop markets whose contracts aren't on the chain.
func (s *Service) deployedMarkets(chainIDFilter *int) ([]deployedMarket, error) {
	registry, err := s.deploymentsLoader.Load()
	if err != nil {
		return nil, err
	}
	defs := Definitions(chainIDFilter)
	out := make([]deployedMarket, 0, len(defs))
	for _, def := range defs {
		cfg := registry[def.ChainID]
		idx := deployments.LookupAddress(cfg, def.IndexContractNames...)
		col := deployments.LookupAddress(cfg, def.CollateralContractNames...)
		if idx == "" || col == "" {
			continue
		}
		out = append(out, deployedMarket{def: def, indexToken: idx, collateralToken: col})
	}
	return out, nil
}

func buildSnapshotSymbols(deployed []deployedMarket, positions []trading.PerpPosition, trades []trading.PerpTrade) ([]SnapshotSymbol, error) {
	longSizes := map[string][]string{}
	shortSizes := map[string][]string{}
	for _, p := range positions {
		if p.IsLong {
			longSizes[p.Token] = append(longSizes[p.Token], p.Size)
		} else {
			shortSizes[p.Token] = append(shortSizes[p.Token], p.Size)
		}
	}

	volumes := map[string][]string{}
	for _, t := range trades {
		volumes[t.Token] = append(volumes[t.Token], t.SizeDelta)
	}

	out := make([]SnapshotSymbol, 0, len(deployed))
	for _, m := range deployed {
		long, err := bigsum.Sum(longSizes[m.def.Symbol])
		if err != nil {
			return nil, err
		}
		short, err := bigsum.Sum(shortSizes[m.def.Symbol])
		if err != nil {
			return nil, err
		}
		vol, err := bigsum.Sum(volumes[m.def.Symbol])
		if err != nil {
			return nil, err
		}
		out = append(out, SnapshotSymbol{
			Symbol:            m.def.Symbol,
			ChainID:           m.def.ChainID,
			Volume24h:         vol,
			LongOpenInterest:  long,
			ShortOpenInterest: short,
			FundingRate:       m.def.FundingRate,
		})
	}
	return out, nil
}

func mapTopMovers(tokens []token.TrendingToken) []TopMover {
	out := make([]TopMover, 0, len(tokens))
	for _, t := range tokens {
		change := 0.0
		if t.PriceChange24h != nil {
			change = *t.PriceChange24h
		}
		out = append(out, TopMover{
			Symbol:    t.Symbol,
			Address:   t.Address,
			Change24h: change,
		})
	}
	return out
}

func extractSizes(positions []trading.PerpPosition) []string {
	out := make([]string, len(positions))
	for i, p := range positions {
		out[i] = p.Size
	}
	return out
}

func extractDeltas(trades []trading.PerpTrade) []string {
	out := make([]string, len(trades))
	for i, t := range trades {
		out[i] = t.SizeDelta
	}
	return out
}

func splitSymbol(symbol string) (base, quote string) {
	idx := strings.IndexByte(symbol, '-')
	if idx < 0 {
		return symbol, ""
	}
	return symbol[:idx], symbol[idx+1:]
}
