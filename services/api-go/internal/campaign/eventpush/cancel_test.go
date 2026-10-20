package eventpush

import (
	"context"
	"testing"
	"time"
)

// checkAccounting asserts that the capacity counter equals the live
// (reserved or active) subscriptions and that only active ones are routable.
func checkAccounting(t *testing.T, h *Hub) {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	live := 0
	for c := range h.clients {
		for k, sub := range c.subs {
			if sub.key != k || sub.client != c || !(sub.reserved || sub.active) || (sub.reserved && sub.active) {
				t.Fatalf("inconsistent subscription %+v under %v", sub, k)
			}
			live++
		}
	}
	for campaign, set := range h.byCampaign {
		for sub := range set {
			if !sub.active || sub.key.campaign != campaign || sub.client.subs[sub.key] != sub {
				t.Fatalf("routable subscription is not current: %+v", sub)
			}
		}
	}
	if h.total != live || h.total < 0 {
		t.Fatalf("capacity counter %d, live subscriptions %d", h.total, live)
	}
}

type pendingAck chan map[string]any

func subscribeAsync(s *Session, campaign, family string) pendingAck {
	done := make(pendingAck, 1)
	go func() {
		ack, after := s.Subscribe(context.Background(), map[string]any{"campaignId": campaign, "family": family})
		after()
		done <- ack
	}()
	return done
}

func (p pendingAck) wait(t *testing.T) map[string]any {
	t.Helper()
	select {
	case ack := <-p:
		return ack
	case <-time.After(5 * time.Second):
		t.Fatal("subscribe did not finish")
		return nil
	}
}

func entered(t *testing.T, o *fakeObserver, want string) {
	t.Helper()
	select {
	case got := <-o.entered:
		if got != want {
			t.Fatalf("entered %s, want %s", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not enter its observation", want)
	}
}

// expectSilentAfterChange advances the key's head, forces a sweep and requires
// that the connection receives nothing.
func expectSilentAfterChange(t *testing.T, h *Hub, o *fakeObserver, conn *fakeConn, campaign, family, owner string, head int64) {
	t.Helper()
	o.set(campaign, family, owner, head)
	h.markDirty(campaign)
	h.RequestSweep()
	conn.none(t, 150*time.Millisecond)
}

func TestUnsubscribeCancelsBlockedSubscribe(t *testing.T) {
	o := newObserver()
	h := testHub(t, o, nil)
	s, conn := attach(t, h, nil)
	gate := o.block("c1", "publication")
	done := subscribeAsync(s, "c1", "publication")
	entered(t, o, "c1|publication")
	if h.Stats().Subscriptions != 1 {
		t.Fatalf("reservation not counted: %+v", h.Stats())
	}
	mustSucceed(t, s.Unsubscribe(map[string]any{"campaignId": "c1", "family": "publication"}))
	if h.Stats().Subscriptions != 0 {
		t.Fatalf("PENDING_RESERVATION_SURVIVED_UNSUBSCRIBE: %+v", h.Stats())
	}
	close(gate)
	mustFail(t, done.wait(t), "cancelled")
	if got := h.Stats().Subscriptions; got != 0 {
		t.Fatalf("ACKNOWLEDGED_UNSUBSCRIBE_RESURRECTED: subscriptions=%d", got)
	}
	checkAccounting(t, h)
	expectSilentAfterChange(t, h, o, conn, "c1", "publication", "", 1)
}

func TestReplacementSubscribeIsNotDisturbedByOlderCompletion(t *testing.T) {
	for _, order := range []string{"old-first", "new-first", "old-fails-first"} {
		t.Run(order, func(t *testing.T) {
			o := newObserver()
			h := testHub(t, o, nil)
			s, conn := attach(t, h, nil)
			key := map[string]any{"campaignId": "c1", "family": "interaction"}
			oldGate := o.block("c1", "interaction")
			oldDone := subscribeAsync(s, "c1", "interaction")
			entered(t, o, "c1|interaction")
			mustSucceed(t, s.Unsubscribe(key))
			newGate := o.block("c1", "interaction")
			newDone := subscribeAsync(s, "c1", "interaction")
			entered(t, o, "c1|interaction")
			if h.Stats().Subscriptions != 1 {
				t.Fatalf("replacement reservation: %+v", h.Stats())
			}
			switch order {
			case "old-first", "old-fails-first":
				if order == "old-fails-first" {
					o.mu.Lock()
					o.hidden["c1"] = true
					o.mu.Unlock()
				}
				close(oldGate)
				mustFail(t, oldDone.wait(t), "cancelled")
				if h.Stats().Subscriptions != 1 {
					t.Fatalf("OLD_COMPLETION_REMOVED_REPLACEMENT: %+v", h.Stats())
				}
				checkAccounting(t, h)
				o.mu.Lock()
				delete(o.hidden, "c1")
				o.mu.Unlock()
				close(newGate)
				mustSucceed(t, newDone.wait(t))
			case "new-first":
				close(newGate)
				mustSucceed(t, newDone.wait(t))
				close(oldGate)
				mustFail(t, oldDone.wait(t), "cancelled")
			}
			if h.Stats().Subscriptions != 1 {
				t.Fatalf("replacement not active exactly once: %+v", h.Stats())
			}
			checkAccounting(t, h)
			o.set("c1", "interaction", "", 4)
			h.markDirty("c1")
			frame := conn.next(t)
			if frame.event != EventInvalidate || frame.payload["headVersion"] != "4" {
				t.Fatalf("frame = %v", frame)
			}
			conn.none(t, 100*time.Millisecond)
			mustSucceed(t, s.Unsubscribe(key))
			checkAccounting(t, h)
			if h.Stats().Subscriptions != 0 {
				t.Fatalf("capacity not returned: %+v", h.Stats())
			}
		})
	}
}

func TestStaleRefreshFailureCannotRemoveReplacement(t *testing.T) {
	o := newObserver()
	h := testHub(t, o, nil)
	s, conn := attach(t, h, nil)
	key := map[string]any{"campaignId": "c1", "family": "publication"}
	mustSucceed(t, subscribe(t, s, "c1", "publication"))
	drain(o)
	refreshGate := o.block("c1", "publication")
	refresh := subscribeAsync(s, "c1", "publication")
	entered(t, o, "c1|publication")
	mustSucceed(t, s.Unsubscribe(key))
	o.mu.Lock()
	delete(o.gate, "c1|publication")
	o.mu.Unlock()
	mustSucceed(t, subscribe(t, s, "c1", "publication"))
	drain(o)
	o.mu.Lock()
	o.hidden["c1"] = true
	o.mu.Unlock()
	close(refreshGate)
	mustFail(t, refresh.wait(t), "cancelled")
	if h.Stats().Subscriptions != 1 {
		t.Fatalf("STALE_FAILURE_REMOVED_REPLACEMENT: %+v", h.Stats())
	}
	checkAccounting(t, h)
	o.mu.Lock()
	delete(o.hidden, "c1")
	o.mu.Unlock()
	o.set("c1", "publication", "", 2)
	h.markDirty("c1")
	if frame := conn.next(t); frame.payload["headVersion"] != "2" {
		t.Fatalf("frame = %v", frame)
	}
}

func TestRepeatedUnsubscribeIsIdempotent(t *testing.T) {
	o := newObserver()
	h := testHub(t, o, func(c *Config) { c.RateLimit = 2 })
	s, conn := attach(t, h, nil)
	mustSucceed(t, subscribe(t, s, "c1", "publication"))
	for i := 0; i < 5; i++ {
		ack := s.Unsubscribe(map[string]any{"campaignId": "c1", "family": "publication"})
		mustSucceed(t, ack)
		checkAccounting(t, h)
		if h.Stats().Subscriptions != 0 {
			t.Fatalf("unsubscribe %d: %+v", i, h.Stats())
		}
	}
	mustSucceed(t, s.Unsubscribe(map[string]any{"campaignId": "never", "family": "personal"}))
	expectSilentAfterChange(t, h, o, conn, "c1", "publication", "", 1)
}

func TestObservationFailureOrTimeoutReleasesReservationOnce(t *testing.T) {
	t.Run("failure", func(t *testing.T) {
		o := newObserver()
		h := testHub(t, o, nil)
		s, conn := attach(t, h, nil)
		gate := o.block("c1", "publication")
		done := subscribeAsync(s, "c1", "publication")
		entered(t, o, "c1|publication")
		o.mu.Lock()
		o.failing["c1"] = domain("CAMPAIGN_UNAVAILABLE")
		o.mu.Unlock()
		close(gate)
		mustFail(t, done.wait(t), "unavailable")
		checkAccounting(t, h)
		if h.Stats().Subscriptions != 0 {
			t.Fatalf("failed reservation retained: %+v", h.Stats())
		}
		mustSucceed(t, s.Unsubscribe(map[string]any{"campaignId": "c1", "family": "publication"}))
		checkAccounting(t, h)
		expectSilentAfterChange(t, h, o, conn, "c1", "publication", "", 1)
	})
	for _, cancelFirst := range []bool{false, true} {
		name := "timeout"
		if cancelFirst {
			name = "timeout-after-unsubscribe"
		}
		t.Run(name, func(t *testing.T) {
			o := newObserver()
			h := testHub(t, o, func(c *Config) { c.ObserveTimeout = 200 * time.Millisecond })
			s, conn := attach(t, h, nil)
			o.block("c1", "publication") // never released: only the timeout ends it
			done := subscribeAsync(s, "c1", "publication")
			entered(t, o, "c1|publication")
			want := "unavailable"
			if cancelFirst {
				mustSucceed(t, s.Unsubscribe(map[string]any{"campaignId": "c1", "family": "publication"}))
				want = "cancelled"
			}
			mustFail(t, done.wait(t), want)
			checkAccounting(t, h)
			if h.Stats().Subscriptions != 0 {
				t.Fatalf("timed-out reservation retained: %+v", h.Stats())
			}
			o.mu.Lock()
			delete(o.gate, "c1|publication")
			o.mu.Unlock()
			expectSilentAfterChange(t, h, o, conn, "c1", "publication", "", 1)
		})
	}
}

func TestDisconnectOrShutdownDuringObservation(t *testing.T) {
	for _, mode := range []string{"disconnect", "shutdown", "unsubscribe-then-disconnect"} {
		t.Run(mode, func(t *testing.T) {
			o := newObserver()
			cfg := func(c *Config) { c.ObserveTimeout = time.Minute }
			h := testHub(t, o, cfg)
			s, conn := attach(t, h, map[string]any{"token": "tokA"})
			other, otherConn := attach(t, h, nil)
			mustSucceed(t, subscribe(t, other, "c2", "publication"))
			drain(o)
			o.block("c1", "personal") // never released: only cancellation can end it
			done := subscribeAsync(s, "c1", "personal")
			entered(t, o, "c1|personal")
			switch mode {
			case "disconnect":
				s.Close()
			case "unsubscribe-then-disconnect":
				mustSucceed(t, s.Unsubscribe(map[string]any{"campaignId": "c1", "family": "personal"}))
				s.Close()
			case "shutdown":
				h.Close()
			}
			mustFail(t, done.wait(t), "unavailable")
			s.Close()
			s.Close()
			mustSucceed(t, s.Unsubscribe(map[string]any{"campaignId": "c1", "family": "personal"}))
			checkAccounting(t, h)
			conn.none(t, 50*time.Millisecond)
			if mode == "shutdown" {
				if h.Stats() != (Stats{}) {
					t.Fatalf("stats after shutdown: %+v", h.Stats())
				}
				return
			}
			if got := h.Stats(); got.Subscriptions != 1 || got.Connections != 1 {
				t.Fatalf("disconnect released the wrong subscriptions: %+v", got)
			}
			o.set("c1", "personal", "0xa", 3)
			o.set("c2", "publication", "", 3)
			h.RequestSweep()
			if frame := otherConn.next(t); frame.payload["campaignId"] != "c2" {
				t.Fatalf("unrelated frame = %v", frame)
			}
			conn.none(t, 100*time.Millisecond)
		})
	}
}

func TestRevocationFoundByCancelledSubscribeStillEndsConnection(t *testing.T) {
	o := newObserver()
	h := testHub(t, o, nil)
	s, conn := attach(t, h, map[string]any{"token": "tokA"})
	mustSucceed(t, subscribe(t, s, "c2", "personal"))
	drain(o)
	gate := o.block("c1", "personal")
	done := subscribeAsync(s, "c1", "personal")
	entered(t, o, "c1|personal")
	mustSucceed(t, s.Unsubscribe(map[string]any{"campaignId": "c1", "family": "personal"}))
	o.mu.Lock()
	o.state["tokA"] = "revoked"
	o.mu.Unlock()
	close(gate)
	mustFail(t, done.wait(t), "unauthorized")
	frame := conn.next(t)
	if frame.event != EventReset || frame.payload["campaignId"] != "c2" || frame.payload["reason"] != "unauthorized" {
		t.Fatalf("frame = %v", frame)
	}
	conn.none(t, 50*time.Millisecond)
	if !conn.isClosed() || h.Stats() != (Stats{}) {
		t.Fatalf("revoked connection retained: %+v", h.Stats())
	}
}
