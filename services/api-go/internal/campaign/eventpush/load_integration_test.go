package eventpush

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth/authtest"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/eventfeed"
	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/application"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/postgres"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/durableoutbox"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/web3session"
	"github.com/jackc/pgx/v5/pgxpool"
)

// meter wraps the production observer to measure observation latency and
// concurrency without changing what it does.
type meter struct {
	inner   Observer
	calls   atomic.Int64
	active  atomic.Int64
	maxSeen atomic.Int64
	mu      sync.Mutex
	samples []time.Duration
	// distinct, when non-nil, collects each observed (subject, stream) once.
	distinct map[string]struct{}
}

func (m *meter) Authenticate(ctx context.Context, token string) (*eventfeed.Subject, error) {
	return m.inner.Authenticate(ctx, token)
}

func (m *meter) Observe(ctx context.Context, subject *eventfeed.Subject, id, family string) (int64, error) {
	n := m.active.Add(1)
	for {
		seen := m.maxSeen.Load()
		if n <= seen || m.maxSeen.CompareAndSwap(seen, n) {
			break
		}
	}
	start := time.Now()
	head, err := m.inner.Observe(ctx, subject, id, family)
	elapsed := time.Since(start)
	m.active.Add(-1)
	m.calls.Add(1)
	m.mu.Lock()
	m.samples = append(m.samples, elapsed)
	if m.distinct != nil && err == nil {
		m.distinct[fmt.Sprintf("%p|%s|%s", subject, id, family)] = struct{}{}
	}
	m.mu.Unlock()
	return head, err
}

func (m *meter) take() []time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.samples
	m.samples = nil
	return out
}

func quantiles(samples []time.Duration) map[string]float64 {
	if len(samples) == 0 {
		return nil
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	at := func(q float64) float64 { return float64(samples[int(q*float64(len(samples)-1))].Microseconds()) / 1000 }
	return map[string]float64{"n": float64(len(samples)), "p50ms": at(0.5), "p95ms": at(0.95), "p99ms": at(0.99), "maxms": at(1)}
}

// probe measures what an unrelated request sees from the shared pool while push
// work runs: acquiring a connection plus one trivial query.
func probe(ctx context.Context, pool *pgxpool.Pool, every time.Duration) func() []time.Duration {
	var mu sync.Mutex
	var samples []time.Duration
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				start := time.Now()
				var one int
				if err := pool.QueryRow(ctx, `SELECT 1`).Scan(&one); err == nil {
					mu.Lock()
					samples = append(samples, time.Since(start))
					mu.Unlock()
				}
			}
		}
	}()
	return func() []time.Duration {
		close(done)
		<-stopped
		mu.Lock()
		defer mu.Unlock()
		return samples
	}
}

func transactions(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	var n int64
	if err := pool.QueryRow(context.Background(), `SELECT xact_commit + xact_rollback FROM pg_stat_database WHERE datname=current_database()`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func settle(t *testing.T, h *Hub, m *meter, want int64, limit time.Duration) time.Duration {
	t.Helper()
	start := time.Now()
	for {
		s := h.Stats()
		if m.calls.Load() >= want && s.Inflight == 0 && s.Pending == 0 {
			return time.Since(start)
		}
		if time.Since(start) > limit {
			t.Fatalf("sweep did not settle within %s: calls=%d want=%d stats=%+v", limit, m.calls.Load(), want, s)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestPushCostAtSubscriptionCap measures the worst-case private load: every one
// of the process's subscriptions is a separately authorized personal stream.
// It writes its measurements to CAMPAIGN_PUSH_LOAD_REPORT.
func TestPushCostAtSubscriptionCap(t *testing.T) {
	report := os.Getenv("CAMPAIGN_PUSH_LOAD_REPORT")
	if report == "" {
		t.Skip("load measurement requires CAMPAIGN_PUSH_LOAD_REPORT and the migrated database fixture")
	}
	pool := database(t)
	ctx := context.Background()
	sessions := web3session.New(pool)
	f := authtest.New(t, func(p *auth.Policy) { p.Sessions = sessions })
	commands := application.NewService(postgres.New(pool), allow{}, func() v2.Capabilities { return v2.Capabilities{Templates: map[string]bool{"campaignCardV1": true}} })
	const campaigns, connections = 16, 128
	ids := make([]string, campaigns)
	pubs := make([]application.Result, campaigns)
	for i := range ids {
		revision := revisionInput()
		draft := execute(t, commands, walletA, application.Command{Operation: "create", Revision: &revision})
		pubs[i] = execute(t, commands, walletA, application.Command{Operation: "publish", CampaignID: draft.CampaignID, ExpectedPublication: &draft.PublicationVersion, CandidateRevisionID: draft.RevisionID})
		ids[i] = pubs[i].CampaignID
	}
	tokens := make([]string, connections)
	wallets := make([]string, connections)
	for i := range tokens {
		wallets[i] = fmt.Sprintf("0x%040x", i+16)
		tokens[i], _ = f.Login(t, wallets[i], 11155111)
	}
	run := 0
	// join is a genuine Campaign command for the wallet behind one connection;
	// each measurement uses a different wallet and campaign so it is a first join.
	join := func() (int, string) {
		run++
		zero := uint64(0)
		execute(t, commands, wallets[run], application.Command{Operation: "join", CampaignID: ids[run], ExpectedPublication: &pubs[run].PublicationVersion, ExpectedState: &zero})
		return run, ids[run]
	}

	results := map[string]any{"campaigns": campaigns, "connections": connections, "numCPU": runtime.NumCPU()}
	for _, poolSize := range []int{0, 4} {
		label := "defaultPool"
		measured := pool
		if poolSize > 0 {
			label = fmt.Sprintf("pool%d", poolSize)
			cfg := pool.Config().Copy()
			cfg.MaxConns = int32(poolSize)
			var err error
			if measured, err = pgxpool.NewWithConfig(ctx, cfg); err != nil {
				t.Fatal(err)
			}
		}
		results[label] = measureCap(t, measured, f.Verifier, ids, tokens, join)
		if poolSize > 0 {
			measured.Close()
		}
	}
	body, _ := json.MarshalIndent(results, "", "  ")
	if err := os.WriteFile(report, append(body, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Log(string(body))
}

func measureCap(t *testing.T, pool *pgxpool.Pool, verifier *auth.Verifier, ids, tokens []string, join func() (int, string)) map[string]any {
	ctx := context.Background()
	cfg := DefaultConfig()
	if os.Getenv("CAMPAIGN_PUSH_LOAD_POOL_BOUND") == "true" {
		cfg = cfg.BoundedBy(pool.Config().MaxConns) // as composed by cmd/api
	}
	cfg.SweepInterval = time.Hour // sweeps are requested explicitly
	m := &meter{inner: eventfeed.New(pool, verifier)}
	goroutines := runtime.NumGoroutine()
	var before runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	h := New(m, durableoutbox.NewRepository(pool), cfg)
	defer h.Close()
	out := map[string]any{"poolMaxConns": pool.Config().MaxConns, "hubConcurrency": cfg.Concurrency, "observeTimeoutMs": cfg.ObserveTimeout.Milliseconds(), "sweepIntervalDefaultMs": DefaultConfig().SweepInterval.Milliseconds()}

	idle := probe(ctx, pool, 10*time.Millisecond)
	time.Sleep(500 * time.Millisecond)
	out["probeIdle"] = quantiles(idle())

	sessions := make([]*Session, len(tokens))
	conns := make([]*fakeConn, len(tokens))
	start := time.Now()
	var wg sync.WaitGroup
	var failed atomic.Int64
	for i, token := range tokens {
		identity, code := h.Authenticate(ctx, map[string]any{"token": token})
		if code != "" {
			t.Fatalf("authenticate %d: %s", i, code)
		}
		conns[i] = newConn()
		sessions[i] = h.Attach(identity, conns[i])
		wg.Add(1)
		go func(s *Session) {
			defer wg.Done()
			for _, id := range ids {
				if ack, _ := s.Subscribe(ctx, map[string]any{"campaignId": id, "family": "personal"}); ack["success"] != true {
					failed.Add(1)
				}
			}
		}(sessions[i])
	}
	wg.Wait()
	out["subscribeAll"] = map[string]any{"ms": time.Since(start).Milliseconds(), "failed": failed.Load(), "subscriptions": h.Stats().Subscriptions, "observations": quantiles(m.take())}
	if h.Stats().Subscriptions != cfg.Total || failed.Load() != 0 {
		t.Fatalf("cap not reached: %+v failed=%d", h.Stats(), failed.Load())
	}
	extra, _ := attach(t, h, map[string]any{"token": tokens[0]})
	out["beyondCap"] = subscribe(t, extra, ids[0], "publication")["code"]
	extra.Close()

	var sweeps []map[string]any
	for i := 0; i < 3; i++ {
		calls, xacts := m.calls.Load(), transactions(t, pool)
		m.maxSeen.Store(0)
		busy := probe(ctx, pool, 10*time.Millisecond)
		h.RequestSweep()
		elapsed := settle(t, h, m, calls+int64(cfg.Total), time.Minute)
		observed := m.calls.Load() - calls
		time.Sleep(700 * time.Millisecond) // PostgreSQL 14 publishes backend statistics with a delay
		sweeps = append(sweeps, map[string]any{"ms": elapsed.Milliseconds(), "observations": observed, "dbTransactionsApprox": transactions(t, pool) - xacts,
			"maxConcurrentObservations": m.maxSeen.Load(), "observation": quantiles(m.take()), "unrelatedPoolQueryDuringSweep": quantiles(busy())})
	}
	out["sweeps"] = sweeps
	var during runtime.MemStats
	runtime.ReadMemStats(&during)
	out["heapDeltaBytes"] = int64(during.HeapAlloc) - int64(before.HeapAlloc)
	out["goroutinesWhileSubscribed"] = runtime.NumGoroutine() - goroutines

	// Wakeup latency for a genuine private mutation, idle and while a full sweep
	// is queued ahead of it.
	hint := func(during bool) int64 {
		if during {
			calls := m.calls.Load()
			h.RequestSweep()
			for m.calls.Load() < calls+64 {
				time.Sleep(time.Millisecond)
			}
		}
		i, campaign := join()
		start := time.Now()
		h.markDirty(campaign)
		for {
			frame := conns[i].next(t)
			if frame.payload["campaignId"] == campaign && frame.payload["family"] == "personal" {
				frame.ack(true)
				return time.Since(start).Milliseconds()
			}
		}
	}
	out["wakeupHintIdleMs"] = []int64{hint(false), hint(false)}
	out["wakeupHintDuringSweepMs"] = []int64{hint(true), hint(true), hint(true)}
	settle(t, h, m, 0, time.Minute)

	// Sustained arrivals: wakeups for rotating campaigns arrive every 10 ms
	// (each re-observes that campaign's 128 private subscriptions, far above
	// what the bounded concurrency can serve) while a full sweep must still
	// reach every subscription, then every connection closes under load.
	arrivals := make(chan struct{})
	arrived := atomic.Int64{}
	var arrivalsDone sync.WaitGroup
	arrivalsDone.Add(1)
	go func() {
		defer arrivalsDone.Done()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for i := 0; ; i++ {
			select {
			case <-arrivals:
				return
			case <-ticker.C:
				h.markDirty(ids[i%len(ids)])
				arrived.Add(1)
			}
		}
	}()
	time.Sleep(time.Second) // let the urgent queue saturate first
	sustained := map[string]any{"wakeupIntervalMs": 10}
	var sweepsUnderLoad []map[string]any
	for i := 0; i < 2; i++ {
		m.mu.Lock()
		m.distinct = map[string]struct{}{}
		m.mu.Unlock()
		calls := m.calls.Load()
		start := time.Now()
		h.RequestSweep()
		reached := false
		for time.Since(start) < 3*time.Minute {
			m.mu.Lock()
			n := len(m.distinct)
			m.mu.Unlock()
			if n >= cfg.Total {
				reached = true
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		sweepsUnderLoad = append(sweepsUnderLoad, map[string]any{"allSubscriptionsObserved": reached, "ms": time.Since(start).Milliseconds(),
			"observations": m.calls.Load() - calls, "pendingAtCompletion": h.Stats().Pending, "observation": quantiles(m.take())})
		if !reached {
			t.Fatalf("SUSTAINED_SWEEP_INCOMPLETE: sweep did not reach all %d subscriptions within 3 minutes under sustained wakeups", cfg.Total)
		}
	}
	m.mu.Lock()
	m.distinct = nil
	m.mu.Unlock()
	sustained["fullSweepsUnderLoad"] = sweepsUnderLoad
	busyQuery := probe(ctx, pool, 10*time.Millisecond)
	time.Sleep(time.Second)
	sustained["unrelatedPoolQueryUnderLoad"] = quantiles(busyQuery())
	closeStart := time.Now()
	for _, s := range sessions {
		s.Close()
	}
	sustained["closeAllCallsMs"] = time.Since(closeStart).Milliseconds()
	idleStart := time.Now()
	for s := h.Stats(); s.Inflight != 0 || s.Pending != 0 || s.Subscriptions != 0; s = h.Stats() {
		if time.Since(idleStart) > 10*time.Second {
			t.Fatalf("cleanup under sustained wakeups did not settle: %+v", s)
		}
		time.Sleep(time.Millisecond)
	}
	sustained["untilIdleWhileArrivalsContinueMs"] = time.Since(idleStart).Milliseconds()
	callsAfterIdle := m.calls.Load()
	time.Sleep(200 * time.Millisecond)
	sustained["observationsIn200msAfterIdle"] = m.calls.Load() - callsAfterIdle
	close(arrivals)
	arrivalsDone.Wait()
	sustained["wakeupsArrived"] = arrived.Load()
	out["sustainedArrivals"] = sustained
	for i, token := range tokens {
		identity, _ := h.Authenticate(ctx, map[string]any{"token": token})
		sessions[i] = h.Attach(identity, newConn())
		for _, id := range ids {
			sessions[i].Subscribe(ctx, map[string]any{"campaignId": id, "family": "personal"})
		}
	}
	settle(t, h, m, 0, time.Minute)

	// Cancellation responsiveness: every connection closes while a sweep runs.
	calls := m.calls.Load()
	h.RequestSweep()
	for m.calls.Load() < calls+64 {
		time.Sleep(time.Millisecond)
	}
	start = time.Now()
	for _, s := range sessions {
		s.Close()
	}
	closeMs := time.Since(start).Milliseconds()
	settleStart := time.Now()
	for s := h.Stats(); s.Inflight != 0 || s.Pending != 0; s = h.Stats() {
		if time.Since(settleStart) > 10*time.Second {
			t.Fatalf("cancellation did not settle: %+v", s)
		}
		time.Sleep(time.Millisecond)
	}
	out["disconnectAllDuringSweep"] = map[string]any{"closeCallsMs": closeMs, "untilIdleMs": time.Since(settleStart).Milliseconds(), "observationsAfterClose": m.calls.Load() - calls, "stats": h.Stats()}

	// Shutdown responsiveness with a full sweep in flight.
	for _, token := range tokens[:len(tokens)/2] {
		identity, _ := h.Authenticate(ctx, map[string]any{"token": token})
		s := h.Attach(identity, newConn())
		for _, id := range ids {
			s.Subscribe(ctx, map[string]any{"campaignId": id, "family": "personal"})
		}
	}
	calls = m.calls.Load()
	h.RequestSweep()
	for m.calls.Load() < calls+32 {
		time.Sleep(time.Millisecond)
	}
	start = time.Now()
	h.Close()
	out["shutdownDuringSweepMs"] = time.Since(start).Milliseconds()
	waitFor(t, func() bool { return pool.Stat().AcquiredConns() == 0 })
	waitFor(t, func() bool { return runtime.NumGoroutine() <= goroutines+2 })
	out["goroutinesAfterShutdown"] = runtime.NumGoroutine() - goroutines
	return out
}
