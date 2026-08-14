package productstatus

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"net/http"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/deployments"
	"github.com/jackc/pgx/v5"
)

// ── fake pgx surface ────────────────────────────────────────────────────────

type fakeRow struct {
	i   int64
	t   *time.Time
	err error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) == 0 {
		return nil
	}
	switch d := dest[0].(type) {
	case *int64:
		*d = r.i
	case **time.Time:
		*d = r.t
	default:
		return errors.New("fakeRow: unexpected dest type")
	}
	return nil
}

type fakePinger struct {
	pingErr error
	row     func(sql string) pgx.Row
}

func (p *fakePinger) Ping(ctx context.Context) error { return p.pingErr }
func (p *fakePinger) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return p.row(sql)
}

func intRow(i int64) pgx.Row      { return fakeRow{i: i} }
func timeRow(t time.Time) pgx.Row { return fakeRow{t: &t} }

func registryWithRouter() func() map[int]deployments.ChainConfig {
	return func() map[int]deployments.ChainConfig {
		return map[int]deployments.ChainConfig{
			11155111: {Contracts: map[string]deployments.ContractInfo{
				"router": {Address: "0x1111111111111111111111111111111111111111"},
			}},
		}
	}
}

func healthyPinger() *fakePinger {
	last := time.Date(2026, 7, 8, 6, 32, 4, 0, time.UTC)
	return &fakePinger{row: func(sql string) pgx.Row {
		switch {
		case strings.Contains(sql, "web3_indexer_state"):
			return intRow(105)
		case strings.Contains(sql, "ORDER BY block_number DESC"):
			return intRow(105)
		case strings.Contains(sql, "MAX(created_at)"):
			return timeRow(last)
		case strings.Contains(sql, "FROM web3_events") && strings.Contains(sql, "WHERE chain_id"):
			return intRow(23)
		case strings.Contains(sql, "FROM tokens"):
			return intRow(1)
		case strings.Contains(sql, "FROM perp_trades"):
			return intRow(4)
		case strings.Contains(sql, "FROM token_trades"):
			return intRow(2)
		case strings.Contains(sql, "FROM web3_events"):
			return intRow(23)
		case strings.Contains(sql, "FROM launchpad_campaigns"):
			return intRow(1)
		case strings.Contains(sql, "FROM staking_pools"):
			return intRow(1)
		default:
			return intRow(0)
		}
	}}
}

func fixedNow() func() time.Time {
	return func() time.Time { return time.Date(2026, 7, 8, 7, 0, 0, 0, time.UTC) }
}

// ── tests ───────────────────────────────────────────────────────────────────

func TestBuild_HealthyReportsRealData(t *testing.T) {
	svc := NewService(Deps{
		Pool:            healthyPinger(),
		Registry:        registryWithRouter(),
		RealtimeEnabled: true,
		MediaMode:       "local",
		Version:         "abc123",
		Now:             fixedNow(),
	})
	rep := svc.Build(context.Background())

	if rep.Runtime.Backend != "go" {
		t.Errorf("backend = %q, want go", rep.Runtime.Backend)
	}
	if rep.Database.Status != StatusHealthy {
		t.Errorf("database = %q, want healthy", rep.Database.Status)
	}
	if rep.Swap.Status != StatusHealthy || !rep.Swap.RouterConfigured {
		t.Errorf("swap = %+v, want healthy+configured", rep.Swap)
	}
	if rep.Realtime.Status != StatusHealthy {
		t.Errorf("realtime = %q, want healthy", rep.Realtime.Status)
	}
	if rep.Media.Status != StatusHealthy || rep.Media.Mode != "local" {
		t.Errorf("media = %+v, want healthy/local", rep.Media)
	}
	if rep.Data.Tokens == nil || *rep.Data.Tokens != 1 {
		t.Errorf("data.tokens = %v, want 1", rep.Data.Tokens)
	}
	if rep.Data.PerpTrades == nil || *rep.Data.PerpTrades != 4 {
		t.Errorf("data.perpTrades = %v, want 4", rep.Data.PerpTrades)
	}
	if rep.Chain.DefaultChainID != 11155111 {
		t.Errorf("defaultChain = %d, want 11155111", rep.Chain.DefaultChainID)
	}
	// Healthy path: no DB/media/swap warnings.
	for _, w := range rep.Warnings {
		if w.Code == CodeDBUnavailable || w.Code == CodeMediaUnconfig || w.Code == CodeSwapNoRouter {
			t.Errorf("unexpected warning on healthy path: %s", w.Code)
		}
	}
}

func TestBuild_NilPoolDegradesNotFakeOK(t *testing.T) {
	svc := NewService(Deps{Pool: nil, Registry: registryWithRouter(), Now: fixedNow()})
	rep := svc.Build(context.Background())

	if rep.Database.Status != StatusUnavailable {
		t.Errorf("database = %q, want unavailable (never fake healthy)", rep.Database.Status)
	}
	if rep.Indexer.Status != StatusUnavailable {
		t.Errorf("indexer = %q, want unavailable", rep.Indexer.Status)
	}
	if rep.Data.Tokens != nil {
		t.Errorf("data.tokens = %v, want nil (unreadable, not a fake 0)", rep.Data.Tokens)
	}
	if !hasWarning(rep, CodeDBUnavailable) {
		t.Errorf("missing %s warning", CodeDBUnavailable)
	}
}

func TestBuild_PingFailureDegrades(t *testing.T) {
	p := healthyPinger()
	p.pingErr = errors.New("connection refused")
	svc := NewService(Deps{Pool: p, Registry: registryWithRouter(), Now: fixedNow()})
	rep := svc.Build(context.Background())
	if rep.Database.Status != StatusUnavailable {
		t.Errorf("database = %q, want unavailable on ping failure", rep.Database.Status)
	}
}

// Bridge mode is derived from the registry, not asserted. The predecessor of
// this test pinned a hardcoded "hidden-roadmap" — true when no adapter existed,
// and a false statement the moment BridgeGateway shipped. A status endpoint is
// only useful if it tracks reality, so what's pinned now is the derivation.
func TestBuild_BridgeNotDeployed(t *testing.T) {
	svc := NewService(Deps{Pool: healthyPinger(), Registry: registryWithRouter(), Now: fixedNow()})
	rep := svc.Build(context.Background())
	if rep.Bridge.Mode != BridgeModeNotDeployed {
		t.Errorf("bridge.mode = %q, want %q", rep.Bridge.Mode, BridgeModeNotDeployed)
	}
	if rep.Bridge.Executable {
		t.Errorf("bridge.executable = true with no gateway deployed")
	}
	if !hasWarning(rep, CodeBridgeUnavail) {
		t.Errorf("missing %s warning", CodeBridgeUnavail)
	}
}

// twoChainRegistry has a gateway on the default chain and one destination.
func twoChainRegistry() func() map[int]deployments.ChainConfig {
	return func() map[int]deployments.ChainConfig {
		return map[int]deployments.ChainConfig{
			11155111: {Contracts: map[string]deployments.ContractInfo{
				"router":        {Address: "0x1111111111111111111111111111111111111111"},
				"bridgeGateway": {Address: "0x2d68a51fb4c3f3ac26fa48b8a457d150132f185d"},
			}},
			84532: {Contracts: map[string]deployments.ContractInfo{
				"bridgeGateway": {Address: "0x9c1f86e09ae424734cf7fbe34f005e6e1277fef5"},
			}},
		}
	}
}

func TestBuild_BridgeExecutableNeedsAReachableDestination(t *testing.T) {
	rep := NewService(Deps{
		Pool: healthyPinger(), Registry: twoChainRegistry(), Now: fixedNow(),
		BridgeRPCChains: map[int]bool{84532: true},
	}).Build(context.Background())

	if rep.Bridge.Mode != BridgeModeLive || !rep.Bridge.Executable {
		t.Errorf("bridge = %+v, want live+executable", rep.Bridge)
	}
	if len(rep.Bridge.DeliverableChains) != 1 || rep.Bridge.DeliverableChains[0] != 84532 {
		t.Errorf("deliverableChains = %v", rep.Bridge.DeliverableChains)
	}
	if hasWarning(rep, CodeBridgeUnavail) || hasWarning(rep, CodeBridgeNoDest) {
		t.Errorf("a fully wired bridge must not warn: %+v", rep.Warnings)
	}
}

// Production reported executable=true with no destination RPC and no relayer —
// nothing there could have completed a transfer. A field named `executable` has
// to mean the transfer could actually happen.
func TestBuild_BridgeNotExecutableWithoutADestinationRPC(t *testing.T) {
	rep := NewService(Deps{
		Pool: healthyPinger(), Registry: twoChainRegistry(), Now: fixedNow(),
		BridgeRPCChains: map[int]bool{}, // gateway deployed, but unreachable
	}).Build(context.Background())

	if rep.Bridge.Mode != BridgeModeLive {
		t.Errorf("the gateway IS deployed, mode should stay live: %q", rep.Bridge.Mode)
	}
	if rep.Bridge.Executable {
		t.Error("executable must be false when no destination can be read")
	}
	if len(rep.Bridge.DeliverableChains) != 0 {
		t.Errorf("deliverableChains = %v, want empty", rep.Bridge.DeliverableChains)
	}
	if !hasWarning(rep, CodeBridgeNoDest) {
		t.Errorf("missing %s: deposits would escrow undeliverable", CodeBridgeNoDest)
	}
}

// A destination with an RPC but no deployed gateway is not deliverable either.
func TestBuild_BridgeIgnoresChainsWithoutAGateway(t *testing.T) {
	registry := func() map[int]deployments.ChainConfig {
		return map[int]deployments.ChainConfig{
			11155111: {Contracts: map[string]deployments.ContractInfo{
				"bridgeGateway": {Address: "0x2d68a51fb4c3f3ac26fa48b8a457d150132f185d"},
			}},
			421614: {Contracts: map[string]deployments.ContractInfo{
				"router": {Address: "0x3333333333333333333333333333333333333333"},
			}},
		}
	}
	rep := NewService(Deps{
		Pool: healthyPinger(), Registry: registry, Now: fixedNow(),
		BridgeRPCChains: map[int]bool{421614: true},
	}).Build(context.Background())

	if rep.Bridge.Executable {
		t.Error("an RPC without a gateway is not a deliverable destination")
	}
}

// The AI explanation layer is off by default, and off is not a warning: it is
// presentation-only, and flagging it would train operators to skim warnings.
func TestBuild_AIExplainDefaultsOffWithoutWarning(t *testing.T) {
	rep := NewService(Deps{Pool: healthyPinger(), Registry: registryWithRouter(), Now: fixedNow()}).
		Build(context.Background())
	if rep.AIExplain.Status != StatusDisabled {
		t.Errorf("aiExplain.status = %q, want disabled", rep.AIExplain.Status)
	}
	if rep.AIExplain.Detail == "" {
		t.Error("aiExplain must say how to turn it on")
	}
	if !strings.Contains(rep.AIExplain.Detail, "DEEPSEEK_API_KEY") || strings.Contains(rep.AIExplain.Detail, "ANTHROPIC_API_KEY") {
		t.Errorf("aiExplain detail has a stale provider contract: %q", rep.AIExplain.Detail)
	}
	for _, w := range rep.Warnings {
		if strings.Contains(strings.ToLower(w.Message), "explanation") {
			t.Errorf("disabled explanations must not raise a warning: %+v", w)
		}
	}

	on := NewService(Deps{Pool: healthyPinger(), Registry: registryWithRouter(), Now: fixedNow(),
		AIExplainEnabled: true}).Build(context.Background())
	if on.AIExplain.Status != StatusHealthy {
		t.Errorf("aiExplain.status = %q, want healthy", on.AIExplain.Status)
	}
}

func TestBuild_DegradedDependenciesWarn(t *testing.T) {
	// No router, realtime off, media unconfigured, paper trade on.
	svc := NewService(Deps{
		Pool:              healthyPinger(),
		Registry:          func() map[int]deployments.ChainConfig { return map[int]deployments.ChainConfig{11155111: {}} },
		RealtimeEnabled:   false,
		PaperTradeEnabled: true,
		MediaMode:         "",
		Now:               fixedNow(),
	})
	rep := svc.Build(context.Background())
	for _, code := range []string{CodeSwapNoRouter, CodeRealtimeDisabled, CodeMediaUnconfig, CodePaperTradeOn} {
		if !hasWarning(rep, code) {
			t.Errorf("missing warning %s", code)
		}
	}
	if rep.Swap.RouterConfigured {
		t.Errorf("swap.routerConfigured = true, want false")
	}
	if rep.PaperTrade.Status != StatusHealthy {
		t.Errorf("paperTrade.status = %q, want healthy(enabled)", rep.PaperTrade.Status)
	}
}

func TestBuild_IndexerLagDegrades(t *testing.T) {
	p := healthyPinger()
	p.row = func(sql string) pgx.Row {
		switch {
		case strings.Contains(sql, "web3_indexer_state"):
			return intRow(100) // cursor behind
		case strings.Contains(sql, "ORDER BY block_number DESC"):
			return intRow(105) // highest event ahead
		case strings.Contains(sql, "MAX(created_at)"):
			return timeRow(time.Now())
		default:
			return intRow(0)
		}
	}
	svc := NewService(Deps{Pool: p, Registry: registryWithRouter(), Now: fixedNow()})
	rep := svc.Build(context.Background())
	if rep.Indexer.Status != StatusDegraded {
		t.Errorf("indexer.status = %q, want degraded on lag", rep.Indexer.Status)
	}
	if rep.Indexer.LagBlocks == nil || *rep.Indexer.LagBlocks != 5 {
		t.Errorf("lagBlocks = %v, want 5", rep.Indexer.LagBlocks)
	}
	if !hasWarning(rep, CodeIndexerBehind) {
		t.Errorf("missing %s warning", CodeIndexerBehind)
	}
}

func TestBuild_NoScenarioDataWarns(t *testing.T) {
	p := &fakePinger{row: func(string) pgx.Row { return intRow(0) }}
	svc := NewService(Deps{Pool: p, Registry: registryWithRouter(), RealtimeEnabled: true, MediaMode: "local", Now: fixedNow()})
	rep := svc.Build(context.Background())
	if !hasWarning(rep, CodeNoScenarioData) {
		t.Errorf("missing %s warning when all counts are zero", CodeNoScenarioData)
	}
}

func TestBuild_NoSecretsLeak(t *testing.T) {
	// Even with secret-shaped env present, the report must not echo them.
	t.Setenv("DATABASE_URL", "postgres://app:sup3rsecret@db:5432/x")
	t.Setenv("SEPOLIA_RPC_URL", "https://sepolia.infura.io/v3/DEADBEEFKEY")
	t.Setenv("JWT_SECRET", "jwt-shhh")
	t.Setenv("BUILD_VERSION", "v1.2.3")

	svc := NewService(Deps{Pool: healthyPinger(), Registry: registryWithRouter(), MediaMode: "local", Now: fixedNow()})
	raw, err := json.Marshal(svc.Build(context.Background()))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	blob := string(raw)
	for _, secret := range []string{"sup3rsecret", "DEADBEEFKEY", "jwt-shhh", "infura.io", "postgres://"} {
		if strings.Contains(blob, secret) {
			t.Errorf("report leaked secret substring %q: %s", secret, blob)
		}
	}
	// The public build version is fine to surface.
	if !strings.Contains(blob, "v1.2.3") {
		t.Errorf("expected build version in report")
	}
}

func TestRouter_ServesStableShape(t *testing.T) {
	svc := NewService(Deps{Pool: healthyPinger(), Registry: registryWithRouter(), RealtimeEnabled: true, MediaMode: "local", Now: fixedNow()})
	req := httptest.NewRequest(http.MethodGet, "/product", nil)
	rec := httptest.NewRecorder()
	Router(svc).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("content-type = %q", ct)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, key := range []string{
		"runtime", "chain", "database", "indexer", "realtime",
		"swap", "bridge", "media", "paperTrade", "data", "warnings", "generatedAt",
	} {
		if _, ok := m[key]; !ok {
			t.Errorf("missing top-level key %q", key)
		}
	}
}

func hasWarning(rep Report, code string) bool {
	for _, w := range rep.Warnings {
		if w.Code == code {
			return true
		}
	}
	return false
}

// Regression: production's web3_indexer_state carries orphaned rows from before
// contract addresses were normalized to lower case — the same contract, stuck
// at an old block. The cursor query must aggregate, not take an arbitrary row,
// or a healthy indexer reports a fabricated multi-hundred-thousand-block lag
// (observed in production: cursor 11028000 vs a live cursor of 11431984).
func TestIndexerCursorAggregatesAcrossOrphanRows(t *testing.T) {
	if !strings.Contains(indexerCursorSQLProbe(t), "MAX(") {
		t.Fatal("the indexer cursor query must use MAX(last_processed_block); an unqualified single-row SELECT picks an arbitrary row and can report a stale orphan as the cursor")
	}
}

// indexerCursorSQLProbe captures the SQL the service issues against
// web3_indexer_state.
func indexerCursorSQLProbe(t *testing.T) string {
	t.Helper()
	var captured string
	pinger := &fakePinger{row: func(sql string) pgx.Row {
		if strings.Contains(sql, "web3_indexer_state") {
			captured = sql
			return intRow(11431984)
		}
		return intRow(0)
	}}
	svc := NewService(Deps{Pool: pinger, DefaultChainID: 11155111, Now: fixedNow()})
	svc.Build(context.Background())
	if captured == "" {
		t.Fatal("service never queried web3_indexer_state")
	}
	return captured
}

// A stale orphan row must not drag the reported cursor backwards or raise a
// spurious "indexer is behind" warning.
func TestStaleOrphanRowDoesNotFakeIndexerLag(t *testing.T) {
	pinger := &fakePinger{row: func(sql string) pgx.Row {
		switch {
		case strings.Contains(sql, "web3_indexer_state"):
			// MAX() over {11028000 orphan, 11431984 live} = the live cursor.
			return intRow(11431984)
		case strings.Contains(sql, "ORDER BY block_number DESC"):
			return intRow(11431984)
		default:
			return intRow(0)
		}
	}}
	rep := NewService(Deps{Pool: pinger, DefaultChainID: 11155111, Now: fixedNow()}).Build(context.Background())

	if rep.Indexer.Status != StatusHealthy {
		t.Errorf("want healthy indexer, got %q (detail=%q)", rep.Indexer.Status, rep.Indexer.Detail)
	}
	if rep.Indexer.LagBlocks == nil || *rep.Indexer.LagBlocks != 0 {
		t.Errorf("want zero lag, got %v", rep.Indexer.LagBlocks)
	}
	if hasWarning(rep, CodeIndexerBehind) {
		t.Error("a caught-up indexer must not raise INDEXER_BEHIND_EVENTS")
	}
}

// ─── relayer liveness ───────────────────────────────────────────────────────
//
// A stopped relayer is otherwise invisible: the bridge keeps accepting deposits,
// bridge_transfers simply stops changing, and every other section stays green.
// Production ran in exactly that state.

// heartbeatRow satisfies the 5-column heartbeat scan.
type heartbeatRow struct {
	addr       string
	lastSeen   time.Time
	canDeliver bool
	lastErr    *string
	gas        string
	err        error
}

func (r heartbeatRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != 5 {
		return errors.New("heartbeatRow: want 5 dests")
	}
	*(dest[0].(*string)) = r.addr
	*(dest[1].(*time.Time)) = r.lastSeen
	*(dest[2].(*bool)) = r.canDeliver
	*(dest[3].(**string)) = r.lastErr
	*(dest[4].(*[]byte)) = []byte(r.gas)
	return nil
}

// relayerPinger answers the heartbeat + stuck-transfer queries; everything else
// falls through to the healthy fixture.
func relayerPinger(stuck int64, hb pgx.Row) *fakePinger {
	base := healthyPinger()
	return &fakePinger{row: func(sql string) pgx.Row {
		switch {
		case strings.Contains(sql, "bridge_relayer_heartbeat"):
			return hb
		case strings.Contains(sql, "status = 'INITIATED'"):
			return intRow(stuck)
		default:
			return base.row(sql)
		}
	}}
}

func liveBridgeDeps(p Pinger, now func() time.Time) Deps {
	return Deps{
		Pool: p, Registry: twoChainRegistry(), Now: now,
		BridgeRPCChains: map[int]bool{84532: true},
	}
}

func TestRelayer_HealthyHeartbeat(t *testing.T) {
	now := fixedNow()
	hb := heartbeatRow{
		addr: "0x85acf278d93a3f8e58b15a82f54d8393aa44d38a",
		// One cycle ago.
		lastSeen: now().Add(-20 * time.Second), canDeliver: true,
		gas: `{"84532":"15000000000000000"}`,
	}
	rep := NewService(liveBridgeDeps(relayerPinger(0, hb), now)).Build(context.Background())

	r := rep.Bridge.Relayer
	if r == nil || r.Status != StatusHealthy {
		t.Fatalf("relayer = %+v, want healthy", r)
	}
	if !r.CanDeliver || r.Address == "" || r.LastSeenAt == nil {
		t.Errorf("relayer = %+v", r)
	}
	if r.GasWei["84532"] != "15000000000000000" {
		t.Errorf("gasWei = %v", r.GasWei)
	}
	for _, code := range []string{CodeRelayerAbsent, CodeRelayerStale, CodeRelayerLowGas, CodeTransfersStuck} {
		if hasWarning(rep, code) {
			t.Errorf("healthy relayer must not raise %s", code)
		}
	}
}

// The failure this whole feature exists for: gateways deployed, nothing
// delivering, and no other section showing a problem.
func TestRelayer_AbsentIsWarned(t *testing.T) {
	now := fixedNow()
	hb := heartbeatRow{err: errors.New("no rows in result set")}
	rep := NewService(liveBridgeDeps(relayerPinger(0, hb), now)).Build(context.Background())

	if rep.Bridge.Relayer == nil || rep.Bridge.Relayer.Status != "absent" {
		t.Fatalf("relayer = %+v, want absent", rep.Bridge.Relayer)
	}
	if !hasWarning(rep, CodeRelayerAbsent) {
		t.Errorf("a live bridge with no relayer must warn: %+v", rep.Warnings)
	}
}

func TestRelayer_StaleIsWarned(t *testing.T) {
	now := fixedNow()
	hb := heartbeatRow{
		addr: "0xabc", lastSeen: now().Add(-30 * time.Minute), canDeliver: true,
	}
	rep := NewService(liveBridgeDeps(relayerPinger(0, hb), now)).Build(context.Background())

	if rep.Bridge.Relayer.Status != "stale" {
		t.Errorf("status = %q, want stale", rep.Bridge.Relayer.Status)
	}
	if !hasWarning(rep, CodeRelayerStale) {
		t.Errorf("missing %s", CodeRelayerStale)
	}
}

// "Up but failing every 15 seconds" is what production actually did, and it is
// indistinguishable from health unless the error is carried through.
func TestRelayer_SurfacesTheCycleErrorVerbatim(t *testing.T) {
	now := fixedNow()
	msg := `relation "bridge_transfers" does not exist (SQLSTATE 42P01)`
	hb := heartbeatRow{
		addr: "0xabc", lastSeen: now().Add(-5 * time.Second), canDeliver: true, lastErr: &msg,
	}
	rep := NewService(liveBridgeDeps(relayerPinger(0, hb), now)).Build(context.Background())

	if rep.Bridge.Relayer.LastCycleError != msg {
		t.Errorf("lastCycleError = %q, want it verbatim", rep.Bridge.Relayer.LastCycleError)
	}
	// Live but failing is NOT healthy. Production reported a relayer cycling
	// every 15s with every cycle dying on an Infura 429 — "healthy" there would
	// be a green dot over a bridge that delivers nothing.
	if rep.Bridge.Relayer.Status != "failing" {
		t.Errorf("status = %q, want failing", rep.Bridge.Relayer.Status)
	}
	if !hasWarning(rep, CodeRelayerCycleErr) {
		t.Errorf("a relayer failing every cycle must warn: %+v", rep.Warnings)
	}
}

// A stale relayer is stale regardless of what its last cycle said: not running
// is the more serious fact, and two warnings for one condition is noise.
func TestRelayer_StaleOutranksFailing(t *testing.T) {
	now := fixedNow()
	msg := "some error"
	hb := heartbeatRow{
		addr: "0xabc", lastSeen: now().Add(-30 * time.Minute), canDeliver: true, lastErr: &msg,
	}
	rep := NewService(liveBridgeDeps(relayerPinger(0, hb), now)).Build(context.Background())

	if rep.Bridge.Relayer.Status != "stale" {
		t.Errorf("status = %q, want stale", rep.Bridge.Relayer.Status)
	}
	if hasWarning(rep, CodeRelayerCycleErr) {
		t.Errorf("stale already covers it; %s is redundant noise", CodeRelayerCycleErr)
	}
}

func TestRelayer_StuckTransfersAndLowGasWarn(t *testing.T) {
	now := fixedNow()
	hb := heartbeatRow{
		addr: "0xabc", lastSeen: now().Add(-5 * time.Second), canDeliver: true,
		gas: `{"84532":"1000"}`, // far below LowGasWei
	}
	rep := NewService(liveBridgeDeps(relayerPinger(3, hb), now)).Build(context.Background())

	if rep.Bridge.Relayer.StuckTransfers == nil || *rep.Bridge.Relayer.StuckTransfers != 3 {
		t.Errorf("stuckTransfers = %v", rep.Bridge.Relayer.StuckTransfers)
	}
	if !hasWarning(rep, CodeTransfersStuck) {
		t.Errorf("escrowed-but-undelivered deposits must warn")
	}
	if !hasWarning(rep, CodeRelayerLowGas) {
		t.Errorf("a relayer that will run out of gas must warn before it does")
	}
}

// A bridge with no gateway has nothing to relay, so a missing relayer there is
// not a problem to report — warning about it would train operators to skim.
func TestRelayer_NoWarningWhenNoBridgeIsDeployed(t *testing.T) {
	now := fixedNow()
	hb := heartbeatRow{err: errors.New("no rows")}
	rep := NewService(Deps{
		Pool: relayerPinger(0, hb), Registry: registryWithRouter(), Now: now,
	}).Build(context.Background())

	if hasWarning(rep, CodeRelayerAbsent) {
		t.Errorf("no gateway means no relayer is expected: %+v", rep.Warnings)
	}
}
