// Package httpx wires the chi router, health endpoints, and the
// /metrics handler. Routes for migrated modules will be mounted here as Phase
// 1+ ports land.
package httpx

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/activity"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/ai"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/article"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/bridge"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/contractconfig"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/deployments"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/discover"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/earn"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/i18n"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/indexeradmin"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/markets"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/media"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/mobilebff"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/mobilecontrol"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/papertrade"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/portfolio"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/pricefeed"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/productstatus"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/security"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/settings"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/social"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/staking"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/swap"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/sysapi"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/tags"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/token"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/trading"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/txreview"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/userauth"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/users"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/wallets"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/web3auth"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/web3events"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Deps is the explicit-dependency set for the router. Anything added here must
// be safe for concurrent use across handlers.
type Deps struct {
	Pool            *pgxpool.Pool
	MarketsLoader   markets.DeploymentsLoader
	TradingRepo     markets.TradingRepository
	TokenRepo       markets.TokenRepository
	DiscoverTokens  discover.TokenRepository
	DiscoverStaking discover.StakingRepository
	DiscoverEvents  discover.EventsRepository
	CampaignRepo    *campaign.Repository
	TokenListRepo   *token.Repository
	ActivityRepo    *activity.Repository
	EarnPools       earn.PoolLister
	EarnFinder      earn.PoolFinder
	EarnLoader      earn.DeploymentsLoader
	SettingsRepo    *settings.Repository
	SecurityRepo    *security.Repository
	SocialRepo      *social.Repository
	WalletsRepo     *wallets.Repository
	SwapLoader      swap.DeploymentsLoader
	SwapRPC         swap.EthCaller
	// BridgeRPCs is one RPC client per chain that has a deployed gateway.
	// A chain absent here still appears in the registry, but its route state
	// reads as "unavailable" rather than being guessed.
	BridgeRPCs          map[int]bridge.EthCaller
	AuthVerifier        *auth.Verifier
	TradingRepoConcrete *trading.Repository
	TradingMarkets      trading.MarketsProvider
	Web3EventsRepo      *web3events.Repository
	Web3TxVerifier      web3events.TransactionVerifier
	TagsRepo            *tags.Repository
	PaperTradeRepo      *papertrade.Repository
	PaperTradeEnabled   bool
	StakingRepo         *staking.Repository
	UsersRepo           *users.Repository
	UserVerifier        *auth.UserVerifier
	CsrfSigner          *auth.CsrfSigner
	ArticleRepo         *article.Repository
	Media               *media.Service
	ContractConfig      *contractconfig.Service
	TxReview            *txreview.Service
	AIExplain           *ai.Service
	Web3Auth            *web3auth.Service
	PortfolioDeps       portfolio.Deps
	IndexerAdmin        *indexeradmin.Service
	MobileControl       *mobilecontrol.Service
	MobileBFF           *mobilebff.Service
	// Product-diagnostics inputs (GET /api/status/product). RealtimeEnabled and
	// MediaMode are runtime flags the status surface reports; the rest is reused
	// from Pool + MarketsLoader + PaperTradeEnabled above.
	RealtimeEnabled bool
	MediaMode       string
}

// bffSessionAdapter wraps the mobile control plane as mobilebff.SessionProvider.
type bffSessionAdapter struct{ mc *mobilecontrol.Service }

func (a bffSessionAdapter) MobileSession(r *http.Request) mobilebff.Session {
	sc := a.mc.SessionContextFor(r)
	uid, appVer := "", ""
	if sc.UserID != nil {
		uid = *sc.UserID
	}
	if sc.AppVersion != nil {
		appVer = *sc.AppVersion
	}
	return mobilebff.Session{IsLoggedIn: sc.IsLoggedIn, UserID: uid, Locale: sc.Locale,
		Theme: sc.Theme, Container: sc.Container, AppVersion: appVer}
}

// bffCampaignAdapter wraps the Go campaign service as mobilebff.CampaignSource.
type bffCampaignAdapter struct{ svc *campaign.Service }

func (a bffCampaignAdapter) ActiveCampaigns(ctx context.Context) []mobilebff.CampaignInfo {
	rows, err := a.svc.Active(ctx, time.Now())
	if err != nil {
		return nil
	}
	out := make([]mobilebff.CampaignInfo, 0, len(rows))
	for _, c := range rows {
		out = append(out, toBffCampaign(c))
	}
	return out
}

func (a bffCampaignAdapter) FindCampaign(ctx context.Context, id string) (mobilebff.CampaignInfo, bool) {
	c, err := a.svc.FindByID(ctx, id)
	if err != nil {
		return mobilebff.CampaignInfo{}, false
	}
	return toBffCampaign(c), true
}

func toBffCampaign(c campaign.Campaign) mobilebff.CampaignInfo {
	info := mobilebff.CampaignInfo{ID: c.ID, Title: c.Title}
	if c.Description != nil {
		info.Description = *c.Description
	}
	if c.Banner != nil {
		info.Banner = *c.Banner
	}
	return info
}

// bffSettingsAdapter wraps the Go settings service as mobilebff.SettingsSource.
type bffSettingsAdapter struct{ svc *settings.Service }

func (a bffSettingsAdapter) NotificationsEnabled(ctx context.Context, owner string) bool {
	p, err := a.svc.GetPreferences(ctx, owner)
	if err != nil {
		return false
	}
	return p.NotificationsEnabled
}

func (a bffSettingsAdapter) MarketAlerts(ctx context.Context, owner string) bool {
	n, err := a.svc.GetNotificationPreferences(ctx, owner)
	if err != nil {
		return false
	}
	return n.MarketAlerts
}

// mobileSessionValidator adapts the user access verifier to mobilecontrol's
// TokenValidator so the control plane can report session login state. A nil
// verifier (no JWT_SECRET) makes every request report logged-out.
type mobileSessionValidator struct{ v *auth.UserVerifier }

func (m mobileSessionValidator) ValidateToken(_ context.Context, token string) (string, bool) {
	if m.v == nil {
		return "", false
	}
	claims, err := m.v.VerifyAccess(token)
	if err != nil {
		return "", false
	}
	return claims.Sub, true
}

// httpRequestsTotal mirrors the NestJS metrics module's counter shape so the
// existing Prometheus scrape config can ingest both services without forking.
var httpRequestsTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Total number of HTTP requests handled, partitioned by method and status.",
	},
	[]string{"method", "status"},
)

var httpRequestDuration = promauto.NewHistogramVec(
	prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "Latency of HTTP requests in seconds, partitioned by method.",
		Buckets: prometheus.DefBuckets,
	},
	[]string{"method"},
)

// registrySnapshot adapts a markets.DeploymentsLoader to the productstatus
// registry provider: a fresh, nil-safe snapshot of the per-chain deployment
// config. A load error yields nil (the status surface degrades, not panics).
// bridgeRPCChainSet reduces the per-chain RPC clients to the set of chain ids
// this process can actually read, which is what makes bridge delivery readiness
// a reportable fact rather than an assumption.
func bridgeRPCChainSet(clients map[int]bridge.EthCaller) map[int]bool {
	out := make(map[int]bool, len(clients))
	for chainID, c := range clients {
		if c != nil {
			out[chainID] = true
		}
	}
	return out
}

func registrySnapshot(loader markets.DeploymentsLoader) func() map[int]deployments.ChainConfig {
	return func() map[int]deployments.ChainConfig {
		if loader == nil {
			return nil
		}
		m, err := loader.Load()
		if err != nil {
			return nil
		}
		return m
	}
}

// NewRouter builds the top-level chi router. All business modules mount under
// the public /api prefix (the sole backend since NestJS was retired 2026-07-08).
func NewRouter(deps Deps) *chi.Mux {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(corsMiddleware(resolveAllowedOrigins()))
	r.Use(metricsMiddleware)

	r.Get("/healthz", livenessHandler)
	r.Get("/readyz", readinessHandler(deps.Pool))
	r.Handle("/metrics", promhttp.Handler())

	// Mount business routes under the same /api prefix NestJS uses
	// (legacy NestJS bootstrap/bootstrap-app.ts setGlobalPrefix('api')).
	// Matching the URL contract end-to-end means upstream nginx can route
	// /api/markets/* to Go without path rewriting.
	r.Route("/api", func(api chi.Router) {
		var marketsSvc *markets.Service
		if deps.MarketsLoader != nil {
			marketsSvc = markets.NewService(deps.MarketsLoader, deps.TradingRepo, deps.TokenRepo)
			api.Mount("/markets", markets.Router(marketsSvc))
		}
		// Discover needs the markets service for marketPulse aggregation.
		// Mount only when markets is wired — otherwise the response would
		// always degrade and the endpoint is pointless.
		if marketsSvc != nil {
			api.Mount("/discover", discover.Router(discover.NewService(
				marketsSvc,
				deps.DiscoverTokens,
				deps.DiscoverStaking,
				deps.DiscoverEvents,
			)))
		}
		// guardedAuth is shared by every module's guarded mutations.
		// Built once; nil when JWT_SECRET wasn't configured (cmd/api
		// warns then). Modules treat nil as "no guarded routes mount"
		// — except token's create handler also has a defense-in-depth
		// 401 fallback that fires if a nil middleware ever did mount.
		var guardedAuth func(http.Handler) http.Handler
		if deps.AuthVerifier != nil {
			guardedAuth = auth.Middleware(deps.AuthVerifier)
		}
		// optionalWalletResolver verifies a bearer web3 token when present but
		// never rejects — public reads (campaign list, follow-state) use it to
		// enrich responses with the caller's state.
		var optionalWalletResolver func(r *http.Request) string
		if deps.AuthVerifier != nil {
			verifier := deps.AuthVerifier
			optionalWalletResolver = func(r *http.Request) string {
				header := r.Header.Get("Authorization")
				const prefix = "Bearer "
				if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
					return ""
				}
				claims, err := verifier.Verify(strings.TrimSpace(header[len(prefix):]))
				if err != nil {
					return ""
				}
				// Web3 tokens carry the wallet address in `sub` (see
				// auth.Middleware, which stores claims.Sub in the context).
				return strings.ToLower(claims.Sub)
			}
		}
		// Campaign mounts unconditionally — reads degrade to empty lists / 404
		// when the pool is nil; join/reminder mutations are SIWE-guarded and
		// backed by campaign_participants / campaign_reminders rows.
		api.Mount("/campaign", campaign.Router(
			campaign.NewService(deps.CampaignRepo),
			campaign.WalletResolver(optionalWalletResolver),
			guardedAuth,
		))
		// Profile follow graph: public counts/state + SIWE-guarded
		// follow/unfollow on durable profile_follows rows. A missing repo
		// degrades to the nil-pool contract instead of a nil deref.
		socialRepo := deps.SocialRepo
		if socialRepo == nil {
			socialRepo = social.NewRepository(deps.Pool)
		}
		api.Mount("/profile", social.Router(
			social.NewService(socialRepo),
			social.WalletResolver(optionalWalletResolver),
			guardedAuth,
		))
		// accessGuard is the user access-token middleware — parity with the
		// deployed NestJS global JwtAuthGuard, which guards every route that
		// carries no @Public. It guards the modules that are entirely non-public
		// in Web3AppModule (staking/swap/earn/bridge/paper-orders). nil when
		// JWT_SECRET isn't configured (dev), in which case those modules mount
		// open exactly as before. See docs/migration/backend-go/guard-parity.md.
		// accessUserChecker rejects access tokens for deleted/disabled users
		// (guard-parity.md addendum). Typed-nil-safe: only a non-nil concrete
		// repo becomes a non-nil interface, so a missing user store skips the
		// lookup (pure-JWT, as before).
		var accessUserChecker userauth.AccessUserChecker
		if deps.UsersRepo != nil {
			accessUserChecker = deps.UsersRepo
		}
		var accessGuard func(http.Handler) http.Handler
		var stakingAdminGuard func(http.Handler) http.Handler
		if deps.UserVerifier != nil {
			accessGuard = userauth.AccessMiddleware(deps.UserVerifier, accessUserChecker)
			stakingAdminGuard = userauth.RequireRoles(deps.UserVerifier, accessUserChecker, "admin")
		}
		// mountGuarded mounts a sub-router under an optional middleware. A nil
		// guard mounts it open (dev without the relevant secret), matching the
		// prior behavior so local dev is unchanged.
		mountGuarded := func(guard func(http.Handler) http.Handler, pattern string, h http.Handler) {
			if guard == nil {
				api.Mount(pattern, h)
				return
			}
			api.Group(func(r chi.Router) {
				r.Use(guard)
				r.Mount(pattern, h)
			})
		}
		// token: POST create stays web3-guarded (guardedAuth); the GET reads are
		// Go-canonical PUBLIC market data — an intentional departure from the
		// deployed NestJS global JwtAuthGuard, which 401s them (the FE loads
		// token detail/trending/sidebar with plain fetch + no auth). Same pattern
		// as earn /products. See cutover/token.md + guard-parity.md.
		api.Mount("/token", token.Router(token.NewService(deps.TokenListRepo), guardedAuth))
		// Activity: NestJS @Public + Web3AuthGuard (web3 token), so guard the
		// whole mount with the web3 verifier.
		mountGuarded(guardedAuth, "/activity", activity.Router(activity.NewService(deps.ActivityRepo)))
		// Earn: GET /products is PUBLIC (product catalog; the FE EarnPage loads
		// it pre-sign-in) — an intentional Go-canonical departure from the NestJS
		// global JwtAuthGuard, which 401s it. build-deposit/build-withdraw stay
		// access-guarded inside earn.Router (accessGuard) and are NOT routed to Go
		// yet. See docs/migration/backend-go/cutover/earn-products.md.
		api.Mount("/earn", earn.Router(earn.NewServiceWithBuilder(deps.EarnPools, deps.EarnFinder, deps.EarnLoader), accessGuard))
		// Settings: the whole module is web3-guarded + owner-pinned (parity
		// with NestJS class-level @Public + Web3AuthGuard). Reads were public
		// before — now guarded via auth.ResolveOwner inside the handlers (no
		// ?ownerAddress= → JWT wallet; mismatch → 403). The guarded read group
		// and the PUT/POST/DELETE writes both need guardedAuth (JWT_SECRET).
		api.Mount("/settings", settings.Router(settings.NewService(deps.SettingsRepo), guardedAuth))
		// Security: reads degrade to []; revoke/build-tx is pure ABI;
		// POST/DELETE /connected-sites mount under guardedAuth.
		// tx-review (Phase 5d.2) folds into the same /security mount via
		// the extraGuardedRoutes registrar — chi rejects two Mount() calls
		// on the same prefix, so we fan the tx-review handler in here.
		var extras []func(r chi.Router)
		if deps.TxReview != nil {
			extras = append(extras, txreview.RegisterGuarded(deps.TxReview))
		}
		// /tx-review/explain rides the same mount and the same guard. It is
		// registered even when the AI layer is disabled: the endpoint then
		// answers 200 with source="static", which is the contract — a client
		// that gets a 404 can't tell "off" from "broken".
		if deps.AIExplain != nil {
			extras = append(extras, ai.RegisterGuarded(deps.AIExplain))
		}
		api.Mount("/security", security.Router(security.NewService(deps.SecurityRepo), guardedAuth, extras...))
		// Bridge: pure simulator (no DB, no on-chain calls; every response is
		// deterministic and carries executionMode "simulated"). The NestJS
		// BridgeController has no @Public, so the global JwtAuthGuard 401s it —
		// but the FE bridge panel calls /bridge/routes via fetchApi, which sends
		// a web3-token-or-nothing, and the access guard rejects both. The route
		// is therefore broken pre-login on NestJS. Since the data is
		// non-user-private (simulated route quotes, no secrets, no per-viewer
		// data), Go serves it PUBLIC — the same Go-canonical departure as
		// earn /products and the token GET reads. See cutover/bridge.md +
		// guard-parity.md.
		// Bridge: REAL routes read from the deployed BridgeGateway contracts and
		// REAL transfer status from the bridge_transfers projection (it used to
		// be a simulated route engine plus a constant fake-progress fixture).
		// Reads and the calldata builder are PUBLIC — route availability and
		// transfer state are public chain data, and build-deposit returns
		// UNSIGNED calldata the user's own wallet must sign, exactly like the
		// /api/swap builders. Nothing here holds a key or moves funds.
		//
		// A chain appears here only when it has a `bridgeGateway` deployment
		// entry AND an RPC client, so adding the counterpart chain later is a
		// deploy + config step with no code change.
		api.Mount("/bridge", bridge.Router(bridge.NewService(
			bridge.NewRegistry(registrySnapshot(deps.MarketsLoader)(), deps.BridgeRPCs),
			bridge.NewStore(deps.Pool),
			nil,
		)))
		// Server-owned i18n: public catalog reads (ETag-cached) + SIWE-admin
		// draft/publish/rollback/audit management. PostgreSQL revisions are
		// the runtime source of truth; the embedded Go baseline serves
		// fresh/degraded/pool-less runs (source "embedded-baseline"), so the
		// mount is unconditional — the store is nil-pool-safe.
		api.Mount("/i18n", i18n.Router(i18n.NewService(i18n.NewPgStore(deps.Pool)), guardedAuth))
		// Product diagnostics: GET /api/status/product — read-only, no secrets.
		// Explains empty/disabled states (DB, indexer, realtime, swap router,
		// media, bridge, scenario-data counts). Public: non-user data.
		api.Mount("/status", productstatus.Router(productstatus.NewService(productstatus.Deps{
			Pool:              deps.Pool,
			Registry:          registrySnapshot(deps.MarketsLoader),
			RealtimeEnabled:   deps.RealtimeEnabled,
			PaperTradeEnabled: deps.PaperTradeEnabled,
			MediaMode:         deps.MediaMode,
			AIExplainEnabled:  deps.AIExplain.Enabled(),
			BridgeRPCChains:   bridgeRPCChainSet(deps.BridgeRPCs),
		})))
		// Prices: real reference price series (market OHLCV + on-chain oracle
		// rounds) persisted by cmd/pricefeed. PUBLIC and DB-only — the /trade
		// chart loads it pre-login, and the read path never calls an upstream
		// venue or RPC node, so a blocked/rate-limited provider can never make
		// a page render slowly or fail. Every response states its source; a
		// source with no rows returns status "empty", never a substitute.
		api.Mount("/prices", pricefeed.Router(
			pricefeed.NewService(pricefeed.NewStore(deps.Pool), pricefeed.NewGateioProvider("", nil, 0).ID(), nil),
		))
		// Swap: Go-canonical PUBLIC, stateless — quote reads + pure ABI
		// tx-builders (no DB, no signing, no submission). The NestJS
		// SwapController has no @Public so the global JwtAuthGuard 401s all
		// three POSTs, but the FE swap panel calls them via fetchApi
		// (web3-token-or-none) which the access guard rejects → broken
		// pre-login. The data is non-user-private (quote estimates + calldata
		// the client signs later), so Go serves it PUBLIC — the same
		// Go-canonical departure as earn/products, token, and bridge. quote
		// degrades to the fallback shape when no router/RPC; build-approve with
		// an explicit spender is pure ABI (no router needed); build-swap needs a
		// Router deployment for the chain (404 otherwise). A live RPC client
		// (Phase 5d.1) opts quote into the on-chain getAmountsOut + getReserves
		// path. See cutover/swap.md + guard-parity.md.
		swapSvc := swap.NewService(deps.SwapLoader)
		if deps.SwapRPC != nil {
			swapSvc.SetRPCClient(deps.SwapRPC)
		}
		api.Mount("/swap", swap.Router(swapSvc))
		// Media: shared upload/pinning for Create Token + NFT Studio.
		// Mutations sit behind the SIWE web3 guard (same gate as token
		// creation); GET /files + /status are public. A nil service mounts
		// disabled so uploads answer 503 with the config hint, never 404.
		mediaSvc := deps.Media
		if mediaSvc == nil {
			mediaSvc = media.NewService(nil)
		}
		api.Mount("/media", media.Router(mediaSvc, guardedAuth))
		// Wallets: the whole module is web3-guarded + owner-pinned (parity
		// with NestJS class-level @Public + Web3AuthGuard). Reads (/ and
		// /manager) were public before — now guarded via auth.ResolveOwner
		// inside the handlers (no ?address=/?ownerAddress= → JWT wallet;
		// mismatch → 403). POST /watch-only, POST /groups, PUT
		// /:walletId/profile mount only when guardedAuth is wired.
		api.Mount("/wallets", wallets.Router(wallets.NewService(deps.WalletsRepo), guardedAuth))
		// Portfolio: summary/assets are @Public + Web3AuthGuard; asset detail
		// has no @Public and stays behind the user access guard.
		api.Mount("/portfolio", portfolio.Router(portfolio.NewServiceFromDeps(deps.PortfolioDeps), guardedAuth, accessGuard))
		// Trading: all 8 GETs read from perp_positions / perp_trades /
		// perp_orders. /markets delegates to the markets provider so
		// the snapshot shape stays canonical across /api/markets and
		// /api/trading/markets. nil concrete repo → service runs in
		// degraded mode and returns [] from every endpoint.
		// trading: market-wide reads are public; positions/orders/history are
		// wallet-private and use the product's SIWE identity. The handlers also
		// owner-pin the path account to the verified wallet to prevent IDOR reads.
		api.Mount("/trading", trading.Router(trading.NewService(deps.TradingRepoConcrete, deps.TradingMarkets), guardedAuth))
		// Web3 events: 5 read endpoints. nil repo → degraded responses
		// (empty lists, "0" stat counters). The 2 mutation routes
		// (POST /transactions[, /:txHash/receipt]) need SIWE auth and
		// land alongside Phase 5b.
		var web3eventsReader web3events.Reader
		web3svc := web3events.NewService(web3eventsReader)
		if deps.Web3EventsRepo != nil {
			web3eventsReader = deps.Web3EventsRepo
			// Enable the SIWE-guarded tx-reporting writes (POST /transactions
			// [/receipt]) — the FE depends on them (events.ts). guardedAuth is the
			// web3 middleware matching NestJS @UseGuards(Web3AuthGuard).
			web3svc = web3events.NewService(web3eventsReader).WithWrites(
				deps.Web3EventsRepo,
				guardedAuth,
				deps.Web3TxVerifier,
			)
		}
		api.Mount("/web3-events", web3events.Router(web3svc))
		// Tags: 3 GETs. nil repo → [] for list, 503 for detail.
		var tagsReader tags.Reader
		if deps.TagsRepo != nil {
			tagsReader = deps.TagsRepo
		}
		api.Mount("/tags", tags.Router(tags.NewService(tagsReader)))
		// Paper-trade: dev/admin-only fake order seeder. Only mounts live
		// when PAPER_TRADE_ENABLED=1 (cmd/api passes the flag); otherwise
		// every route answers an explicit 503 — fake orders must never be
		// creatable in production.
		if deps.PaperTradeEnabled {
			var paperStore papertrade.Store
			if deps.PaperTradeRepo != nil {
				paperStore = deps.PaperTradeRepo
			}
			mountGuarded(accessGuard, "/paper-orders", papertrade.Router(papertrade.NewService(paperStore)))
		} else {
			api.Mount("/paper-orders", papertrade.DisabledRouter())
		}
		// Staking: 8 routes (pool reads/stats/detail, admin create/update,
		// user stakes, record stake/unstake). Reads degrade to []; writes
		// return 503 when the pool is unavailable.
		var stakingStore staking.Store
		if deps.StakingRepo != nil {
			stakingStore = deps.StakingRepo
		}
		api.Mount("/staking", staking.Router(
			staking.NewService(stakingStore),
			stakingAdminGuard,
			guardedAuth,
		))
		// /api/health + /api/health/db + /api/metrics + /api/metrics/custom
		// mirror NestJS health/metrics modules. Root /healthz, /readyz,
		// /metrics keep working — the /api aliases let nginx route the
		// versioned paths to Go without touching scraper configs.
		api.Mount("/", sysapi.Router(sysapi.PingerFromPool(deps.Pool)))
		// /api/auth combines user/password auth routes with the SIWE aliases
		// NestJS exposes through SiweAuthController:
		//   POST /auth/nonce, POST /auth/verify, GET /auth/me.
		// User-auth mounts only when its verifier is configured; SIWE nonce
		// and verify stay public, while /me still requires a valid web3 JWT.
		authRouter := chi.NewRouter()
		authMounted := false
		if deps.UserVerifier != nil {
			authSvc := userauth.NewService(deps.UsersRepo, deps.UserVerifier, deps.CsrfSigner)
			// accessGuard is built once above (non-nil here, same condition).
			refreshGuard := userauth.RefreshMiddleware(deps.UserVerifier)
			userauth.RegisterRoutes(authRouter, authSvc, accessGuard, refreshGuard)
			authMounted = true
		}
		if deps.Web3Auth != nil {
			web3auth.RegisterAuthAliases(authRouter, deps.Web3Auth, deps.AuthVerifier)
			authMounted = true
		}
		if authMounted {
			api.Mount("/auth", authRouter)
		}
		// Articles: 2 public reads (list, detail). Mutations
		// (POST/PATCH/DELETE/publish/unpublish) need the RolesGuard
		// parity middleware and land in Phase 4v.2.
		var articleReader article.Reader
		var articleWriter article.Writer
		if deps.ArticleRepo != nil {
			articleReader = deps.ArticleRepo
			articleWriter = deps.ArticleRepo
		}
		var articleRolesGuard func(http.Handler) http.Handler
		if deps.UserVerifier != nil {
			articleRolesGuard = userauth.RequireRoles(deps.UserVerifier, accessUserChecker, "admin", "editor")
		}
		api.Mount("/articles", article.Router(article.NewService(articleReader, articleWriter), articleRolesGuard))
		// Users: GET /:id is public; GET /me, PATCH /me, DELETE /me
		// only mount when the access middleware is wired (JWT_SECRET).
		var userStore users.Store
		if deps.UsersRepo != nil {
			userStore = deps.UsersRepo
		}
		var accessGuardForUsers func(http.Handler) http.Handler
		if deps.UserVerifier != nil {
			accessGuardForUsers = userauth.AccessMiddleware(deps.UserVerifier, accessUserChecker)
		}
		api.Mount("/users", users.Router(userStore, accessGuardForUsers))
		// Contract-config: 3 public GETs (config, by-chain, by-name).
		// Service falls back to empty chains/abis when the loader or
		// ABIs dir aren't wired.
		if deps.ContractConfig != nil {
			api.Mount("/contracts", contractconfig.Router(deps.ContractConfig))
		}
		// Web3-auth (SIWE): /api/web3-auth/{nonce,verify,me}. nonce/verify
		// are public; /me decodes the existing web3 JWT (auth.Verifier).
		if deps.Web3Auth != nil {
			api.Mount("/web3-auth", web3auth.Router(deps.Web3Auth, deps.AuthVerifier))
		}
		// Mobile control plane (mobile-control.controller): runtime bootstrap,
		// session context, route resolve, capability check, web bootstrap,
		// analytics ingest. All @Public; session login state comes from the user
		// access verifier (nil → logged-out). Shares the /api/mobile prefix.
		var mcValidator mobilecontrol.TokenValidator
		if deps.UserVerifier != nil {
			mcValidator = mobileSessionValidator{v: deps.UserVerifier}
		}
		mobileControlSvc := deps.MobileControl
		if mobileControlSvc == nil {
			mobileControlSvc = mobilecontrol.NewService(mcValidator)
		}
		// Mobile-bff aggregation (mobile-bff.controller): the 13 feed/article/
		// comment/search/message/profile/settings/topic/video/share routes. All
		// @Public; comment/create + message/* read the session (401 when
		// logged-out). Reuses the Go article/tags/users repos + comments/
		// conversations/messages read-models (internal/mobilebff), the campaign +
		// settings services, and the mobile control plane for session state.
		bffSvc := deps.MobileBFF
		if bffSvc == nil {
			bffSvc = mobilebff.NewService(
				mobilebff.NewRepository(deps.Pool),
				bffSessionAdapter{mc: mobileControlSvc},
				bffCampaignAdapter{svc: campaign.NewService(deps.CampaignRepo)},
				bffSettingsAdapter{svc: settings.NewService(deps.SettingsRepo)},
			)
		}
		api.Route("/mobile", func(m chi.Router) {
			mobilecontrol.Register(m, mobileControlSvc)
			mobilebff.Register(m, bffSvc)
		})
		// Indexer HTTP access is monitoring-only; worker control and repair stay
		// on internal process boundaries.
		indexerAdmin := deps.IndexerAdmin
		if indexerAdmin == nil {
			transport := "http"
			if os.Getenv("SEPOLIA_WSS_RPC") != "" {
				transport = "wss"
			}
			// DefaultChainID 11155111 (Sepolia) mirrors indexer.DefaultChainID.
			indexerAdmin = indexeradmin.NewService(
				indexeradmin.NewRepository(deps.Pool),
				indexeradmin.Config{ChainID: 11155111, WatchTransport: transport},
			)
		}
		api.Mount("/indexer", indexeradmin.Router(indexerAdmin))
	})

	// Full-takeover identity (final wave): once nginx points the generic
	// /api/ fallback at Go, every path NestJS used to answer lands here —
	// including paths no module registers. chi's default text/plain 404 is
	// indistinguishable from an infra 404, and the NestJS envelope
	// {code,message,data} is exactly the signature the cutover smokes must
	// reject. Registering these AFTER all mounts propagates them recursively
	// to every sub-router whose handler is still nil (chi v5 NotFound()/
	// MethodNotAllowed() walk subroutes), so unknown paths at ANY depth get
	// the Go-owned JSON body. The `source: "api-go"` field is the identity
	// witness asserted by scripts/strict/production-cutover-stability-smoke.sh's
	// built-in 404 probe — do not remove it.
	r.NotFound(goOwnedNotFound)
	r.MethodNotAllowed(goOwnedMethodNotAllowed)

	return r
}

// goOwnedNotFound / goOwnedMethodNotAllowed answer unmatched routes with a
// JSON body that proves Go (not the retired NestJS fallback) served the
// request. Shape is deliberately the guard-style {statusCode,message} — never
// the NestJS {code,message,data} envelope — plus the source marker.
func goOwnedNotFound(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusNotFound, map[string]any{
		"statusCode": http.StatusNotFound,
		"message":    "route not found",
		"source":     "api-go",
	})
}

func goOwnedMethodNotAllowed(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusMethodNotAllowed, map[string]any{
		"statusCode": http.StatusMethodNotAllowed,
		"message":    "method not allowed",
		"source":     "api-go",
	})
}

func livenessHandler(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func readinessHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		if pool == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"status": "degraded",
				"reason": "database pool not initialized",
			})
			return
		}
		if err := pool.Ping(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"status": "degraded",
				"reason": "database ping failed",
				"error":  err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	}
}

func metricsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		duration := time.Since(start).Seconds()

		httpRequestDuration.WithLabelValues(r.Method).Observe(duration)
		httpRequestsTotal.WithLabelValues(r.Method, statusBucket(ww.Status())).Inc()
	})
}

func statusBucket(code int) string {
	switch {
	case code >= 500:
		return "5xx"
	case code >= 400:
		return "4xx"
	case code >= 300:
		return "3xx"
	case code >= 200:
		return "2xx"
	default:
		return "1xx"
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
