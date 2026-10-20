package eventpush

import (
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"
)

// Ported from the independent review's counterexample: one hot campaign keeps
// replenishing urgent work while a revoked private subscription waits in the
// sweep queue. Progress must not depend on the hot traffic stopping.
func TestContinuousWakeupsDoNotStarveQueuedSweep(t *testing.T) {
	o := newObserver()
	h := testHub(t, o, func(c *Config) { c.Concurrency = 1; c.SweepInterval = time.Second })
	hot, _ := attach(t, h, nil)
	cold, coldConn := attach(t, h, map[string]any{"token": "tokA"})
	mustSucceed(t, subscribe(t, hot, "c1", "interaction"))
	mustSucceed(t, subscribe(t, cold, "c2", "personal"))
	drain(o)
	o.mu.Lock()
	o.state["tokA"] = "revoked"
	o.mu.Unlock()
	gate := o.block("c1", "interaction")
	h.markDirty("c1")
	entered(t, o, "c1|interaction")
	h.RequestSweep()
	waitFor(t, func() bool { return h.Stats().Pending >= 2 })
	hotUnit := unit{key: key{campaign: "c1", family: "interaction"}}
	for wave := 0; wave < 32; wave++ {
		nextGate := o.block("c1", "interaction")
		h.markDirty("c1")
		waitFor(t, func() bool { h.mu.Lock(); defer h.mu.Unlock(); return h.queued[hotUnit] || h.again[hotUnit] })
		close(gate)
		gate = nextGate
		select {
		case got := <-o.entered:
			if got == "c2|personal" {
				if wave > 1 {
					t.Fatalf("SWEEP_STARVED_BY_CONTINUOUS_WAKEUPS: queued sweep unit started only after %d hot observations", wave)
				}
				// Hot traffic is still being generated: the next wave is queued.
				waitFor(t, coldConn.isClosed)
				close(gate)
				return
			}
			if got != "c1|interaction" {
				t.Fatalf("unexpected observation %s", got)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("scheduler did not dispatch")
		}
	}
	close(gate)
	t.Fatal("SWEEP_STARVED_BY_CONTINUOUS_WAKEUPS: 32 hot observations bypassed a queued revoked private subscription")
}

// sequence drains the observer's entry channel so never-ending hot work does
// not block, recording the order in which observations started.
type sequence struct {
	mu   sync.Mutex
	seen []string
	stop chan struct{}
	done chan struct{}
}

func record(o *fakeObserver) *sequence {
	s := &sequence{stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(s.done)
		for {
			select {
			case got := <-o.entered:
				s.mu.Lock()
				s.seen = append(s.seen, got)
				s.mu.Unlock()
			case <-s.stop:
				return
			}
		}
	}()
	return s
}

func (s *sequence) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.seen...)
}

func (s *sequence) close() { close(s.stop); <-s.done }

func count(seen []string, want string) int {
	n := 0
	for _, got := range seen {
		if got == want {
			n++
		}
	}
	return n
}

// selfRenewing makes every observation of the hot stream advance its head and
// request another wakeup, so urgent work never runs out. With acknowledgements
// delayed, the owed-hint path re-promotes the same unit as well.
func selfRenewing(t *testing.T, h *Hub, o *fakeObserver, campaign, family string, conn *fakeConn, ackDelay time.Duration) func() {
	t.Helper()
	var head int64
	stopped := make(chan struct{})
	var once sync.Once
	o.mu.Lock()
	o.after[campaign+"|"+family] = func() {
		select {
		case <-stopped:
			return
		default:
		}
		head++
		o.set(campaign, family, "", head)
		h.markDirty(campaign)
	}
	o.mu.Unlock()
	acked := make(chan struct{})
	go func() {
		defer close(acked)
		for {
			select {
			case frame := <-conn.notify:
				if frame.ack != nil {
					time.Sleep(ackDelay)
					frame.ack(true)
				}
			case <-stopped:
				return
			}
		}
	}()
	return func() {
		once.Do(func() {
			close(stopped)
			<-acked
		})
	}
}

func TestQueuedSweepUnitsStartWithinTwiceTheirPosition(t *testing.T) {
	for _, mode := range []struct {
		name     string
		ackDelay time.Duration
	}{{"continuous-wakeups", 0}, {"wakeups-with-owed-hints", 2 * time.Millisecond}} {
		t.Run(mode.name, func(t *testing.T) {
			o := newObserver()
			h := testHub(t, o, func(c *Config) { c.Concurrency = 1 })
			hot, hotConn := attach(t, h, nil)
			cold, _ := attach(t, h, map[string]any{"token": "tokA"})
			mustSucceed(t, subscribe(t, hot, "hot", "interaction"))
			const quiet = 12
			for i := 0; i < quiet; i++ {
				mustSucceed(t, subscribe(t, cold, fmt.Sprintf("q%02d", i), "personal"))
			}
			drain(o)
			seq := record(o)
			defer seq.close()
			stop := selfRenewing(t, h, o, "hot", "interaction", hotConn, mode.ackDelay)
			defer stop()
			h.markDirty("hot")
			waitFor(t, func() bool { return count(seq.snapshot(), "hot|interaction") >= 5 })
			// Hold one hot observation so the sweep is fully queued before the
			// measured window opens; the hold is released at once afterwards.
			gate := o.block("hot", "interaction")
			held := count(seq.snapshot(), "hot|interaction")
			// Self-renewing observations restart within microseconds, so a count
			// that stays still while the gate is installed means one is blocked.
			waitFor(t, func() bool {
				before := count(seq.snapshot(), "hot|interaction")
				time.Sleep(20 * time.Millisecond)
				return before > held && count(seq.snapshot(), "hot|interaction") == before
			})
			h.RequestSweep()
			waitFor(t, func() bool {
				h.mu.Lock()
				defer h.mu.Unlock()
				waiting := 0
				for _, urgent := range h.queued {
					if !urgent {
						waiting++
					}
				}
				return waiting >= quiet
			})
			start := len(seq.snapshot())
			o.mu.Lock()
			delete(o.gate, "hot|interaction")
			o.mu.Unlock()
			close(gate)
			// Each queued sweep unit must start within twice its position; one
			// observation may already be running when the window opens.
			bound := 2*quiet + 4
			waitFor(t, func() bool { return len(seq.snapshot()) >= start+bound })
			window := seq.snapshot()[start : start+bound]
			for i := 0; i < quiet; i++ {
				if count(window, fmt.Sprintf("q%02d|personal", i)) == 0 {
					t.Fatalf("SWEEP_PROGRESS_UNBOUNDED: q%02d not observed within %d observations: %v", i, bound, window)
				}
			}
			// Hot work kept arriving throughout; it was not stopped to make room.
			if count(window, "hot|interaction") < quiet/2 {
				t.Fatalf("wakeups were not served alongside the sweep: %v", window)
			}
		})
	}
}

func TestRevocationWithoutEventsUnderContinuousWakeupsAndCleanup(t *testing.T) {
	goroutines := runtime.NumGoroutine()
	o := newObserver()
	h := testHub(t, o, func(c *Config) { c.Concurrency = 1; c.SweepInterval = 50 * time.Millisecond })
	hot, hotConn := attach(t, h, nil)
	cold, coldConn := attach(t, h, map[string]any{"token": "tokA"})
	other, _ := attach(t, h, map[string]any{"token": "tokB"})
	mustSucceed(t, subscribe(t, hot, "hot", "interaction"))
	for i := 0; i < 8; i++ {
		mustSucceed(t, subscribe(t, cold, fmt.Sprintf("r%02d", i), "personal"))
		mustSucceed(t, subscribe(t, other, fmt.Sprintf("r%02d", i), "personal"))
	}
	drain(o)
	seq := record(o)
	stop := selfRenewing(t, h, o, "hot", "interaction", hotConn, time.Millisecond)
	h.markDirty("hot")
	waitFor(t, func() bool { return count(seq.snapshot(), "hot|interaction") >= 20 })
	// No event touches the quiet streams: only periodic sweeps can see this.
	o.mu.Lock()
	o.state["tokA"] = "revoked"
	o.mu.Unlock()
	revokedAt := time.Now()
	hotBefore := count(seq.snapshot(), "hot|interaction")
	deadline := time.After(2 * time.Second)
	for !coldConn.isClosed() {
		select {
		case <-deadline:
			t.Fatalf("REVOCATION_STARVED: revoked connection still open after 2s of continuous wakeups (stats %+v)", h.Stats())
		case <-time.After(5 * time.Millisecond):
		}
	}
	elapsed := time.Since(revokedAt)
	if count(seq.snapshot(), "hot|interaction") <= hotBefore {
		t.Fatal("hot wakeups stopped; the test no longer proves progress under load")
	}
	if s := h.Stats(); s.Subscriptions != 9 || s.Connections != 2 {
		t.Fatalf("revocation released the wrong subscriptions: %+v", s)
	}
	t.Logf("revoked connection closed %s after revocation, under continuous wakeups", elapsed)

	// Cleanup under load: closing connections and the hub drops queued work
	// and joins every owned goroutine.
	other.Close()
	stop()
	hot.Close()
	seq.close()
	h.Close()
	if s := h.Stats(); s != (Stats{}) {
		t.Fatalf("stats after close: %+v", s)
	}
	waitFor(t, func() bool { return runtime.NumGoroutine() <= goroutines })
}

func TestCloseDropsQueuedWork(t *testing.T) {
	o := newObserver()
	h := testHub(t, o, func(c *Config) { c.Concurrency = 1; c.ObserveTimeout = time.Minute })
	s, _ := attach(t, h, map[string]any{"token": "tokA"})
	for i := 0; i < 8; i++ {
		mustSucceed(t, subscribe(t, s, fmt.Sprintf("d%02d", i), "personal"))
	}
	drain(o)
	o.block("d00", "personal") // never released: only shutdown ends it
	h.markDirty("d00")
	entered(t, o, "d00|personal")
	h.RequestSweep()
	h.markDirty("d03")
	waitFor(t, func() bool { return h.Stats().Pending >= 7 })
	h.Close()
	h.RequestSweep()
	h.markDirty("d05")
	if stats := h.Stats(); stats != (Stats{}) {
		t.Fatalf("QUEUED_WORK_SURVIVED_CLOSE: %+v", stats)
	}
}
