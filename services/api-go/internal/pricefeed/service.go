package pricefeed

import (
	"context"
	"errors"
	"time"
)

// SeriesStatus explains a series the way the rest of this product does:
// "empty" is a real answer (we looked, there is nothing), and is never
// conflated with "unavailable" (we could not look).
const (
	StatusOK          = "ok"
	StatusEmpty       = "empty"
	StatusUnavailable = "unavailable"
)

// Series is the read model returned by the candle endpoints. `Source` is
// always populated so a caller can label exactly what it rendered.
type Series struct {
	Symbol      string   `json:"symbol"`
	Resolution  string   `json:"resolution"`
	Source      string   `json:"source"`
	Provider    string   `json:"provider,omitempty"` // e.g. "gateio"; empty for oracle
	Status      string   `json:"status"`
	Candles     []Candle `json:"candles"`
	GeneratedAt string   `json:"generatedAt"`
}

// Spot is the latest known price for one symbol from one source.
type Spot struct {
	Symbol     string `json:"symbol"`
	Source     string `json:"source"`
	Price      string `json:"price"`
	ObservedAt string `json:"observedAt"`
	Status     string `json:"status"`
	// Oracle-only provenance. Omitted for market prices.
	ChainID     int    `json:"chainId,omitempty"`
	FeedAddress string `json:"feedAddress,omitempty"`
	RoundID     string `json:"roundId,omitempty"`
}

// Service serves stored price data. It never calls a provider on the read
// path — reads are DB-only so a blocked or rate-limited upstream can never
// turn into a slow or failing page render.
type Service struct {
	store    *Store
	provider string // market provider id whose rows this service serves
	now      func() time.Time
}

// NewService builds the read service. providerID is the market provider whose
// rows are served (e.g. "gateio").
func NewService(store *Store, providerID string, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	if providerID == "" {
		providerID = "gateio"
	}
	return &Service{store: store, provider: providerID, now: now}
}

// MaxLimit bounds any candle request.
const MaxLimit = 1000

func clampLimit(limit int) int {
	if limit <= 0 {
		return 200
	}
	if limit > MaxLimit {
		return MaxLimit
	}
	return limit
}

// Candles returns a stored series for the requested source. An unknown source
// yields an error; a known source with no rows yields StatusEmpty and an empty
// (non-nil) slice, so JSON consumers never see null.
func (s *Service) Candles(ctx context.Context, source Source, symbol string, res Resolution, limit int) (Series, error) {
	limit = clampLimit(limit)
	out := Series{
		Symbol:      NormalizeSymbol(symbol),
		Resolution:  string(res),
		Source:      string(source),
		Candles:     []Candle{},
		GeneratedAt: s.now().UTC().Format(time.RFC3339),
	}

	var (
		candles []Candle
		err     error
	)
	switch source {
	case SourceMarket:
		out.Provider = s.provider
		candles, err = s.store.ListMarketCandles(ctx, s.provider, symbol, res, limit)
	case SourceOracle:
		candles, err = s.store.AggregateOracleCandles(ctx, symbol, res, limit)
	default:
		return out, errors.New("pricefeed: unknown source")
	}

	if errors.Is(err, ErrNoPool) {
		out.Status = StatusUnavailable
		return out, nil
	}
	if err != nil {
		return out, err
	}

	if len(candles) == 0 {
		out.Status = StatusEmpty
		return out, nil
	}
	SortCandlesDesc(candles)
	out.Candles = candles
	out.Status = StatusOK
	return out, nil
}

// SpotPrice returns the latest price for a symbol from a source. For the
// market source that is the close of the newest 1m candle — the most recent
// real trade price the venue reported — not a separately-invented "current"
// price.
func (s *Service) SpotPrice(ctx context.Context, source Source, symbol string) (Spot, error) {
	out := Spot{Symbol: NormalizeSymbol(symbol), Source: string(source), Status: StatusEmpty}

	switch source {
	case SourceOracle:
		obs, found, err := s.store.LatestObservation(ctx, symbol)
		if errors.Is(err, ErrNoPool) {
			out.Status = StatusUnavailable
			return out, nil
		}
		if err != nil {
			return out, err
		}
		if !found {
			return out, nil
		}
		out.Price = obs.Price
		out.ObservedAt = obs.ObservedAt.Format(time.RFC3339)
		out.ChainID = obs.ChainID
		out.FeedAddress = obs.FeedAddress
		out.RoundID = obs.RoundID
		out.Status = StatusOK
		return out, nil

	case SourceMarket:
		candles, err := s.store.ListMarketCandles(ctx, s.provider, symbol, Res1m, 1)
		if errors.Is(err, ErrNoPool) {
			out.Status = StatusUnavailable
			return out, nil
		}
		if err != nil {
			return out, err
		}
		if len(candles) == 0 {
			return out, nil
		}
		out.Price = candles[0].Close
		out.ObservedAt = time.UnixMilli(candles[0].Timestamp).UTC().Format(time.RFC3339)
		out.Status = StatusOK
		return out, nil

	default:
		return out, errors.New("pricefeed: unknown source")
	}
}

// Coverage reports what is actually stored, so an empty chart is always
// explainable without shell access.
type Coverage struct {
	Market      []CoverageRow `json:"market"`
	Oracle      []CoverageRow `json:"oracle"`
	Status      string        `json:"status"`
	GeneratedAt string        `json:"generatedAt"`
}

// Coverage returns stored-row coverage for both sources.
func (s *Service) Coverage(ctx context.Context) (Coverage, error) {
	out := Coverage{
		Market:      []CoverageRow{},
		Oracle:      []CoverageRow{},
		Status:      StatusOK,
		GeneratedAt: s.now().UTC().Format(time.RFC3339),
	}
	market, err := s.store.MarketCoverage(ctx)
	if errors.Is(err, ErrNoPool) {
		out.Status = StatusUnavailable
		return out, nil
	}
	if err != nil {
		return out, err
	}
	oracle, err := s.store.OracleCoverage(ctx)
	if err != nil {
		return out, err
	}
	out.Market = market
	out.Oracle = oracle
	if len(market) == 0 && len(oracle) == 0 {
		out.Status = StatusEmpty
	}
	return out, nil
}
