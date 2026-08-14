package realtime

import (
	"fmt"
	"sync"
	"time"
)

// guardRejection mirrors WsGuardRejection. The JSON `code` is what clients
// branch on (e.g. rate_limited -> backoff).
type guardRejection struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type guardOptions struct {
	maxSubscriptions   int
	rateLimitPerWindow int
	windowMs           int64
}

// defaultGuardOptions mirrors DEFAULT_OPTIONS in ws-subscription-guard.ts.
var defaultGuardOptions = guardOptions{
	maxSubscriptions:   64,
	rateLimitPerWindow: 60,
	windowMs:           10_000,
}

type socketBudget struct {
	rooms  map[string]struct{}
	events []int64 // ms timestamps within the sliding window
}

// subscriptionGuard is the Go port of WsSubscriptionGuard: per-socket
// subscription budget + sliding-window rate limit. Safe for concurrent use.
type subscriptionGuard struct {
	mu      sync.Mutex
	opts    guardOptions
	budgets map[string]*socketBudget
	now     func() int64 // injectable clock (ms) for tests
}

func newSubscriptionGuard() *subscriptionGuard {
	return &subscriptionGuard{
		opts:    defaultGuardOptions,
		budgets: map[string]*socketBudget{},
		now:     func() int64 { return time.Now().UnixMilli() },
	}
}

func (g *subscriptionGuard) registerSocket(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.budgets[id]; !ok {
		g.budgets[id] = &socketBudget{rooms: map[string]struct{}{}}
	}
}

func (g *subscriptionGuard) releaseSocket(id string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.budgets, id)
}

// trySubscribe mirrors WsSubscriptionGuard.trySubscribe: nil == allowed.
func (g *subscriptionGuard) trySubscribe(id, room string) *guardRejection {
	g.mu.Lock()
	defer g.mu.Unlock()
	budget := g.budgets[id]
	if budget == nil {
		return &guardRejection{"unknown_socket", "Socket is not registered"}
	}
	if rej := g.applyRateLimit(budget); rej != nil {
		return rej
	}
	if _, joined := budget.rooms[room]; joined {
		return nil // idempotent re-subscribe
	}
	if len(budget.rooms) >= g.opts.maxSubscriptions {
		return &guardRejection{
			Code:    "too_many_subscriptions",
			Message: fmt.Sprintf("Subscription limit of %d reached", g.opts.maxSubscriptions),
		}
	}
	budget.rooms[room] = struct{}{}
	return nil
}

// tryUnsubscribe mirrors WsSubscriptionGuard.tryUnsubscribe.
func (g *subscriptionGuard) tryUnsubscribe(id, room string) *guardRejection {
	g.mu.Lock()
	defer g.mu.Unlock()
	budget := g.budgets[id]
	if budget == nil {
		return &guardRejection{"unknown_socket", "Socket is not registered"}
	}
	if rej := g.applyRateLimit(budget); rej != nil {
		return rej
	}
	delete(budget.rooms, room)
	return nil
}

// applyRateLimit mirrors the private applyRateLimit: evict events older than the
// window, reject when the window is full, else record the event.
func (g *subscriptionGuard) applyRateLimit(budget *socketBudget) *guardRejection {
	now := g.now()
	windowStart := now - g.opts.windowMs
	i := 0
	for i < len(budget.events) && budget.events[i] < windowStart {
		i++
	}
	budget.events = budget.events[i:]
	if len(budget.events) >= g.opts.rateLimitPerWindow {
		return &guardRejection{"rate_limited", "Too many subscription changes; slow down"}
	}
	budget.events = append(budget.events, now)
	return nil
}
