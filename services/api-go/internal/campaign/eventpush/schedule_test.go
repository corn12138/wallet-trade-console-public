package eventpush

import (
	"fmt"
	"testing"
	"time"
)

func TestWakeupObservationPrecedesQueuedSweep(t *testing.T) {
	o := newObserver()
	h := testHub(t, o, func(c *Config) { c.Concurrency = 1 })
	s, conn := attach(t, h, nil)
	s2, conn2 := attach(t, h, nil)
	for i := 0; i < 16; i++ {
		mustSucceed(t, subscribe(t, s, fmt.Sprintf("s%02d", i), "publication"))
		mustSucceed(t, subscribe(t, s2, fmt.Sprintf("t%02d", i), "publication"))
	}
	other, target := attach(t, h, nil)
	mustSucceed(t, subscribe(t, other, "hot", "interaction"))
	drain(o)
	// Occupy the only slot, queue a full sweep behind it, then wake one campaign.
	gate := o.block("s00", "publication")
	h.markDirty("s00")
	entered(t, o, "s00|publication")
	h.RequestSweep()
	waitFor(t, func() bool { return h.Stats().Pending >= 32 })
	o.set("hot", "interaction", "", 1)
	h.markDirty("hot")
	waitFor(t, func() bool { h.mu.Lock(); defer h.mu.Unlock(); return len(h.dirty) == 0 })
	close(gate)
	// Fairness may interleave one sweep unit, never the 32-unit sweep backlog.
	var started []string
	for len(started) < 2 && (len(started) == 0 || started[len(started)-1] != "hot|interaction") {
		started = append(started, <-o.entered)
	}
	if started[len(started)-1] != "hot|interaction" {
		t.Fatalf("WAKEUP_WAITED_BEHIND_SWEEP: observations after release %v", started)
	}
	if frame := target.next(t); frame.payload["campaignId"] != "hot" {
		t.Fatalf("frame = %v", frame)
	}
	conn.none(t, 0)
	conn2.none(t, 0)
}

func TestQueuedWorkOfClosedConnectionIsDropped(t *testing.T) {
	o := newObserver()
	h := testHub(t, o, func(c *Config) { c.Concurrency = 1 })
	closing, _ := attach(t, h, map[string]any{"token": "tokA"})
	for i := 0; i < 16; i++ {
		mustSucceed(t, subscribe(t, closing, fmt.Sprintf("k%02d", i), "personal"))
	}
	keep, _ := attach(t, h, nil)
	mustSucceed(t, subscribe(t, keep, "pub", "publication"))
	drain(o)
	gate := o.block("pub", "publication")
	h.markDirty("pub")
	entered(t, o, "pub|publication")
	h.RequestSweep()
	waitFor(t, func() bool { return h.Stats().Pending >= 16 })
	before := 0
	for i := 0; i < 16; i++ {
		before += o.callsFor(fmt.Sprintf("k%02d", i), "personal")
	}
	closing.Close()
	close(gate)
	settleHub(t, h)
	after := 0
	for i := 0; i < 16; i++ {
		after += o.callsFor(fmt.Sprintf("k%02d", i), "personal")
	}
	if after != before {
		t.Fatalf("CLOSED_CONNECTION_WORK_OBSERVED: %d observations after close", after-before)
	}
}

func settleHub(t *testing.T, h *Hub) {
	t.Helper()
	waitFor(t, func() bool { s := h.Stats(); return s.Pending == 0 && s.Inflight == 0 })
	time.Sleep(20 * time.Millisecond)
	waitFor(t, func() bool { s := h.Stats(); return s.Pending == 0 && s.Inflight == 0 })
}

func TestConcurrencyIsBoundedByThePool(t *testing.T) {
	for _, tc := range []struct {
		pool int32
		want int
	}{{1, 1}, {2, 1}, {4, 1}, {5, 2}, {10, 4}, {16, 7}, {17, 8}, {100, 8}} {
		if got := DefaultConfig().BoundedBy(tc.pool).Concurrency; got != tc.want {
			t.Fatalf("POOL_BOUND_EXCEEDED: pool %d concurrency %d, want %d", tc.pool, got, tc.want)
		}
	}
}
