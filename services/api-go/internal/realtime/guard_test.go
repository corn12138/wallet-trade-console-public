package realtime

import (
	"fmt"
	"testing"
)

func TestGuardUnknownSocket(t *testing.T) {
	g := newSubscriptionGuard()
	if rej := g.trySubscribe("nope", "r1"); rej == nil || rej.Code != "unknown_socket" {
		t.Fatalf("want unknown_socket, got %+v", rej)
	}
}

func TestGuardSubscribeIdempotentAndBudget(t *testing.T) {
	g := newSubscriptionGuard()
	// Advancing clock so the rate limit (60/10s) never trips while we probe the
	// subscription budget (64).
	var tick int64
	g.now = func() int64 { tick += 1000; return tick }
	g.registerSocket("s1")

	if rej := g.trySubscribe("s1", "r1"); rej != nil {
		t.Fatalf("first subscribe rejected: %+v", rej)
	}
	if rej := g.trySubscribe("s1", "r1"); rej != nil {
		t.Fatalf("idempotent re-subscribe rejected: %+v", rej)
	}
	// Fill to the 64-room budget (r1 already counts as 1).
	for i := 2; i <= 64; i++ {
		if rej := g.trySubscribe("s1", fmt.Sprintf("r%d", i)); rej != nil {
			t.Fatalf("subscribe %d rejected: %+v", i, rej)
		}
	}
	if rej := g.trySubscribe("s1", "r65"); rej == nil || rej.Code != "too_many_subscriptions" {
		t.Fatalf("want too_many_subscriptions at 65, got %+v", rej)
	}
}

func TestGuardRateLimit(t *testing.T) {
	g := newSubscriptionGuard()
	g.now = func() int64 { return 5000 } // fixed clock -> single window
	g.registerSocket("s1")
	// 60 ops allowed in the window (distinct rooms to avoid the idempotent path
	// short-circuiting before the rate check... the rate check runs first anyway).
	for i := 0; i < 60; i++ {
		if rej := g.trySubscribe("s1", fmt.Sprintf("r%d", i)); rej != nil {
			t.Fatalf("op %d rejected early: %+v", i, rej)
		}
	}
	if rej := g.trySubscribe("s1", "r-final"); rej == nil || rej.Code != "rate_limited" {
		t.Fatalf("want rate_limited at 61st op, got %+v", rej)
	}
}

func TestGuardReleaseSocket(t *testing.T) {
	g := newSubscriptionGuard()
	g.registerSocket("s1")
	g.releaseSocket("s1")
	if rej := g.trySubscribe("s1", "r1"); rej == nil || rej.Code != "unknown_socket" {
		t.Fatalf("released socket should be unknown, got %+v", rej)
	}
}
