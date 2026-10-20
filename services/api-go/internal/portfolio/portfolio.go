// Package portfolio is the Go port of legacy NestJS portfolio/
// portfolio.service.ts.
//
// Inventory and summary expose indexed protocol records, their coverage and
// valuation availability. They are not a complete wallet balance service.
package portfolio

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/activity"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/markets"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/security"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/settings"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/staking"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/token"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/trading"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/wallets"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/web3events"
	"github.com/go-chi/chi/v5"
	"golang.org/x/sync/errgroup"
)

var addressRE = regexp.MustCompile(`^0x[a-fA-F0-9]{40}$`)

// AssetDetail is the GET /api/portfolio/assets/:address response.
// Field shape matches the NestJS getAssetDetail return — recentTrades
// and holders are limited to 10 each, matching the NestJS call sites.
type AssetDetail struct {
	ID             string         `json:"id"`
	Address        *string        `json:"address"`
	Symbol         string         `json:"symbol"`
	Name           string         `json:"name"`
	Description    *string        `json:"description"`
	ChainID        int            `json:"chainId"`
	MarketCap      float64        `json:"marketCap"`
	Volume24h      float64        `json:"volume24h"`
	PriceChange24h float64        `json:"priceChange24h"`
	ApprovalCount  int64          `json:"approvalCount"`
	RecentTrades   []token.Trade  `json:"recentTrades"`
	Holders        []token.Holder `json:"holders"`
}

// TokenLookup is the subset of token.Service the portfolio needs. Kept
// as an interface so tests can inject a fake without spinning a real
// repo.
type TokenLookup interface {
	FindByAddress(ctx context.Context, address string) (token.Token, error)
	ListTradesForToken(ctx context.Context, tokenID string, limit int) ([]token.Trade, error)
	ListHoldersForToken(ctx context.Context, tokenID string, limit int) ([]token.Holder, error)
}

// ApprovalCounter abstracts the web3events query so the portfolio
// service doesn't depend on the concrete repo type.
type ApprovalCounter interface {
	CountByContractAndName(ctx context.Context, contractAddress, eventName string) (int64, error)
}

// StakeLister abstracts staking.Repository.ListUserStakes for the
// inventory.
type StakeLister interface {
	ListUserStakes(ctx context.Context, userAddress string) ([]staking.UserStake, error)
}

// PositionLister abstracts the perp_positions read scoped by account.
type PositionLister interface {
	ListOpenPositionsForAccount(ctx context.Context, account string, chainID *int) ([]trading.PerpPositionForAccount, error)
}

// CreatedTokenLister abstracts the tokens-by-creator query already
// available via token.Repository.ListAll.
type CreatedTokenLister interface {
	ListAll(ctx context.Context, q token.ListQuery) ([]token.Token, int, error)
}

// HoldingLister abstracts token_holders-by-owner JOINed with tokens.
type HoldingLister interface {
	ListHoldingsByOwner(ctx context.Context, ownerAddress string, chainID *int) ([]token.HoldingForOwner, error)
}

// SettingsProvider keeps visibility rules strict even when settings HTTP reads
// deliberately degrade to defaults.
type SettingsProvider interface {
	GetPortfolioVisibility(ctx context.Context, ownerAddress string, chainID *int) (settings.UserPreferences, []settings.HiddenAsset, error)
}

// WalletCounter abstracts the web3_wallets row count. Used by
// GetSummary's `trackedWallets` field.
type WalletCounter interface {
	CountWallets(ctx context.Context, chainID *int) (int, error)
}

// EventActorCounter abstracts the per-actor event count. Used by
// GetSummary's `recentActivityCount` floor.
type EventActorCounter interface {
	CountByActor(ctx context.Context, actorAddress string, chainID *int) (int64, error)
}

// TxCounter abstracts the per-from-address web3_transactions count.
// status filter may be empty (total) or "pending" (pendingTxCount).
type TxCounter interface {
	CountTransactionsByFromAddress(ctx context.Context, fromAddress string, chainID *int, status string) (int, error)
}

// AlertLister abstracts security.Service.GetAlerts. Used by
// GetSummary's `securityAlertCount`.
type AlertLister interface {
	GetAlerts(ctx context.Context, address string, chainID *int) ([]security.Alert, error)
}

// MarketSnapshotProvider abstracts the markets snapshot summary read
// (active markets count + 24h totals). markets.Service satisfies it
// via a small adapter in main.go.
type MarketSnapshotProvider interface {
	GetSnapshotSummary(ctx context.Context, chainID *int) (markets.SnapshotSummary, error)
}

// TrendingTokenLister abstracts token.Repository.ListTrendingFull —
// the top-N price-change tokens for the summary's topMovers.
type TrendingTokenLister interface {
	ListTrendingFull(ctx context.Context, chainID *int, limit int) ([]token.TrendingTokenFull, error)
}

// ErrNotFound is returned when the address doesn't resolve to a token.
var ErrNotFound = errors.New("token not found")

// Service composes token detail, strict asset inventory and summary reads.
type Service struct {
	tokens        TokenLookup
	approvals     ApprovalCounter
	stakes        StakeLister
	positions     PositionLister
	createdTokens CreatedTokenLister
	holdings      HoldingLister
	settings      SettingsProvider
	wallets       WalletCounter
	events        EventActorCounter
	transactions  TxCounter
	alerts        AlertLister
	markets       MarketSnapshotProvider
	trending      TrendingTokenLister
}

// Missing inventory sources are reported as unavailable; missing visibility
// rules fail the inventory read closed.
type Deps struct {
	Tokens        TokenLookup
	Approvals     ApprovalCounter
	Stakes        StakeLister
	Positions     PositionLister
	CreatedTokens CreatedTokenLister
	Holdings      HoldingLister
	Settings      SettingsProvider
	Wallets       WalletCounter
	Events        EventActorCounter
	Transactions  TxCounter
	Alerts        AlertLister
	Markets       MarketSnapshotProvider
	Trending      TrendingTokenLister
}

// NewService wires asset-detail reads. Inventory requires NewServiceFromDeps.
func NewService(tokens TokenLookup, approvals ApprovalCounter) *Service {
	return &Service{tokens: tokens, approvals: approvals}
}

// NewServiceWithInventory wires the dependencies GetAssets needs.
//
// Deprecated: use NewServiceFromDeps so summary + inventory share one
// wiring path. Retained for back-compat with older call sites.
func NewServiceWithInventory(
	tokens TokenLookup,
	approvals ApprovalCounter,
	stakes StakeLister,
	positions PositionLister,
	createdTokens CreatedTokenLister,
	holdings HoldingLister,
	settingsProvider SettingsProvider,
) *Service {
	return &Service{
		tokens:        tokens,
		approvals:     approvals,
		stakes:        stakes,
		positions:     positions,
		createdTokens: createdTokens,
		holdings:      holdings,
		settings:      settingsProvider,
	}
}

// NewServiceFromDeps wires the portfolio endpoints.
func NewServiceFromDeps(d Deps) *Service {
	return &Service{
		tokens:        d.Tokens,
		approvals:     d.Approvals,
		stakes:        d.Stakes,
		positions:     d.Positions,
		createdTokens: d.CreatedTokens,
		holdings:      d.Holdings,
		settings:      d.Settings,
		wallets:       d.Wallets,
		events:        d.Events,
		transactions:  d.Transactions,
		alerts:        d.Alerts,
		markets:       d.Markets,
		trending:      d.Trending,
	}
}

// GetAssetDetail returns the asset detail by case-insensitive address
// (the underlying FindByAddress handles the LOWER() comparison). 404
// on miss; other errors bubble up.
//
// Description (a free-text column not yet exposed on token.Token) is
// nil-passthrough for now — that field is null in the current data
// shape anyway and lands in a follow-up that extends the token row
// projection.
func (s *Service) GetAssetDetail(ctx context.Context, address string) (AssetDetail, error) {
	if s.tokens == nil {
		return AssetDetail{}, ErrNotFound
	}
	tok, err := s.tokens.FindByAddress(ctx, address)
	if errors.Is(err, token.ErrNotFound) {
		return AssetDetail{}, ErrNotFound
	}
	if err != nil {
		return AssetDetail{}, err
	}

	trades, err := s.tokens.ListTradesForToken(ctx, tok.ID, 10)
	if err != nil {
		return AssetDetail{}, err
	}
	holders, err := s.tokens.ListHoldersForToken(ctx, tok.ID, 10)
	if err != nil {
		return AssetDetail{}, err
	}

	var approvalCount int64
	if s.approvals != nil && tok.Address != nil {
		count, err := s.approvals.CountByContractAndName(ctx, *tok.Address, "Approval")
		if err != nil && !errors.Is(err, web3events.ErrPoolUnavailable) {
			return AssetDetail{}, err
		}
		approvalCount = count
	}

	return AssetDetail{
		ID:             tok.ID,
		Address:        tok.Address,
		Symbol:         tok.Symbol,
		Name:           tok.Name,
		Description:    nil,
		ChainID:        tok.ChainID,
		MarketCap:      parseDecimalDisplay(tok.MarketCap),
		Volume24h:      parseDecimalDisplay(tok.Volume24h),
		PriceChange24h: derefFloat(tok.PriceChange24h),
		ApprovalCount:  approvalCount,
		RecentTrades:   trades,
		Holders:        holders,
	}, nil
}

// parseDecimalDisplay converts a durable NATIVE decimal string into a
// display float for portfolio aggregation views. The durable source stays
// the NUMERIC column / decimal string — this is a leaf display conversion.
func parseDecimalDisplay(p *string) float64 {
	if p == nil {
		return 0
	}
	f, err := strconv.ParseFloat(*p, 64)
	if err != nil {
		return 0
	}
	return f
}

func derefFloat(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

// Summary retains portfolioValueUsd for compatibility; valuation describes
// the returned inventory without implying complete wallet coverage.
type Summary struct {
	ValuationVersion    int                     `json:"valuationVersion"`
	Valuation           Valuation               `json:"valuation"`
	Coverage            Coverage                `json:"coverage"`
	Address             *string                 `json:"address"`
	PortfolioValueUSD   *float64                `json:"portfolioValueUsd"`
	DayChangePct        float64                 `json:"dayChangePct"`
	TrackedWallets      int                     `json:"trackedWallets"`
	RecentActivityCount int64                   `json:"recentActivityCount"`
	PendingTxCount      int                     `json:"pendingTxCount"`
	SecurityAlertCount  int                     `json:"securityAlertCount"`
	DataCompleteness    string                  `json:"dataCompleteness"`
	AssetMix            AssetMix                `json:"assetMix"`
	AssetFilters        FilterSummary           `json:"assetFilters"`
	TopMovers           []TopMover              `json:"topMovers"`
	MarketSnapshot      markets.SnapshotSummary `json:"marketSnapshot"`
}

// AssetMix is the per-type asset count in the summary.
type AssetMix struct {
	EarnPositions int `json:"earnPositions"`
	PerpPositions int `json:"perpPositions"`
}

// TopMover is one entry in the summary's topMovers feed — a narrower
// projection of the discover/trending token shape.
type TopMover struct {
	ID             string  `json:"id"`
	Symbol         string  `json:"symbol"`
	Name           string  `json:"name"`
	Address        *string `json:"address"`
	PriceChange24h float64 `json:"priceChange24h"`
}

// Summary auxiliary counters can degrade independently. Inventory visibility
// and source errors must propagate so they cannot become an apparent zero balance.
func (s *Service) GetSummary(ctx context.Context, address string, chainID *int) (Summary, error) {
	addr, ok := normalizeIfValid(address)
	hasOwner := ok

	g, gctx := errgroup.WithContext(ctx)
	var (
		walletCount    int
		recentEvents   int64
		txCountTotal   int
		txCountPending int
		topMovers      []token.TrendingTokenFull
		snapshot       markets.SnapshotSummary
		alerts         []security.Alert
		inventory      AssetInventory
		inventoryErr   error
	)
	inventory = emptyInventory()

	// walletCount: per NestJS, 1 if an owner is set, else the total
	// from the chain-filtered table. The "1" path doesn't even hit DB.
	if hasOwner {
		walletCount = 1
	} else if s.wallets != nil {
		g.Go(func() error {
			n, err := s.wallets.CountWallets(gctx, chainID)
			if isDegradable(err) {
				return nil
			}
			if err != nil {
				return err
			}
			walletCount = n
			return nil
		})
	}

	if hasOwner && s.events != nil {
		g.Go(func() error {
			n, err := s.events.CountByActor(gctx, addr, chainID)
			if isDegradable(err) {
				return nil
			}
			if err != nil {
				return err
			}
			recentEvents = n
			return nil
		})
	}
	if hasOwner && s.transactions != nil {
		g.Go(func() error {
			n, err := s.transactions.CountTransactionsByFromAddress(gctx, addr, chainID, "")
			if isDegradable(err) {
				return nil
			}
			if err != nil {
				return err
			}
			txCountTotal = n
			return nil
		})
		g.Go(func() error {
			n, err := s.transactions.CountTransactionsByFromAddress(gctx, addr, chainID, "pending")
			if isDegradable(err) {
				return nil
			}
			if err != nil {
				return err
			}
			txCountPending = n
			return nil
		})
	}

	if s.trending != nil {
		g.Go(func() error {
			rows, err := s.trending.ListTrendingFull(gctx, chainID, 4)
			if isDegradable(err) {
				return nil
			}
			if err != nil {
				return err
			}
			topMovers = rows
			return nil
		})
	}
	if s.markets != nil {
		g.Go(func() error {
			sum, err := s.markets.GetSnapshotSummary(gctx, chainID)
			if isDegradable(err) {
				return nil
			}
			if err != nil {
				return err
			}
			snapshot = sum
			return nil
		})
	}
	if hasOwner && s.alerts != nil {
		g.Go(func() error {
			rows, err := s.alerts.GetAlerts(gctx, addr, chainID)
			if isDegradable(err) {
				return nil
			}
			if err != nil {
				return err
			}
			alerts = rows
			return nil
		})
	}
	if hasOwner {
		g.Go(func() error {
			inventory, inventoryErr = s.GetAssets(gctx, addr, chainID)
			return inventoryErr
		})
	}

	if err := g.Wait(); err != nil {
		// Clients discard cached private rows when visibility is unavailable;
		// an earlier auxiliary-counter failure must not hide that error code.
		var visibilityErr *visibilityReadError
		if errors.As(inventoryErr, &visibilityErr) {
			return Summary{}, inventoryErr
		}
		return Summary{}, err
	}

	portfolioValue := inventory.compatibleValue()
	var earnCount, perpCount int
	for _, item := range inventory.Items {
		switch item.AssetType {
		case "earn":
			earnCount++
		case "perp":
			perpCount++
		}
	}

	// dayChangePct: average priceChange24h across topMovers. NestJS
	// uses `priceChange24h || 0` to coerce nulls — match exactly.
	dayChange := 0.0
	if len(topMovers) > 0 {
		sum := 0.0
		for _, m := range topMovers {
			if m.PriceChange24h != nil {
				sum += *m.PriceChange24h
			}
		}
		dayChange = sum / float64(len(topMovers))
	}

	recentActivity := recentEvents
	if int64(txCountTotal) > recentActivity {
		recentActivity = int64(txCountTotal)
	}

	dataCompleteness := inventory.dataCompleteness()

	var addrPtr *string
	if hasOwner {
		a := addr
		addrPtr = &a
	}

	movers := make([]TopMover, 0, len(topMovers))
	for _, m := range topMovers {
		change := 0.0
		if m.PriceChange24h != nil {
			change = *m.PriceChange24h
		}
		movers = append(movers, TopMover{
			ID:             m.ID,
			Symbol:         m.Symbol,
			Name:           m.Name,
			Address:        m.Address,
			PriceChange24h: change,
		})
	}

	return Summary{
		ValuationVersion:    ValuationVersion,
		Valuation:           inventory.Valuation,
		Coverage:            inventory.Coverage,
		Address:             addrPtr,
		PortfolioValueUSD:   portfolioValue,
		DayChangePct:        dayChange,
		TrackedWallets:      walletCount,
		RecentActivityCount: recentActivity,
		PendingTxCount:      txCountPending,
		SecurityAlertCount:  len(alerts),
		DataCompleteness:    dataCompleteness,
		AssetMix:            AssetMix{EarnPositions: earnCount, PerpPositions: perpCount},
		AssetFilters:        inventory.FilterSummary,
		TopMovers:           movers,
		MarketSnapshot:      snapshot,
	}, nil
}

// isDegradable returns true when err is one of the sibling-pkg
// ErrPoolUnavailable sentinels. Callers swallow it so a single
// degraded source doesn't fail the whole request.
func isDegradable(err error) bool {
	return errors.Is(err, web3events.ErrPoolUnavailable) ||
		errors.Is(err, staking.ErrPoolUnavailable) ||
		errors.Is(err, trading.ErrPoolUnavailable) ||
		errors.Is(err, token.ErrPoolUnavailable) ||
		errors.Is(err, settings.ErrPoolUnavailable) ||
		errors.Is(err, wallets.ErrPoolUnavailable) ||
		errors.Is(err, activity.ErrPoolUnavailable)
}

func normalizeIfValid(addr string) (string, bool) {
	addr = strings.TrimSpace(addr)
	if addr == "" || !addressRE.MatchString(addr) {
		return "", false
	}
	return strings.ToLower(addr), true
}

// Router mounts the portfolio endpoints at /api/portfolio.
//   - GET /summary          (Phase 4j — web3-guarded)
//   - GET /assets           (Phase 4i — web3-guarded)
//   - GET /assets/{address} (Phase 4h — access-guarded)
//
// The split mirrors NestJS: summary/assets are @Public + Web3AuthGuard, while
// asset detail has no @Public and therefore uses the global JwtAuthGuard.
func Router(svc *Service, web3Middleware, accessMiddleware func(http.Handler) http.Handler) chi.Router {
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		if web3Middleware != nil {
			r.Use(web3Middleware)
		}
		r.Get("/summary", makeSummaryHandler(svc))
		r.Get("/assets", makeAssetsHandler(svc))
	})
	r.Group(func(r chi.Router) {
		if accessMiddleware != nil {
			r.Use(accessMiddleware)
		}
		r.Get("/assets/{address}", makeAssetDetailHandler(svc))
	})
	return r
}

func makeSummaryHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		// Mirror NestJS web3-request-owner: default to the authenticated wallet
		// when no ?address= is given, and reject a query address that doesn't
		// match it (403). Go previously used the raw query param — returning a
		// null-address summary for the authed wallet AND, worse, serving any
		// address's portfolio to any holder of a valid token (an IDOR). Found by
		// the guarded 200-path golden run (golden-run-2026-06-08.md).
		address, err := auth.ResolveOwner(auth.AddressFromContext(r.Context()), q.Get("address"))
		if err != nil {
			mapResolveOwnerErr(w, err)
			return
		}
		chainID := parseChainID(q.Get("chainId"))
		out, err := svc.GetSummary(r.Context(), address, chainID)
		if err != nil {
			slog.ErrorContext(r.Context(), "portfolio summary failed", "err", err, "address", address)
			writeInventoryError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func makeAssetsHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		address, err := auth.ResolveOwner(auth.AddressFromContext(r.Context()), q.Get("address"))
		if err != nil {
			mapResolveOwnerErr(w, err)
			return
		}
		chainID := parseChainID(q.Get("chainId"))
		out, err := svc.GetAssets(r.Context(), address, chainID)
		if err != nil {
			slog.ErrorContext(r.Context(), "portfolio assets failed", "err", err, "address", address)
			writeInventoryError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// mapResolveOwnerErr maps the auth.ResolveOwner sentinels to the same statuses
// the deployed NestJS web3-request-owner helper returns: 401 (no authenticated
// wallet), 403 (requested ≠ authenticated), 400 (malformed requested address).
func mapResolveOwnerErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrMissingAuthenticated):
		http.Error(w, err.Error(), http.StatusUnauthorized)
	case errors.Is(err, auth.ErrOwnerMismatch):
		http.Error(w, err.Error(), http.StatusForbidden)
	default:
		http.Error(w, err.Error(), http.StatusBadRequest)
	}
}

func parseChainID(raw string) *int {
	if raw == "" {
		return nil
	}
	v, err := atoiSafe(raw)
	if err != nil {
		return nil
	}
	return &v
}

// atoiSafe avoids importing strconv just for one call — same pattern
// as visibility.go's intStr helper.
func atoiSafe(s string) (int, error) {
	if s == "" {
		return 0, errors.New("empty")
	}
	negative := false
	i := 0
	if s[0] == '-' {
		negative = true
		i = 1
	}
	n := 0
	for ; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, errors.New("not a number")
		}
		n = n*10 + int(c-'0')
	}
	if negative {
		n = -n
	}
	return n, nil
}

func makeAssetDetailHandler(svc *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		address := chi.URLParam(r, "address")
		if address == "" {
			http.Error(w, "address required", http.StatusBadRequest)
			return
		}
		out, err := svc.GetAssetDetail(r.Context(), address)
		if errors.Is(err, ErrNotFound) {
			http.Error(w, "token not found", http.StatusNotFound)
			return
		}
		if err != nil {
			slog.ErrorContext(r.Context(), "portfolio asset detail failed", "err", err, "address", address)
			http.Error(w, "failed to load asset detail", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Error("portfolio response encode failed", "err", err)
	}
}
