// Package productstatus serves GET /api/status/product — a read-only, Go-owned
// diagnostic that explains WHY a screen is empty or an action is disabled:
// database / RPC-derived indexer / realtime / swap-router / media / bridge
// readiness, plus real scenario-data counts. It is DB-only (no RPC calls) and
// never returns secrets — only booleans, public on-chain addresses, chain ids,
// block numbers, and row counts. Degraded dependencies report `degraded` /
// `unavailable`, never a fake `healthy`.
package productstatus

import (
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"os"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/deployments"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Pinger is the minimal pool surface the status service needs. *pgxpool.Pool
// satisfies it; tests pass a fake.
type Pinger interface {
	Ping(ctx context.Context) error
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

var _ Pinger = (*pgxpool.Pool)(nil)

// Deps are injected so tests can drive degraded/healthy paths deterministically.
type Deps struct {
	Pool              Pinger
	Registry          func() map[int]deployments.ChainConfig // fresh snapshot; nil-safe
	DefaultChainID    int
	RealtimeEnabled   bool
	PaperTradeEnabled bool
	MediaMode         string // "local" | "s3" | "disabled" | "" (unconfigured)
	// BridgeRPCChains is the set of chain ids this process has a bridge RPC
	// for. Delivery readiness is a deployment fact, not a code fact, so it has
	// to be injected rather than assumed.
	BridgeRPCChains map[int]bool
	// AIExplainEnabled reports whether tx-review explanations can be produced.
	// Off is the default and a normal state — the UI uses this to not offer an
	// affordance that would only ever answer "static".
	AIExplainEnabled bool
	Version          string // build/git version; "" → resolved from build info
	Now              func() time.Time
}

// Status vocabulary. Distinguishes real absence from an unavailable dependency.
const (
	StatusHealthy     = "healthy"
	StatusDegraded    = "degraded"
	StatusUnavailable = "unavailable"
	StatusDisabled    = "disabled"
)

// Stable machine warning codes (UI/alerts key off these, not the prose).
const (
	CodeDBUnavailable    = "DB_UNAVAILABLE"
	CodeIndexerNoCursor  = "INDEXER_NO_CURSOR"
	CodeIndexerBehind    = "INDEXER_BEHIND_EVENTS"
	CodeRealtimeDisabled = "REALTIME_DISABLED"
	CodeSwapNoRouter     = "SWAP_ROUTER_UNCONFIGURED"
	CodeMediaUnconfig    = "MEDIA_UNCONFIGURED"
	CodeBridgeUnavail    = "BRIDGE_UNAVAILABLE"
	CodeBridgeNoDest     = "BRIDGE_NO_REACHABLE_DESTINATION"
	CodeRelayerAbsent    = "BRIDGE_RELAYER_ABSENT"
	CodeRelayerStale     = "BRIDGE_RELAYER_STALE"
	CodeRelayerLowGas    = "BRIDGE_RELAYER_LOW_GAS"
	CodeRelayerCycleErr  = "BRIDGE_RELAYER_CYCLE_FAILING"
	CodeTransfersStuck   = "BRIDGE_TRANSFERS_STUCK"
	CodeNoScenarioData   = "NO_SCENARIO_DATA"
	CodePaperTradeOn     = "PAPER_TRADE_ENABLED"
)

// Bridge modes, derived from the deployments registry rather than hardcoded —
// a status endpoint that keeps asserting a shipped feature is unavailable is
// exactly the fabrication this file exists to prevent.
//
// Mode answers "is a gateway deployed on the default chain". Whether a transfer
// could COMPLETE is the separate `Executable` field, which also requires a
// reachable destination. Neither answers whether one specific route is open —
// that needs route()/paused()/liquidity reads and lives in POST /api/bridge/routes.
const (
	BridgeModeLive        = "live"
	BridgeModeNotDeployed = "not-deployed"
)

type Runtime struct {
	Backend string `json:"backend"`
	Version string `json:"version"`
}

type Chain struct {
	DefaultChainID  int   `json:"defaultChainId"`
	SupportedChains []int `json:"supportedChains"`
}

type Dependency struct {
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type IndexerStatus struct {
	Status            string  `json:"status"`
	CursorBlock       *int64  `json:"cursorBlock"`
	HighestEventBlock *int64  `json:"highestEventBlock"`
	LagBlocks         *int64  `json:"lagBlocks"`
	EventsTotal       *int64  `json:"eventsTotal"`
	LastEventAt       *string `json:"lastEventAt"`
	Detail            string  `json:"detail,omitempty"`
}

type SwapStatus struct {
	Status           string  `json:"status"`
	RouterConfigured bool    `json:"routerConfigured"`
	RouterAddress    *string `json:"routerAddress"`
}

type BridgeStatus struct {
	Mode string `json:"mode"`
	// Executable means a transfer could actually complete from the default
	// chain: a gateway here, and at least one destination gateway this process
	// can read. It is NOT "a gateway exists" — see the derivation in Build.
	Executable bool `json:"executable"`
	// DeliverableChains are the destination chains with both a deployed gateway
	// and a configured RPC. Empty with mode=live means deposits would escrow
	// with nothing able to deliver them.
	DeliverableChains []int `json:"deliverableChains"`
	// Relayer is the delivery worker's observed liveness. Nil means the
	// heartbeat could not be read at all (no DB) — distinct from a relayer that
	// has never run, which reports status "absent".
	Relayer *RelayerStatus `json:"relayer"`
}

// RelayerStatus is what the delivery worker last reported about itself. A
// bridge with gateways but no running relayer accepts deposits and delivers
// nothing, and that state is otherwise invisible: an idle healthy relayer and a
// stopped one both leave the transfer table unchanged.
type RelayerStatus struct {
	// "healthy" | "failing" | "stale" | "absent" | "unavailable".
	//
	// "healthy" is liveness AND a clean last cycle. A relayer whose every cycle
	// errors is live but delivering nothing, so it reports "failing" — calling
	// that healthy is how a broken relayer hides behind a green dot.
	Status string `json:"status"`
	// Address is the delivering wallet, or "" for an observe-only deployment.
	Address string `json:"address,omitempty"`
	// LastSeenAt is the last completed cycle, nil when none was ever recorded.
	LastSeenAt *string `json:"lastSeenAt"`
	// CanDeliver false means it observes but has no signer.
	CanDeliver bool `json:"canDeliver"`
	// LastCycleError is the last cycle's error verbatim — "up but failing every
	// 15 seconds" is a real production failure mode and it looks like health.
	LastCycleError string `json:"lastCycleError,omitempty"`
	// GasWei is chainId -> wei as a decimal string. A relayer out of gas keeps
	// looping and delivers nothing.
	GasWei map[string]string `json:"gasWei,omitempty"`
	// StuckTransfers are deposits still INITIATED past StuckAfter.
	StuckTransfers *int64 `json:"stuckTransfers"`
}

type MediaStatus struct {
	Status string `json:"status"`
	Mode   string `json:"mode"`
}

// DataCounts are real scenario-data row counts. A nil pointer means the count
// could not be read (dependency unavailable), distinct from a real 0.
type DataCounts struct {
	Tokens       *int64 `json:"tokens"`
	PerpTrades   *int64 `json:"perpTrades"`
	TokenTrades  *int64 `json:"tokenTrades"`
	Web3Events   *int64 `json:"web3Events"`
	Campaigns    *int64 `json:"campaigns"`
	StakingPools *int64 `json:"stakingPools"`
}

type Warning struct {
	Code     string `json:"code"`
	Severity string `json:"severity"` // "info" | "warn"
	Message  string `json:"message"`
}

// Report is the stable JSON shape returned by GET /api/status/product.
type Report struct {
	Runtime     Runtime       `json:"runtime"`
	Chain       Chain         `json:"chain"`
	Database    Dependency    `json:"database"`
	Indexer     IndexerStatus `json:"indexer"`
	Realtime    Dependency    `json:"realtime"`
	Swap        SwapStatus    `json:"swap"`
	Bridge      BridgeStatus  `json:"bridge"`
	Media       MediaStatus   `json:"media"`
	AIExplain   Dependency    `json:"aiExplain"`
	PaperTrade  Dependency    `json:"paperTrade"`
	Data        DataCounts    `json:"data"`
	Warnings    []Warning     `json:"warnings"`
	GeneratedAt string        `json:"generatedAt"`
}

// Service builds the report from injected dependencies.
type Service struct{ deps Deps }

func NewService(deps Deps) *Service {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.Version == "" {
		deps.Version = resolveVersion()
	}
	return &Service{deps: deps}
}

// Build assembles the report. Every section degrades independently: one
// unavailable dependency never turns another into a fake OK.
func (s *Service) Build(ctx context.Context) Report {
	now := s.deps.Now().UTC()
	rep := Report{
		Runtime:     Runtime{Backend: "go", Version: s.deps.Version},
		Bridge:      BridgeStatus{Mode: BridgeModeNotDeployed, Executable: false},
		GeneratedAt: now.Format(time.RFC3339),
		Warnings:    []Warning{},
	}

	// ── Chain / supported chains (from the deployments registry) ────────────
	registry := map[int]deployments.ChainConfig{}
	if s.deps.Registry != nil {
		if r := s.deps.Registry(); r != nil {
			registry = r
		}
	}
	chains := make([]int, 0, len(registry))
	for id := range registry {
		chains = append(chains, id)
	}
	sort.Ints(chains)
	defaultChain := s.deps.DefaultChainID
	if defaultChain == 0 {
		if _, ok := registry[11155111]; ok {
			defaultChain = 11155111
		} else if len(chains) > 0 {
			defaultChain = chains[0]
		}
	}
	rep.Chain = Chain{DefaultChainID: defaultChain, SupportedChains: chains}

	// ── Swap router readiness (public address; no secret) ───────────────────
	router := ""
	if cfg, ok := registry[defaultChain]; ok {
		router = deployments.LookupAddress(cfg, "router", "Router")
	}
	if router != "" {
		routerCopy := router
		rep.Swap = SwapStatus{Status: StatusHealthy, RouterConfigured: true, RouterAddress: &routerCopy}
	} else {
		rep.Swap = SwapStatus{Status: StatusUnavailable, RouterConfigured: false}
		rep.Warnings = append(rep.Warnings, Warning{
			Code: CodeSwapNoRouter, Severity: "warn",
			Message: "No swap Router is configured for the default chain; swap quotes fall back to non-executable estimates.",
		})
	}

	// ── Realtime ────────────────────────────────────────────────────────────
	if s.deps.RealtimeEnabled {
		rep.Realtime = Dependency{Status: StatusHealthy}
	} else {
		rep.Realtime = Dependency{Status: StatusDisabled, Detail: "REALTIME_ENABLED is not set"}
		rep.Warnings = append(rep.Warnings, Warning{
			Code: CodeRealtimeDisabled, Severity: "info",
			Message: "Realtime (Socket.IO) is disabled; live prices fall back to REST polling.",
		})
	}

	// ── AI tx-review explanations ───────────────────────────────────────────
	// No warning when off: this layer is presentation-only, it ships dark by
	// design, and every review is complete without it. Flagging "disabled" as
	// a problem would train operators to ignore the warnings that matter.
	if s.deps.AIExplainEnabled {
		rep.AIExplain = Dependency{Status: StatusHealthy,
			Detail: "DeepSeek explanations are configured; upstream availability is checked per request and the deterministic checks remain the verdict."}
	} else {
		rep.AIExplain = Dependency{Status: StatusDisabled,
			Detail: "Plain-language explanations are off (set AI_ENABLED + DEEPSEEK_API_KEY); reviews render from the deterministic check text."}
	}

	// ── Media storage ───────────────────────────────────────────────────────
	mode := strings.ToLower(strings.TrimSpace(s.deps.MediaMode))
	switch mode {
	case "local", "s3":
		rep.Media = MediaStatus{Status: StatusHealthy, Mode: mode}
	default:
		rep.Media = MediaStatus{Status: StatusUnavailable, Mode: "disabled"}
		rep.Warnings = append(rep.Warnings, Warning{
			Code: CodeMediaUnconfig, Severity: "warn",
			Message: "Media storage is not configured; uploads (create-token / NFT artwork) return 503.",
		})
	}

	// ── Paper trade (should be OFF in this product stage) ───────────────────
	if s.deps.PaperTradeEnabled {
		rep.PaperTrade = Dependency{Status: StatusHealthy, Detail: "enabled (dev only)"}
		rep.Warnings = append(rep.Warnings, Warning{
			Code: CodePaperTradeOn, Severity: "warn",
			Message: "Paper trading is enabled — it must stay disabled in production.",
		})
	} else {
		rep.PaperTrade = Dependency{Status: StatusDisabled}
	}

	// ── Bridge ──────────────────────────────────────────────────────────────
	// Derived from the same registry snapshot the /bridge mount reads, so the
	// two can't disagree. `executable` stays a per-route question — see the
	// BridgeMode doc comment.
	// `executable` used to mean only "a gateway is deployed on the default
	// chain". Production then reported executable=true while it had no
	// destination RPCs and no relayer — nothing there could have completed a
	// transfer. A field named `executable` has to mean the transfer could
	// actually happen, not that half the pieces exist.
	//
	// A deposit needs a source gateway AND a destination the API can read. The
	// route endpoint already refuses to be executable without the destination
	// read; this makes the deployment-level summary agree with it instead of
	// being a looser, rosier claim.
	bridgeGateway := ""
	if cfg, ok := registry[defaultChain]; ok {
		bridgeGateway = deployments.LookupAddress(cfg, "bridgeGateway", "BridgeGateway")
	}
	destinations := make([]int, 0, len(registry))
	for chainID, cfg := range registry {
		if chainID == defaultChain {
			continue
		}
		if deployments.LookupAddress(cfg, "bridgeGateway", "BridgeGateway") == "" {
			continue
		}
		if s.deps.BridgeRPCChains[chainID] {
			destinations = append(destinations, chainID)
		}
	}
	sort.Ints(destinations)

	switch {
	case bridgeGateway == "":
		rep.Bridge = BridgeStatus{Mode: BridgeModeNotDeployed, Executable: false, DeliverableChains: destinations}
		rep.Warnings = append(rep.Warnings, Warning{
			Code: CodeBridgeUnavail, Severity: "info",
			Message: "No BridgeGateway is deployed on the default chain, so no transfer can execute from it.",
		})
	case len(destinations) == 0:
		// The gateway exists but nothing can be delivered to: either no second
		// gateway is deployed, or this process has no RPC for it. Both mean a
		// deposit would escrow with no reachable destination.
		rep.Bridge = BridgeStatus{Mode: BridgeModeLive, Executable: false, DeliverableChains: destinations}
		rep.Warnings = append(rep.Warnings, Warning{
			Code: CodeBridgeNoDest, Severity: "warn",
			Message: "A bridge gateway is deployed but no destination chain is reachable (set BRIDGE_RPC_URL_<chainId>); deposits would escrow with nothing able to deliver them.",
		})
	default:
		rep.Bridge = BridgeStatus{Mode: BridgeModeLive, Executable: true, DeliverableChains: destinations}
	}

	// ── Database + indexer + counts (DB-only) ───────────────────────────────
	s.fillDatabaseSections(ctx, &rep, defaultChain)

	return rep
}

func (s *Service) fillDatabaseSections(ctx context.Context, rep *Report, chainID int) {
	pool := s.deps.Pool
	if pool == nil {
		rep.Database = Dependency{Status: StatusUnavailable, Detail: "no database pool configured"}
		rep.Indexer = IndexerStatus{Status: StatusUnavailable, Detail: "database unavailable"}
		rep.Warnings = append(rep.Warnings, Warning{
			Code: CodeDBUnavailable, Severity: "warn",
			Message: "Database is unavailable; live product data cannot be read.",
		})
		return
	}
	if err := pool.Ping(ctx); err != nil {
		rep.Database = Dependency{Status: StatusUnavailable, Detail: "database ping failed"}
		rep.Indexer = IndexerStatus{Status: StatusUnavailable, Detail: "database unavailable"}
		rep.Warnings = append(rep.Warnings, Warning{
			Code: CodeDBUnavailable, Severity: "warn",
			Message: "Database ping failed; live product data cannot be read.",
		})
		return
	}
	rep.Database = Dependency{Status: StatusHealthy}

	// Indexer cursor + highest indexed event block + last event time.
	ind := IndexerStatus{Status: StatusHealthy}
	// web3_indexer_state holds ONE ROW PER CONTRACT, and production carries
	// orphaned rows from before addresses were normalized to lower case (the
	// same contract, checksum-cased, frozen at an old block). An unqualified
	// single-row SELECT returned whichever row Postgres happened to hand back,
	// so picking a stale orphan reported a ~400k-block lag on a perfectly
	// healthy indexer. MAX is the honest answer to "how far has the indexer
	// scanned", and is immune to orphan rows.
	if cursor, ok := scanInt(ctx, pool, `SELECT MAX(last_processed_block) FROM web3_indexer_state WHERE chain_id = $1`, chainID); ok {
		ind.CursorBlock = &cursor
	}
	if highest, ok := scanInt(ctx, pool,
		`SELECT block_number FROM web3_events WHERE chain_id = $1 ORDER BY block_number DESC LIMIT 1`, chainID); ok {
		ind.HighestEventBlock = &highest
	}
	if total, ok := scanInt(ctx, pool, `SELECT COUNT(*)::bigint FROM web3_events WHERE chain_id = $1`, chainID); ok {
		ind.EventsTotal = &total
	}
	if last, ok := scanTime(ctx, pool,
		`SELECT MAX(created_at) FROM web3_events WHERE chain_id = $1`, chainID); ok {
		iso := last.UTC().Format(time.RFC3339)
		ind.LastEventAt = &iso
	}
	if ind.CursorBlock != nil && ind.HighestEventBlock != nil {
		lag := max(*ind.HighestEventBlock-*ind.CursorBlock, 0)
		ind.LagBlocks = &lag
		if lag > 0 {
			ind.Status = StatusDegraded
			rep.Warnings = append(rep.Warnings, Warning{
				Code: CodeIndexerBehind, Severity: "warn",
				Message: "The indexer cursor is behind the highest indexed event block.",
			})
		}
	}
	if ind.CursorBlock == nil {
		// No cursor row yet — indexer has not checkpointed this chain.
		rep.Warnings = append(rep.Warnings, Warning{
			Code: CodeIndexerNoCursor, Severity: "info",
			Message: "The indexer has no checkpoint for this chain yet.",
		})
	}
	rep.Indexer = ind

	// Real scenario-data counts (each best-effort; nil = unreadable).
	rep.Data = DataCounts{
		Tokens:       countOpt(ctx, pool, `SELECT COUNT(*)::bigint FROM tokens`),
		PerpTrades:   countOpt(ctx, pool, `SELECT COUNT(*)::bigint FROM perp_trades`),
		TokenTrades:  countOpt(ctx, pool, `SELECT COUNT(*)::bigint FROM token_trades`),
		Web3Events:   countOpt(ctx, pool, `SELECT COUNT(*)::bigint FROM web3_events`),
		Campaigns:    countOpt(ctx, pool, `SELECT COUNT(*)::bigint FROM launchpad_campaigns`),
		StakingPools: countOpt(ctx, pool, `SELECT COUNT(*)::bigint FROM staking_pools`),
	}
	if allZeroOrNil(rep.Data) {
		rep.Warnings = append(rep.Warnings, Warning{
			Code: CodeNoScenarioData, Severity: "info",
			Message: "No product data yet — run the canonical testnet scenario to populate live rows.",
		})
	}

	s.fillRelayerStatus(ctx, rep)
}

// StaleAfter is how long without a completed cycle before the relayer is stale.
// The worker cycles every 15s, so minutes of silence is a real stop, not a slow
// tick.
const StaleAfter = 3 * time.Minute

// StuckAfter is how long a deposit may sit INITIATED before it is worth
// flagging. Delivery normally takes seconds; this is generous enough that a
// congested chain does not trip it.
const StuckAfter = 15 * time.Minute

// LowGasWei flags a relayer that will soon stop being able to deliver. A fulfil
// costs on the order of 1e12 wei on these chains, so 1e15 is roughly a thousand
// deliveries of headroom — early enough to act on, not so early it cries wolf.
const LowGasWei = 1_000_000_000_000_000

// fillRelayerStatus reports the delivery worker's own liveness. Without it a
// stopped relayer is invisible: the bridge keeps accepting deposits, the
// transfer table simply stops changing, and every other section stays green.
func (s *Service) fillRelayerStatus(ctx context.Context, rep *Report) {
	pool := s.deps.Pool
	if pool == nil {
		rep.Bridge.Relayer = &RelayerStatus{Status: StatusUnavailable}
		return
	}
	st := &RelayerStatus{Status: "absent"}
	rep.Bridge.Relayer = st

	// Deposits still awaiting delivery past the grace period. Read even when no
	// heartbeat exists — it is the user-visible symptom either way.
	if n, ok := scanInt(ctx, pool,
		`SELECT COUNT(*)::bigint FROM bridge_transfers
		  WHERE status = 'INITIATED' AND deposited_at < now() - interval '15 minutes'`); ok {
		st.StuckTransfers = &n
	}

	var (
		addr       string
		lastSeen   time.Time
		canDeliver bool
		lastErr    *string
		gasRaw     []byte
	)
	err := pool.QueryRow(ctx, `
		SELECT relayer_address, last_seen_at, can_deliver, last_cycle_error, gas_balances
		  FROM bridge_relayer_heartbeat
		 ORDER BY last_seen_at DESC
		 LIMIT 1`).Scan(&addr, &lastSeen, &canDeliver, &lastErr, &gasRaw)
	if err != nil {
		// No row is "never ran"; a missing table is the same thing from the
		// product's point of view — nothing is delivering. Both stay "absent"
		// rather than being dressed up as unknown.
		if st.StuckTransfers == nil {
			st.Status = StatusUnavailable
		}
		s.warnRelayer(rep, st)
		return
	}

	st.Address = addr
	st.CanDeliver = canDeliver
	seen := lastSeen.UTC().Format(time.RFC3339)
	st.LastSeenAt = &seen
	if lastErr != nil {
		st.LastCycleError = *lastErr
	}
	if len(gasRaw) > 0 {
		_ = json.Unmarshal(gasRaw, &st.GasWei)
	}
	switch {
	case s.deps.Now().UTC().Sub(lastSeen.UTC()) > StaleAfter:
		st.Status = "stale"
	case st.LastCycleError != "":
		st.Status = "failing"
	default:
		st.Status = StatusHealthy
	}
	s.warnRelayer(rep, st)
}

func (s *Service) warnRelayer(rep *Report, st *RelayerStatus) {
	// Only a deployed bridge can have a missing relayer worth reporting.
	if rep.Bridge.Mode != BridgeModeLive {
		return
	}
	switch st.Status {
	case "absent", StatusUnavailable:
		rep.Warnings = append(rep.Warnings, Warning{
			Code: CodeRelayerAbsent, Severity: "warn",
			Message: "No bridge relayer has ever reported in; deposits would escrow and never be delivered.",
		})
	case "stale":
		rep.Warnings = append(rep.Warnings, Warning{
			Code: CodeRelayerStale, Severity: "warn",
			Message: "The bridge relayer has not completed a cycle recently; deliveries are stalled.",
		})
	case "failing":
		rep.Warnings = append(rep.Warnings, Warning{
			Code: CodeRelayerCycleErr, Severity: "warn",
			Message: "The bridge relayer is running but its last cycle failed; see relayer.lastCycleError.",
		})
	}
	if st.StuckTransfers != nil && *st.StuckTransfers > 0 {
		rep.Warnings = append(rep.Warnings, Warning{
			Code: CodeTransfersStuck, Severity: "warn",
			Message: "Deposits are escrowed and still undelivered past the delivery window.",
		})
	}
	for _, wei := range st.GasWei {
		v, ok := new(big.Int).SetString(wei, 10)
		if ok && v.Cmp(big.NewInt(LowGasWei)) < 0 {
			rep.Warnings = append(rep.Warnings, Warning{
				Code: CodeRelayerLowGas, Severity: "warn",
				Message: "The bridge relayer is low on gas on at least one chain; it will stop being able to deliver.",
			})
			break
		}
	}
}

func scanInt(ctx context.Context, p Pinger, sql string, args ...any) (int64, bool) {
	var v int64
	if err := p.QueryRow(ctx, sql, args...).Scan(&v); err != nil {
		return 0, false
	}
	return v, true
}

func scanTime(ctx context.Context, p Pinger, sql string, args ...any) (time.Time, bool) {
	var v *time.Time
	if err := p.QueryRow(ctx, sql, args...).Scan(&v); err != nil || v == nil {
		return time.Time{}, false
	}
	return *v, true
}

func countOpt(ctx context.Context, p Pinger, sql string) *int64 {
	if v, ok := scanInt(ctx, p, sql); ok {
		return &v
	}
	return nil
}

func allZeroOrNil(d DataCounts) bool {
	for _, c := range []*int64{d.Tokens, d.PerpTrades, d.TokenTrades, d.Web3Events, d.Campaigns, d.StakingPools} {
		if c != nil && *c > 0 {
			return false
		}
	}
	return true
}

func resolveVersion() string {
	if v := strings.TrimSpace(os.Getenv("BUILD_VERSION")); v != "" {
		return v
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" && s.Value != "" {
				rev := s.Value
				if len(rev) > 12 {
					rev = rev[:12]
				}
				return rev
			}
		}
	}
	return "dev"
}

// Router mounts GET /product. Mount at /api/status.
func Router(svc *Service) chi.Router {
	r := chi.NewRouter()
	r.Get("/product", func(w http.ResponseWriter, req *http.Request) {
		rep := svc.Build(req.Context())
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(rep)
	})
	return r
}
