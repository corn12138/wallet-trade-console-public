// Package discover ports the NestJS /api/discover/home aggregation.
// Mirrors legacy NestJS discover/discover.service.ts byte-for-byte
// in JSON shape — the existing frontend client already consumes this
// contract.
package discover

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/markets"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/staking"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/token"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/web3events"
	"github.com/go-chi/chi/v5"
	"golang.org/x/sync/errgroup"
)

// Home is the GET /api/discover/home response shape.
type Home struct {
	GeneratedAt    string             `json:"generatedAt"`
	TrendingTokens []TrendingTokenOut `json:"trendingTokens"`
	CuratedDapps   []CuratedDapp      `json:"curatedDapps"`
	EarnProducts   []EarnProductOut   `json:"earnProducts"`
	MarketPulse    []MarketPulseEntry `json:"marketPulse"`
	RiskMarkers    []RiskMarker       `json:"riskMarkers"`
}

// TrendingTokenOut is one token in the trendingTokens feed.
// priceChange24h / volume24h / marketCap use NestJS `|| 0` coercion —
// null DB values surface as 0 in the JSON, matching the contract.
type TrendingTokenOut struct {
	ID             string   `json:"id"`
	Symbol         string   `json:"symbol"`
	Name           string   `json:"name"`
	Address        *string  `json:"address"`
	PriceChange24h float64  `json:"priceChange24h"`
	Volume24h      float64  `json:"volume24h"`
	MarketCap      float64  `json:"marketCap"`
	Badges         []string `json:"badges"`
}

// CuratedDapp is a static editorial card. Status distinguishes a shippable
// product surface ("live") from a not-yet-launchable one ("roadmap"): the
// frontend renders roadmap cards without a launch affordance so Discover never
// advertises a disabled product as a normal app.
type CuratedDapp struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Slug     string `json:"slug"`
	Category string `json:"category"`
	Summary  string `json:"summary"`
	Risk     string `json:"risk"`
	Status   string `json:"status"`
}

// EarnProductOut is one row under earnProducts (top 4 active staking pools).
type EarnProductOut struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	Type    string  `json:"type"`
	APY     float64 `json:"apy"`
	TVL     float64 `json:"tvl"`
	ChainID int     `json:"chainId"`
	Status  string  `json:"status"`
}

// MarketPulseEntry is one row under marketPulse (top 5 markets).
type MarketPulseEntry struct {
	Symbol       string                `json:"symbol"`
	ChainID      int                   `json:"chainId"`
	Volume24h    string                `json:"volume24h"`
	FundingRate  string                `json:"fundingRate"`
	OpenInterest MarketOpenInterestOut `json:"openInterest"`
}

// MarketOpenInterestOut splits long vs short for the marketPulse card.
type MarketOpenInterestOut struct {
	Long  string `json:"long"`
	Short string `json:"short"`
}

// RiskMarker is one indicator card under riskMarkers. NestJS emits two
// fixed entries derived from the web3-events stats; we mirror the same
// IDs and labels.
type RiskMarker struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Value any    `json:"value"`
}

// curatedDapps is static editorial content. "live" means wallet-executable
// today; "roadmap" means the frontend renders the card without a launch
// affordance, so Discover never presents a non-launchable product as a normal
// app.
//
// Bridge was roadmap while no adapter or relayer existed. BridgeGateway is
// deployed on Sepolia and Base Sepolia, the relayer delivers, and a transfer
// has completed end to end — so the card is live. Understating a shipped
// feature is the same failure as overstating one: both are a card that does not
// describe the product. Its `risk` stays "preview" because the trust model is
// real and the pre-sign review says so on every transfer.
var curatedDapps = []CuratedDapp{
	{ID: "swap", Name: "Swap Center", Slug: "/swap", Category: "Trading", Summary: "Real quote + allowance review + tx builder surface.", Risk: "managed", Status: "live"},
	{ID: "bridge", Name: "Bridge Center", Slug: "/bridge", Category: "Cross-chain", Summary: "Lock-and-release transfers between deployed gateways, delivered by a trusted relayer. Route state, pause and destination liquidity are read on-chain before you sign.", Risk: "preview", Status: "live"},
	{ID: "security", Name: "Security Center", Slug: "/security", Category: "Risk", Summary: "Approval audit, wallet-signed revoke, and transaction review entry.", Risk: "critical", Status: "live"},
}

// MarketsService is the markets-side input. Defined as an interface so
// tests can fake it without spinning the markets package's full graph.
type MarketsService interface {
	GetMarketsFull(ctx context.Context, chainID *int) ([]markets.MarketFullView, error)
}

// TokenRepository is the slice of internal/token used by Discover.
type TokenRepository interface {
	ListTrendingFull(ctx context.Context, chainID *int, limit int) ([]token.TrendingTokenFull, error)
}

// StakingRepository is the slice of internal/staking used by Discover.
type StakingRepository interface {
	ListActivePools(ctx context.Context, chainID *int) ([]staking.PoolView, error)
}

// EventsRepository is the slice of internal/web3events used by Discover.
type EventsRepository interface {
	GetStats(ctx context.Context, chainID int) (web3events.Stats, error)
}

// Service composes the four downstream queries that build the discover
// home payload.
type Service struct {
	markets      MarketsService
	tokens       TokenRepository
	stakingPools StakingRepository
	web3Events   EventsRepository
	clock        func() time.Time
}

// NewService wires the discover service. Any nil dep degrades the
// corresponding section of the response rather than failing the whole
// endpoint — matches the markets package degraded-mode pattern.
func NewService(m MarketsService, t TokenRepository, s StakingRepository, e EventsRepository) *Service {
	return &Service{
		markets:      m,
		tokens:       t,
		stakingPools: s,
		web3Events:   e,
		clock:        time.Now,
	}
}

// SetClock overrides the time source. Tests use this; production
// never calls it.
func (s *Service) SetClock(now func() time.Time) {
	s.clock = now
}

// GetHome assembles the /api/discover/home payload. chainID:
//   - nil → fall back to 11155111 ONLY for the web3-events stats query
//     (matches NestJS `chainId || 11155111` for that single call site).
//     All other queries pass the original nil through.
func (s *Service) GetHome(ctx context.Context, chainID *int) (Home, error) {
	var (
		trendingRows []token.TrendingTokenFull
		poolRows     []staking.PoolView
		marketRows   []markets.MarketFullView
		eventStats   web3events.Stats
	)

	g, gctx := errgroup.WithContext(ctx)
	if s.tokens != nil {
		g.Go(func() error {
			res, err := s.tokens.ListTrendingFull(gctx, chainID, 6)
			if err != nil {
				return err
			}
			trendingRows = res
			return nil
		})
	}
	if s.stakingPools != nil {
		g.Go(func() error {
			res, err := s.stakingPools.ListActivePools(gctx, chainID)
			if err != nil {
				return err
			}
			poolRows = res
			return nil
		})
	}
	if s.markets != nil {
		g.Go(func() error {
			res, err := s.markets.GetMarketsFull(gctx, chainID)
			if err != nil {
				return err
			}
			marketRows = res
			return nil
		})
	}
	if s.web3Events != nil {
		g.Go(func() error {
			eventsChain := 11155111
			if chainID != nil {
				eventsChain = *chainID
			}
			res, err := s.web3Events.GetStats(gctx, eventsChain)
			if err != nil {
				return err
			}
			eventStats = res
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		// Pool-unavailable across any downstream → degraded. Anything
		// else is a hard failure.
		if !isDegradable(err) {
			return Home{}, err
		}
	}

	return Home{
		GeneratedAt:    s.clock().UTC().Format(time.RFC3339Nano),
		TrendingTokens: mapTrending(trendingRows),
		CuratedDapps:   curatedDapps,
		EarnProducts:   mapEarnProducts(poolRows),
		MarketPulse:    mapMarketPulse(marketRows),
		RiskMarkers:    buildRiskMarkers(eventStats),
	}, nil
}

// isDegradable returns true for the pool-unavailable sentinels each
// sibling repo exposes. Markets.GetMarketsFull absorbs its own
// trading-side pool errors internally, so we only need the three
// repos discover talks to directly.
func isDegradable(err error) bool {
	return errors.Is(err, staking.ErrPoolUnavailable) ||
		errors.Is(err, web3events.ErrPoolUnavailable) ||
		errors.Is(err, token.ErrPoolUnavailable)
}

func mapTrending(rows []token.TrendingTokenFull) []TrendingTokenOut {
	out := make([]TrendingTokenOut, 0, len(rows))
	for _, r := range rows {
		out = append(out, TrendingTokenOut{
			ID:             r.ID,
			Symbol:         r.Symbol,
			Name:           r.Name,
			Address:        r.Address,
			PriceChange24h: derefOrZero(r.PriceChange24h),
			Volume24h:      derefOrZero(r.Volume24h),
			MarketCap:      derefOrZero(r.MarketCap),
			Badges:         buildBadges(r.IsOfficial, r.Status),
		})
	}
	return out
}

func buildBadges(isOfficial bool, status string) []string {
	first := "community"
	if isOfficial {
		first = "verified"
	}
	return []string{first, lowercase(status)}
}

func lowercase(s string) string {
	// Avoid importing strings just for ToLower of a known short word.
	// Status values are short uppercase enums (LAUNCHED, PENDING, etc.).
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out[i] = c
	}
	return string(out)
}

func mapEarnProducts(rows []staking.PoolView) []EarnProductOut {
	out := make([]EarnProductOut, 0, 4)
	for i, p := range rows {
		if i >= 4 {
			break
		}
		out = append(out, EarnProductOut{
			ID:      p.ID,
			Name:    p.Name,
			Type:    p.PoolType,
			APY:     derefOrZero(p.APY),
			TVL:     derefOrZero(p.TVL),
			ChainID: p.ChainID,
			Status:  p.Status,
		})
	}
	return out
}

func mapMarketPulse(rows []markets.MarketFullView) []MarketPulseEntry {
	out := make([]MarketPulseEntry, 0, 5)
	for i, m := range rows {
		if i >= 5 {
			break
		}
		out = append(out, MarketPulseEntry{
			Symbol:      m.Symbol,
			ChainID:     m.ChainID,
			Volume24h:   m.Volume24h,
			FundingRate: m.FundingRate,
			OpenInterest: MarketOpenInterestOut{
				Long:  m.LongOpenInterest,
				Short: m.ShortOpenInterest,
			},
		})
	}
	return out
}

func buildRiskMarkers(stats web3events.Stats) []RiskMarker {
	return []RiskMarker{
		{ID: "events", Label: "Indexed events", Value: stats.TotalEvents},
		{ID: "block", Label: "Latest indexed block", Value: stats.IndexerBlock},
	}
}

func derefOrZero(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

// Router exposes the discover endpoints on a chi sub-router.
func Router(svc *Service) chi.Router {
	r := chi.NewRouter()
	r.Get("/home", makeHomeHandler(svc))
	return r
}

func makeHomeHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		chainID := parseChainIDQuery(r.URL.Query().Get("chainId"))
		home, err := svc.GetHome(r.Context(), chainID)
		if err != nil {
			slog.ErrorContext(r.Context(), "discover/home failed", "err", err)
			http.Error(w, "failed to load discover home", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(w).Encode(home); err != nil {
			slog.Error("discover response encode failed", "err", err)
		}
	}
}

// parseChainIDQuery matches the parseChainIDQuery in markets package
// (kept local to avoid the import cycle since markets depends on neither
// discover nor a shared http helper). Same semantics: empty / non-numeric
// / 0 → nil; positive int → pointer.
func parseChainIDQuery(raw string) *int {
	if raw == "" {
		return nil
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil {
		return nil
	}
	if parsed == 0 {
		return nil
	}
	return &parsed
}
