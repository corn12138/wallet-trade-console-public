// Command api is the entrypoint for the Go API service that gradually
// replaces services/api (NestJS). Phase 0 wires only health + metrics; module
// routes land in Phase 1+ as each NestJS module is ported.
package main

import (
	"context"
	"errors"
	"log/slog"
	"math/big"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/activity"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/ai"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/article"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/bridge"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/contractconfig"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/db"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/discover"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/earn"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/eventbus"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/httpx"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/livekit"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/markets"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/media"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/papertrade"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/portfolio"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/realtime"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/rpc"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/runtimeenv"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/security"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/settings"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/social"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/staking"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/swap"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/tags"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/token"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/trading"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/txreview"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/users"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/wallets"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/web3auth"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/web3events"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{}))
	slog.SetDefault(logger)

	if err := run(); err != nil {
		slog.Error("api-go exited with error", "err", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	databaseURL, err := runtimeenv.Resolve()
	if err != nil {
		return err
	}

	pool, err := openPoolIfConfigured(ctx, databaseURL)
	if err != nil {
		return err
	}
	if pool != nil {
		defer pool.Close()
	}

	authVerifier, err := buildAuthVerifier()
	if err != nil {
		return err
	}

	// User-auth (login/register/refresh) wires its own verifier with
	// separate JWT_SECRET / JWT_REFRESH_SECRET so the user-flow tokens
	// can't be confused with the SIWE web3-auth tokens. Both fall back
	// to the same JWT_SECRET if a dedicated refresh secret isn't set —
	// matches the NestJS config tree behavior in test/dev.
	userVerifier := buildUserVerifier()
	csrfSigner := buildCsrfSigner()

	marketsLoader := markets.NewDirLoader()
	if marketsLoader.Dir() == "" {
		slog.Warn("deployments registry not found (set CONTRACTS_DEPLOYMENTS_DIR or run from a repo checkout); markets will be empty and swap quotes degrade to non-executable fallback")
	} else {
		slog.Info("markets loader configured", "deployments_dir", marketsLoader.Dir())
	}

	var (
		tradingRepo    markets.TradingRepository
		tokenRepo      markets.TokenRepository
		discoverTokens discover.TokenRepository
	)
	// token.NewRepository handles nil pool — same degraded contract as
	// campaign below. Built up-front so both the markets/discover
	// adapters and the new /api/token mount share one instance.
	tokenRepoConcrete := token.NewRepository(pool)
	// web3events.NewRepository tolerates nil pool. Built unconditionally
	// so portfolio.ApprovalCounter can hold a real (degrading) pointer
	// even before DB envs are wired.
	eventsRepoConcrete := web3events.NewRepository(pool)
	// trading.NewRepository tolerates nil pool — built unconditionally
	// so the trading service's degraded path (Phase 4s) gets a real
	// pointer with a nil pool field rather than a typed-nil interface
	// trap that crashes the first ListXxx call.
	tradingRepoConcrete := trading.NewRepository(pool)
	// Paper (txHash-less) perp orders only reach the orderbook/pending
	// read models when the dev-only seeder is explicitly enabled.
	paperTradeEnabled := papertrade.Enabled()
	tradingRepoConcrete.SetIncludePaperOrders(paperTradeEnabled)
	if paperTradeEnabled {
		slog.Warn("paper trading ENABLED (dev-only): /api/paper-orders live, paper rows visible in order reads")
	}
	// staking.NewRepository tolerates a nil pool the same way trading
	// does. Built unconditionally so the new staking HTTP routes
	// (Phase 4y) don't trip the typed-nil receiver trap.
	stakingRepoConcrete := staking.NewRepository(pool)
	if pool != nil {
		tradingRepo = tradingRepoConcrete
		tokenRepo = tokenRepoConcrete
		discoverTokens = tokenRepoConcrete
	}
	// campaign.NewRepository handles nil pool — methods return
	// ErrPoolUnavailable, which the service degrades to empty/404.
	campaignRepo := campaign.NewRepository(pool)
	socialRepo := social.NewRepository(pool)
	activityRepo := activity.NewRepository(pool)
	settingsRepo := settings.NewRepository(pool)
	securityRepo := security.NewRepository(pool)
	walletsRepo := wallets.NewRepository(pool)
	// tags + papertrade repos tolerate nil pool (degraded contract).
	tagsRepo := tags.NewRepository(pool)
	paperTradeRepo := papertrade.NewRepository(pool)
	usersRepo := users.NewRepository(pool)
	articleRepo := article.NewRepository(pool)
	livekitSvc := livekit.NewService(
		os.Getenv("LIVEKIT_API_KEY"),
		os.Getenv("LIVEKIT_API_SECRET"),
		os.Getenv("LIVEKIT_WS_URL"),
	)
	if !livekitSvc.Configured() {
		slog.Warn("LiveKit not configured; /api/livekit/token + /url will respond 400")
	}

	// Media upload/pinning (Create Token icon/banner + NFT Studio artwork
	// and metadata). Provider comes from MEDIA_STORAGE env; disabled means
	// the upload routes answer 503 with the config hint.
	mediaProvider, err := media.FromEnv()
	if err != nil {
		return err
	}
	mediaSvc := media.NewService(mediaProvider)
	mediaMode := "disabled"
	if mediaSvc.Configured() {
		mediaMode = mediaSvc.ProviderName()
		slog.Info("media uploads enabled", "provider", mediaMode)
	} else {
		slog.Warn("media uploads disabled (set MEDIA_STORAGE=local + MEDIA_LOCAL_DIR or MEDIA_STORAGE=s3 + MEDIA_S3_*)")
	}

	// contract-config loads foundry ABIs from CONTRACTS_ABIS_DIR (defaults
	// to "contracts/artifacts"). Empty / missing dir → ABIs map is empty
	// but the endpoint shape still matches NestJS.
	abisDir := os.Getenv("CONTRACTS_ABIS_DIR")
	if abisDir == "" {
		abisDir = "contracts/artifacts"
	}
	contractCfgSvc := contractconfig.NewService(marketsLoader, abisDir)

	// JSON-RPC client for swap live quotes (Phase 5d.1). Honors the
	// same env vars as the NestJS indexer config: SEPOLIA_RPC_URL or
	// SEPOLIA_HTTPS_RPC, with the public node as a safe default. When
	// unset, the swap GetQuote path stays on its fallback estimator.
	var swapRPC *rpc.Client
	if rpcURL := firstNonEmpty(os.Getenv("SEPOLIA_RPC_URL"), os.Getenv("SEPOLIA_HTTPS_RPC")); rpcURL != "" {
		swapRPC = rpc.NewClient(rpcURL, 0)
		slog.Info("swap live-quote enabled", "rpc_url", rpc.RedactURL(rpcURL))
	} else {
		slog.Info("swap live-quote disabled (SEPOLIA_RPC_URL / SEPOLIA_HTTPS_RPC unset)")
	}

	// Per-chain RPC clients for the bridge. Chain 11155111 reuses the Sepolia
	// url above; every additional chain is opt-in via BRIDGE_RPC_URL_<chainId>,
	// which is the whole extension seam — deploy a counterpart gateway, set its
	// url, and the route appears with no code change.
	bridgeRPCs := buildBridgeRPCs(swapRPC)

	// Web3-auth (SIWE). JWT_SECRET also signs the issued web3 access
	// token (Type="web3"), same secret the existing auth.Verifier
	// reads — so a token minted here verifies for guarded routes.
	web3AuthSvc := web3auth.NewServiceFromEnv(os.Getenv("JWT_SECRET"))

	// Pre-built services so portfolio.Deps can hold strongly-typed
	// providers (AlertLister, MarketSnapshotProvider) without main
	// re-creating them inside the httpx call.
	securitySvc := security.NewService(securityRepo)
	settingsSvc := settings.NewService(settingsRepo)
	tokenSvc := token.NewService(tokenRepoConcrete)
	var marketsSnapshotAdapter marketsSummaryAdapter
	var marketsSvc *markets.Service
	if marketsLoader != nil {
		marketsSvc = markets.NewService(marketsLoader, tradingRepo, tokenRepo)
		marketsSnapshotAdapter = marketsSummaryAdapter{svc: marketsSvc}
	}

	// Tx-review wiring (Phase 5d.2b): orchestrate swap + earn + token
	// intel + connected-sites + deployments registry into a single
	// txreview.Service. When swap RPC is unset, simulation still runs
	// against the swapRPCCaller(swapRPC) → nil → simulator degrades to
	// fallback mode (matches NestJS behavior).
	swapSvcForReview := swap.NewService(marketsLoader)
	if swapRPC != nil {
		swapSvcForReview.SetRPCClient(swapRPC)
	}
	earnSvcForReview := earn.NewServiceWithBuilder(earnPoolLister(stakingRepoConcrete), earnPoolFinder(stakingRepoConcrete), marketsLoader)
	// Bridge state for tx-review: the same one-time deployments snapshot the
	// /bridge mount uses, plus the transfer projection for sender history. A
	// registry load failure degrades to an empty registry — the bridge checks
	// then report the source gateway as missing, which is the honest answer.
	bridgeReviewChains, bridgeReviewErr := marketsLoader.Load()
	if bridgeReviewErr != nil {
		slog.Warn("deployments registry unreadable; bridge tx-review will report gateways as not deployed", "err", bridgeReviewErr)
	}
	bridgeReviewReader := bridgeStateReader{
		registry: bridge.NewRegistry(bridgeReviewChains, bridgeRPCs),
		store:    bridge.NewStore(pool),
	}
	txReviewSvc := txreview.NewService(
		txReviewRPCCaller(swapRPC),
		swapPreparer{svc: swapSvcForReview},
		earnPreparer{svc: earnSvcForReview},
		tokenIntelLookup{repo: tokenRepoConcrete},
		connectedSitesLookup{svc: securitySvc},
		knownSpendersLookup{loader: marketsLoader},
		bridgeReviewReader,
	)

	// AI explanation layer (TD P2). Default OFF: with AI_ENABLED unset the
	// service is built with a nil client and every /tx-review/explain call
	// answers 200 with source="static", which the frontend renders from the
	// deterministic check text. A missing key while enabled is a startup
	// WARNING, not a failure — degrading is the designed behavior, and taking
	// the API down over an optional presentation layer would invert that.
	aiCfg := ai.ConfigFromEnv()
	var aiClient ai.Client
	if aiCfg.Enabled {
		client, aiErr := ai.NewDeepSeekClient(aiCfg)
		if aiErr != nil {
			slog.Warn("AI_ENABLED is set but no client could be built; tx-review explanations stay static", "err", aiErr)
		} else {
			aiClient = client
			slog.Info("AI tx-review explanations enabled", "provider", "deepseek", "model", aiCfg.Model, "timeout", aiCfg.Timeout)
		}
	}
	aiExplainSvc := ai.NewService(aiCfg, aiClient, txReviewSvc)

	addr := listenAddr()
	server := &http.Server{
		Addr: addr,
		Handler: httpx.NewRouter(httpx.Deps{
			Pool:                pool,
			MarketsLoader:       marketsLoader,
			TradingRepo:         tradingRepo,
			TokenRepo:           tokenRepo,
			DiscoverTokens:      discoverTokens,
			DiscoverStaking:     discoverStaking(stakingRepoConcrete),
			DiscoverEvents:      discoverEvents(eventsRepoConcrete, pool),
			CampaignRepo:        campaignRepo,
			SocialRepo:          socialRepo,
			TokenListRepo:       tokenRepoConcrete,
			ActivityRepo:        activityRepo,
			EarnPools:           earnPoolLister(stakingRepoConcrete),
			EarnFinder:          earnPoolFinder(stakingRepoConcrete),
			EarnLoader:          marketsLoader,
			SettingsRepo:        settingsRepo,
			SecurityRepo:        securityRepo,
			WalletsRepo:         walletsRepo,
			SwapLoader:          marketsLoader,
			SwapRPC:             swapRPCCaller(swapRPC),
			BridgeRPCs:          bridgeRPCs,
			AuthVerifier:        authVerifier,
			TradingRepoConcrete: tradingRepoConcrete,
			TradingMarkets:      tradingMarketsAdapter{svc: marketsSvc},
			Web3EventsRepo:      eventsRepoConcrete,
			TagsRepo:            tagsRepo,
			PaperTradeRepo:      paperTradeRepo,
			PaperTradeEnabled:   paperTradeEnabled,
			StakingRepo:         stakingRepoConcrete,
			UsersRepo:           usersRepo,
			UserVerifier:        userVerifier,
			CsrfSigner:          csrfSigner,
			ArticleRepo:         articleRepo,
			Media:               mediaSvc,
			LiveKit:             livekitSvc,
			ContractConfig:      contractCfgSvc,
			TxReview:            txReviewSvc,
			AIExplain:           aiExplainSvc,
			Web3Auth:            web3AuthSvc,
			PortfolioDeps: portfolio.Deps{
				Tokens:        tokenSvc,
				Approvals:     eventsRepoConcrete,
				Stakes:        portfolioStakes(stakingRepoConcrete),
				Positions:     portfolioPositions(tradingRepoConcrete),
				CreatedTokens: tokenRepoConcrete,
				Holdings:      tokenRepoConcrete,
				Settings:      settingsSvc,
				Wallets:       walletsRepo,
				Events:        eventsRepoConcrete,
				Transactions:  activityRepo,
				Alerts:        securitySvc,
				Markets:       portfolioMarkets(marketsSnapshotAdapter),
				Trending:      tokenRepoConcrete,
			},
			RealtimeEnabled: realtimeEnabled(),
			MediaMode:       mediaMode,
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Strict zero-Nest realtime tier (ADR 0006): when enabled, serve the
	// Socket.IO-compatible transport at /socket.io/ so nginx can repoint it from
	// NestJS to Go. The market-stream driver + worker feed the gateways
	// (REDIS_URL -> cross-process; MARKET_STREAM_SYMBOLS drives the worker).
	var realtimeStop func()
	if realtimeEnabled() {
		rt, stopRealtime := buildRealtime(ctx, marketsSvc)
		server.Handler = mountRealtime(server.Handler, rt)
		realtimeStop = stopRealtime
		slog.Info("realtime Socket.IO enabled at /socket.io/ (markets + token-events)")
		// Production token-events producer feed: the indexer publishes
		// committed trade/price/graduation projections over Postgres
		// NOTIFY (internal/eventbus); this loop relays them to the
		// /token-events namespace. Without a DB pool the namespace stays
		// mounted but silent — REST polling remains the fallback.
		if pool != nil {
			go eventbus.ListenTokenEvents(ctx, pool, rt)
		} else {
			slog.Warn("token-events producer feed disabled (no database pool)")
		}
	}

	go func() {
		slog.Info("api-go listening", "addr", addr, "db", describeDB(databaseURL))
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("http server failed", "err", err)
			stop()
		}
	}()

	<-ctx.Done()
	slog.Info("api-go shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if realtimeStop != nil {
		realtimeStop()
	}
	return server.Shutdown(shutdownCtx)
}

func openPoolIfConfigured(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	if databaseURL == "" {
		// Match PrismaService's skip-on-bootstrap behavior: log loudly but do
		// not crash, so local dev without DB envs still gets a running
		// /healthz. /readyz will report degraded.
		slog.Warn("DATABASE_URL not configured; starting without database pool")
		return nil, nil
	}
	pool, err := db.Open(ctx, databaseURL)
	if err != nil {
		// In production we want a hard fail. Phase 0 doesn't yet have a
		// production-detection signal that matches NestJS NODE_ENV, so we
		// always return the error and let the caller decide. This is
		// reviewed in Phase 1 when we add config layering.
		return nil, err
	}
	return pool, nil
}

func describeDB(databaseURL string) string {
	if databaseURL == "" {
		return "<not configured>"
	}
	return runtimeenv.DescribeTarget(databaseURL)
}

// discoverStaking returns the concrete repo as an interface when the
// pool is configured, else a true nil interface. Avoids the typed-nil
// trap where a nil pointer disguised as an interface is non-nil.
func discoverStaking(r *staking.Repository) discover.StakingRepository {
	if r == nil {
		return nil
	}
	return r
}

func earnPoolLister(r *staking.Repository) earn.PoolLister {
	if r == nil {
		return nil
	}
	return r
}

func earnPoolFinder(r *staking.Repository) earn.PoolFinder {
	if r == nil {
		return nil
	}
	return r
}

// discoverEvents returns a true nil interface when the pool is nil,
// even though web3events.NewRepository now tolerates nil pool. The
// discover service still expects a real (non-nil) repo before issuing
// queries, and this matches the typed-nil-trap helper above.
func discoverEvents(r *web3events.Repository, pool *pgxpool.Pool) discover.EventsRepository {
	if pool == nil {
		return nil
	}
	return r
}

func portfolioStakes(r *staking.Repository) portfolio.StakeLister {
	if r == nil {
		return nil
	}
	return r
}

func portfolioPositions(r *trading.Repository) portfolio.PositionLister {
	if r == nil {
		return nil
	}
	return r
}

// tradingMarketsAdapter wraps markets.Service so it satisfies
// trading.MarketsProvider. Maps markets.MarketFullView to the
// trading.MarketSnapshotRow shape — same data, redeclared in trading
// to avoid an import cycle (markets already imports trading).
type tradingMarketsAdapter struct {
	svc *markets.Service
}

func (a tradingMarketsAdapter) GetMarketsFull(ctx context.Context, chainID *int) ([]trading.MarketSnapshotRow, error) {
	if a.svc == nil {
		return []trading.MarketSnapshotRow{}, nil
	}
	rows, err := a.svc.GetMarketsFull(ctx, chainID)
	if err != nil {
		return nil, err
	}
	out := make([]trading.MarketSnapshotRow, 0, len(rows))
	for _, m := range rows {
		out = append(out, trading.MarketSnapshotRow{
			Symbol:            m.Symbol,
			ChainID:           m.ChainID,
			IndexToken:        m.IndexToken,
			CollateralToken:   m.CollateralToken,
			FundingRate:       m.FundingRate,
			Volume24h:         m.Volume24h,
			LongOpenInterest:  m.LongOpenInterest,
			ShortOpenInterest: m.ShortOpenInterest,
		})
	}
	return out, nil
}

// marketsSummaryAdapter wraps markets.Service so it satisfies
// portfolio.MarketSnapshotProvider. markets.GetSnapshot returns the
// full snapshot; portfolio only needs the summary block.
type marketsSummaryAdapter struct {
	svc *markets.Service
}

func (a marketsSummaryAdapter) GetSnapshotSummary(ctx context.Context, chainID *int) (markets.SnapshotSummary, error) {
	if a.svc == nil {
		return markets.SnapshotSummary{}, nil
	}
	snap, err := a.svc.GetSnapshot(ctx, chainID)
	if err != nil {
		return markets.SnapshotSummary{}, err
	}
	return snap.Summary, nil
}

func portfolioMarkets(a marketsSummaryAdapter) portfolio.MarketSnapshotProvider {
	if a.svc == nil {
		return nil
	}
	return a
}

// buildAuthVerifier loads JWT_SECRET from the env and constructs a
// Verifier. In production the secret is mandatory — NestJS already
// crashes at boot on missing JWT_SECRET, and we mirror that contract.
// In non-production, missing secret is allowed but logged loudly, so
// guarded routes will only authenticate via the X-Wallet-Address /
// zero-address fallbacks the middleware implements.
func buildAuthVerifier() (*auth.Verifier, error) {
	secret := os.Getenv("JWT_SECRET")
	if secret == "" {
		if os.Getenv("NODE_ENV") == "production" {
			return nil, errors.New("JWT_SECRET is required in production")
		}
		slog.Warn("JWT_SECRET not set; guarded routes will only accept X-Wallet-Address / dev fallbacks")
	}
	return auth.NewVerifier(secret), nil
}

// buildUserVerifier returns the access+refresh signer for /api/auth/*.
// Returns nil when JWT_SECRET is unset — the /auth routes then refuse
// to mount, which matches the NestJS behavior in dev where bootstrap
// crashes on missing JWT_SECRET.
func buildUserVerifier() *auth.UserVerifier {
	access := os.Getenv("JWT_SECRET")
	refresh := os.Getenv("JWT_REFRESH_SECRET")
	if access == "" {
		return nil
	}
	if refresh == "" {
		refresh = access
	}
	return auth.NewUserVerifier(access, refresh, 0, 0)
}

// buildCsrfSigner reads CSRF_SECRET (or SECURITY_CSRF_SECRET as the
// NestJS config tree-equivalent). Nil → /api/auth/* still works but
// the response.csrfToken is empty and the XSRF-TOKEN cookie isn't
// set — fine in dev.
func buildCsrfSigner() *auth.CsrfSigner {
	secret := os.Getenv("CSRF_SECRET")
	if secret == "" {
		secret = os.Getenv("SECURITY_CSRF_SECRET")
	}
	if secret == "" {
		return nil
	}
	return auth.NewCsrfSigner(secret, 0)
}

// --- tx-review adapters (Phase 5d.2b) ----------------------------------

// swapPreparer adapts *swap.Service to txreview.SwapPreparer. The
// adapter translates the txreview-flavored request types into the
// swap-pkg ones (mainly: pointer-typed fields → value-typed) and
// hides time.Now from the orchestrator.
type swapPreparer struct{ svc *swap.Service }

func (s swapPreparer) GetQuote(req txreview.SwapQuoteRequest) (txreview.SwapQuote, error) {
	out, err := s.svc.GetQuote(swap.QuoteRequest{
		ChainID:          intPtr(req.ChainID),
		TokenIn:          req.TokenIn,
		TokenOut:         req.TokenOut,
		AmountIn:         req.AmountIn,
		TokenInDecimals:  req.TokenInDecimals,
		TokenOutDecimals: req.TokenOutDecimals,
		SlippageBps:      req.SlippageBps,
	})
	if err != nil {
		return txreview.SwapQuote{}, err
	}
	router := ""
	if out.RouterAddress != nil {
		router = *out.RouterAddress
	}
	return txreview.SwapQuote{
		RouterAddress:   router,
		MinimumReceived: out.MinimumReceived,
		SlippageBps:     out.SlippageBps,
		Warnings:        out.Warnings,
	}, nil
}

func (s swapPreparer) BuildSwapTx(req txreview.SwapBuildRequest) (txreview.TxShape, error) {
	out, err := s.svc.BuildSwapTx(swap.BuildSwapRequest{
		ChainID:          intPtr(req.ChainID),
		TokenIn:          req.TokenIn,
		TokenOut:         req.TokenOut,
		AmountIn:         req.AmountIn,
		AmountOutMin:     req.AmountOutMin,
		TokenInDecimals:  req.TokenInDecimals,
		TokenOutDecimals: req.TokenOutDecimals,
		Recipient:        req.Recipient,
		DeadlineSeconds:  req.DeadlineSeconds,
	}, time.Now())
	if err != nil {
		return txreview.TxShape{}, err
	}
	return txreview.TxShape{ChainID: out.ChainID, To: out.To, Value: out.Value, Data: out.Data}, nil
}

func (s swapPreparer) BuildApproveTx(req txreview.SwapApproveRequest) (txreview.TxShape, error) {
	swapReq := swap.BuildApproveRequest{
		ChainID:       intPtr(req.ChainID),
		TokenAddress:  req.TokenAddress,
		Amount:        req.Amount,
		TokenDecimals: req.TokenDecimals,
	}
	if req.Spender != "" {
		spender := req.Spender
		swapReq.Spender = &spender
	}
	out, err := s.svc.BuildApproveTx(swapReq)
	if err != nil {
		return txreview.TxShape{}, err
	}
	return txreview.TxShape{ChainID: out.ChainID, To: out.To, Value: out.Value, Data: out.Data}, nil
}

// earnPreparer adapts *earn.Service to txreview.EarnPreparer.
type earnPreparer struct{ svc *earn.Service }

func (e earnPreparer) BuildDepositTx(ctx context.Context, productID, amount string, tokenDecimals int) (txreview.TxShape, error) {
	out, err := e.svc.BuildDepositTx(ctx, earn.BuildTxRequest{ProductID: productID, Amount: amount, TokenDecimals: tokenDecimals})
	if err != nil {
		return txreview.TxShape{}, err
	}
	return txreview.TxShape{ChainID: out.ChainID, To: out.To, Value: out.Value, Data: out.Data}, nil
}

func (e earnPreparer) BuildWithdrawTx(ctx context.Context, productID, amount string, tokenDecimals int) (txreview.TxShape, error) {
	out, err := e.svc.BuildWithdrawTx(ctx, earn.BuildTxRequest{ProductID: productID, Amount: amount, TokenDecimals: tokenDecimals})
	if err != nil {
		return txreview.TxShape{}, err
	}
	return txreview.TxShape{ChainID: out.ChainID, To: out.To, Value: out.Value, Data: out.Data}, nil
}

func (e earnPreparer) FindProductTokenAddress(ctx context.Context, productID string, chainID int) (string, error) {
	products, err := e.svc.GetProducts(ctx, &chainID)
	if err != nil {
		return "", err
	}
	for _, p := range products {
		if p.ID == productID {
			return p.TokenAddress, nil
		}
	}
	return "", nil
}

// tokenIntelLookup adapts *token.Repository to TokenIntelLookup. The
// repo has FindByAddress only (single-token lookup) — we fan out in a
// loop. With at most 2 tokens per review this is fine.
type tokenIntelLookup struct{ repo *token.Repository }

func (t tokenIntelLookup) GetTokenIntel(ctx context.Context, chainID int, addresses []string) (map[string]txreview.TokenIntelRow, error) {
	out := map[string]txreview.TokenIntelRow{}
	for _, addr := range addresses {
		tok, err := t.repo.FindByAddress(ctx, addr)
		if err != nil {
			continue // not-found → outside-registry warning in orchestrator
		}
		if tok.ChainID != chainID {
			continue
		}
		addrStr := ""
		if tok.Address != nil {
			addrStr = *tok.Address
		}
		if addrStr == "" {
			continue
		}
		out[strings.ToLower(addrStr)] = txreview.TokenIntelRow{
			Address:    addrStr,
			Status:     tok.Status,
			IsOfficial: tok.IsOfficial,
			Tags:       tagsToStrings(tok.Tags),
		}
	}
	return out, nil
}

func tagsToStrings(tags any) []string {
	if tags == nil {
		return nil
	}
	switch v := tags.(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, t := range v {
			if s, ok := t.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

// connectedSitesLookup adapts *security.Service to ConnectedSiteLookup.
// Returns nil + no error when no match — same as NestJS.
type connectedSitesLookup struct{ svc *security.Service }

func (c connectedSitesLookup) FindConnectedSite(ctx context.Context, ownerAddress string, chainID int, origin string) (*txreview.ConnectedSiteDetail, error) {
	if c.svc == nil {
		return nil, nil
	}
	sites, err := c.svc.GetConnectedSites(ctx, ownerAddress, &chainID)
	if err != nil {
		return nil, err
	}
	for _, s := range sites {
		if s.Origin == origin {
			return &txreview.ConnectedSiteDetail{
				ID:          s.ID,
				Origin:      s.Origin,
				SiteName:    s.SiteName,
				RiskLevel:   s.RiskLevel,
				Permissions: s.Permissions,
			}, nil
		}
	}
	return nil, nil
}

// knownSpendersLookup adapts the deployments registry to
// KnownSpendersLookup. Returns the lowercase contract addresses for
// the chain (the rules engine compares case-insensitively against this
// list).
type knownSpendersLookup struct{ loader markets.DeploymentsLoader }

func (k knownSpendersLookup) KnownSpenders(chainID int) []string {
	if k.loader == nil {
		return nil
	}
	registry, err := k.loader.Load()
	if err != nil {
		return nil
	}
	cfg := registry[chainID]
	if len(cfg.Contracts) == 0 {
		return nil
	}
	out := make([]string, 0, len(cfg.Contracts))
	for _, info := range cfg.Contracts {
		if info.Address != "" {
			out = append(out, strings.ToLower(info.Address))
		}
	}
	return out
}

// bridgeStateReader adapts the bridge registry + transfer store to
// txreview.BridgeStateReader. Thin per-method delegation, same rationale as
// the other tx-review adapters: txreview must not import internal/bridge, so
// the interface is txreview-owned and the wiring lives here.
type bridgeStateReader struct {
	registry *bridge.Registry
	store    *bridge.Store
}

func (b bridgeStateReader) GatewayAddress(chainID int) (string, bool) {
	gw, ok := b.registry.Gateway(chainID)
	return gw.Address, ok
}

func (b bridgeStateReader) Route(ctx context.Context, srcChainID int, srcToken string, dstChainID int) (txreview.BridgeRouteInfo, error) {
	info, err := b.registry.Route(ctx, srcChainID, srcToken, dstChainID)
	if err != nil {
		return txreview.BridgeRouteInfo{}, err
	}
	return txreview.BridgeRouteInfo{
		Supported: info.Supported,
		DstToken:  info.DstToken,
		MinAmount: info.MinAmount,
		Paused:    info.Paused,
	}, nil
}

func (b bridgeStateReader) Liquidity(ctx context.Context, dstChainID int, dstToken string) (*big.Int, error) {
	return b.registry.Liquidity(ctx, dstChainID, dstToken)
}

func (b bridgeStateReader) SenderHasBridged(ctx context.Context, sender, srcGateway string) (bool, error) {
	// ListByAddress matches sender OR recipient and caps at 200 (its own
	// clamp), newest first; the history check is about what this wallet has
	// SENT through this gateway, so filter both fields. A wallet whose 200
	// most recent rows are all receives or other gateways reads as "first
	// transfer" — an over-warning, which is the safe direction to be wrong.
	rows, err := b.store.ListByAddress(ctx, sender, 200)
	if err != nil {
		return false, err
	}
	for _, t := range rows {
		if strings.EqualFold(t.Sender, sender) && strings.EqualFold(t.SrcGateway, srcGateway) {
			return true, nil
		}
	}
	return false, nil
}

func (b bridgeStateReader) DepositCalldata(srcToken string, amount *big.Int, dstChainID int, recipient string) string {
	return bridge.BuildDepositCalldata(srcToken, amount, dstChainID, recipient)
}

func intPtr(n int) *int { return &n }

// txReviewRPCCaller returns the txreview.RPCCaller interface for the
// tx-review service. Returns untyped nil when the client is nil so
// the simulator's nil-check works correctly.
func txReviewRPCCaller(c *rpc.Client) txreview.RPCCaller {
	if c == nil {
		return nil
	}
	return c
}

// swapRPCCaller returns the swap.EthCaller interface for the swap
// service. Returns untyped nil when the client is nil so the
// httpx-level `if deps.SwapRPC != nil` check works correctly (a typed
// nil *rpc.Client wrapped in an interface would otherwise appear
// non-nil).
func swapRPCCaller(c *rpc.Client) swap.EthCaller {
	if c == nil {
		return nil
	}
	return c
}

// bridgeChainRPCEnvPrefix is scanned for per-chain bridge RPC urls, e.g.
// BRIDGE_RPC_URL_84532 for Base Sepolia.
const bridgeChainRPCEnvPrefix = "BRIDGE_RPC_URL_"

// buildBridgeRPCs assembles the per-chain RPC clients the bridge reads route
// and liquidity state from.
//
// Sepolia reuses the already-configured client. Any other chain is opt-in via
// BRIDGE_RPC_URL_<chainId>. A chain with a deployed gateway but no url here is
// NOT silently treated as broken or as zero-liquidity — the route resolver
// reports it as unavailable, which is the honest answer.
func buildBridgeRPCs(sepolia *rpc.Client) map[int]bridge.EthCaller {
	out := map[int]bridge.EthCaller{}
	if sepolia != nil {
		out[11155111] = sepolia
	}
	for _, entry := range os.Environ() {
		key, value, found := strings.Cut(entry, "=")
		if !found || !strings.HasPrefix(key, bridgeChainRPCEnvPrefix) {
			continue
		}
		chainID, err := strconv.Atoi(strings.TrimPrefix(key, bridgeChainRPCEnvPrefix))
		if err != nil || chainID <= 0 {
			slog.Warn("bridge: ignoring malformed per-chain RPC env var", "key", key)
			continue
		}
		url := strings.TrimSpace(value)
		if url == "" {
			continue
		}
		out[chainID] = rpc.NewClient(url, 0)
		slog.Info("bridge: per-chain RPC configured", "chainId", chainID, "rpc_url", rpc.RedactURL(url))
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func listenAddr() string {
	if v := os.Getenv("API_GO_LISTEN_ADDR"); v != "" {
		return v
	}
	if v := os.Getenv("PORT"); v != "" {
		return ":" + v
	}
	return ":8080"
}

// realtimeEnabled gates the Socket.IO transport behind REALTIME_ENABLED=1/true.
func realtimeEnabled() bool {
	v := strings.TrimSpace(os.Getenv("REALTIME_ENABLED"))
	return v == "1" || strings.EqualFold(v, "true")
}

// mountRealtime routes /socket.io/* to the realtime transport and everything
// else to the REST API handler, so both share the one listener.
func mountRealtime(api http.Handler, rt *realtime.Server) http.Handler {
	sio := rt.Handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/socket.io/") {
			sio.ServeHTTP(w, r)
			return
		}
		api.ServeHTTP(w, r)
	})
}
