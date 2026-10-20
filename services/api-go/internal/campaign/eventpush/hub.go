// Package eventpush delivers authorized Campaign invalidation hints to live
// transports. A hint carries only a durable stream head; clients repair state
// through the REST feed and snapshot. Serving subscriptions never writes
// receipts or delivery metadata, and losing every hint only adds latency.
package eventpush

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/eventfeed"
)

// Observer is the Campaign authorization and durable-head port. The production
// implementation is *eventfeed.Service.
type Observer interface {
	Authenticate(ctx context.Context, token string) (*eventfeed.Subject, error)
	Observe(ctx context.Context, subject *eventfeed.Subject, id, family string) (int64, error)
}

// StreamLookup maps a wakeup event ID to its durable stream.
type StreamLookup interface {
	EventStream(ctx context.Context, id string) (source, stream string, found bool, err error)
}

// Conn is one transport connection. Send must not block on the network; ack,
// when non-nil, is called once with whether the client acknowledged in time.
type Conn interface {
	Send(event string, payload map[string]any, ack func(delivered bool))
	Close()
}

const (
	EventInvalidate = "campaign:invalidate"
	EventReset      = "campaign:reset"
)

type Config struct {
	SweepInterval  time.Duration
	PerConnection  int
	Total          int
	Concurrency    int
	ObserveTimeout time.Duration
	AckTimeout     time.Duration
	RateLimit      int
	RateWindow     time.Duration
	WakeQueue      int
	DirtyLimit     int
	MaxToken       int
}

// BoundedBy limits observation concurrency to a database pool: the listener
// keeps one connection, and push work may use at most half of the rest so
// requests are never starved by sweeps.
func (c Config) BoundedBy(poolMaxConns int32) Config {
	limit := int(poolMaxConns-1) / 2
	if limit < 1 {
		limit = 1
	}
	if c.Concurrency > limit {
		c.Concurrency = limit
	}
	return c
}

func DefaultConfig() Config {
	return Config{SweepInterval: 10 * time.Second, PerConnection: 16, Total: 2048, Concurrency: 8, ObserveTimeout: 5 * time.Second,
		AckTimeout: 10 * time.Second, RateLimit: 30, RateWindow: 10 * time.Second, WakeQueue: 256, DirtyLimit: 1024, MaxToken: 4096}
}

// ConfigFromEnv reads CAMPAIGN_PUSH_SWEEP_MS (canonical integer 1000-60000).
func ConfigFromEnv(getenv func(string) string) (Config, error) {
	cfg := DefaultConfig()
	if raw := getenv("CAMPAIGN_PUSH_SWEEP_MS"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || strconv.Itoa(value) != raw || value < 1000 || value > 60000 {
			return cfg, errors.New("CAMPAIGN_PUSH_SWEEP_MS must be a canonical integer from 1000 to 60000")
		}
		cfg.SweepInterval = time.Duration(value) * time.Millisecond
	}
	return cfg, nil
}

var campaignPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

type key struct{ campaign, family string }

// unit is one observation: a public stream shared by every subscriber, or one
// private subscription with its own credential.
type unit struct {
	key
	sub *subscription
}

type subscription struct {
	client      *client
	key         key
	delivered   int64
	outstanding bool
	owed        bool
	// reserved counts against limits while a subscribe is being authorized;
	// only active subscriptions receive observations and frames.
	reserved bool
	active   bool
}

type client struct {
	conn    Conn
	subject *eventfeed.Subject
	subs    map[key]*subscription
	ctx     context.Context
	cancel  context.CancelFunc
	changes []time.Time
	closed  bool
}

type Hub struct {
	cfg      Config
	observer Observer
	lookup   StreamLookup
	now      func() time.Time

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	slots  chan struct{}
	signal chan struct{}
	wakes  chan string

	mu         sync.Mutex
	closed     bool
	clients    map[*client]struct{}
	total      int
	byCampaign map[string]map[*subscription]struct{}
	dirty      map[string]struct{}
	sweepAll   bool
	// urgent holds wakeup and owed-hint observations, pending holds sweep
	// observations; queued maps each waiting unit to whether it is urgent.
	// sweepTurn alternates the queues while both hold work, so continuous
	// wakeups cannot starve periodic re-authorization of quiet subscriptions.
	// Entries whose queued state no longer matches their slice are stale.
	urgent    []unit
	sweepTurn bool
	pending   []unit
	queued    map[unit]bool
	inflight  map[unit]bool
	again     map[unit]bool
}

// New starts the scheduler, router and sweep ticker. Close joins them.
func New(observer Observer, lookup StreamLookup, cfg Config) *Hub {
	ctx, cancel := context.WithCancel(context.Background())
	h := &Hub{cfg: cfg, observer: observer, lookup: lookup, now: time.Now, ctx: ctx, cancel: cancel,
		slots: make(chan struct{}, cfg.Concurrency), signal: make(chan struct{}, 1), wakes: make(chan string, cfg.WakeQueue),
		clients: map[*client]struct{}{}, byCampaign: map[string]map[*subscription]struct{}{}, dirty: map[string]struct{}{},
		queued: map[unit]bool{}, inflight: map[unit]bool{}, again: map[unit]bool{}}
	h.wg.Add(2)
	go h.schedule()
	go h.route()
	return h
}

// Close cancels every observation, stops the listener, router and ticker, and
// waits for all owned goroutines. Open transports are released but not
// disconnected: a server-initiated disconnect stops Socket.IO clients from
// reconnecting, so only a revoked credential uses it. Process shutdown closes
// the transports, and clients reconnect and resubscribe to the next process.
func (h *Hub) Close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	for c := range h.clients {
		c.closed = true
		c.cancel()
		h.dropClient(c)
	}
	h.urgent, h.pending, h.queued, h.again = nil, nil, map[unit]bool{}, map[unit]bool{}
	h.dirty, h.sweepAll = map[string]struct{}{}, false
	h.mu.Unlock()
	h.cancel()
	h.wg.Wait()
}

// Stats reports bounded runtime counters for tests and diagnostics.
type Stats struct {
	Connections, Subscriptions, Inflight, Pending int
}

func (h *Hub) Stats() Stats {
	h.mu.Lock()
	defer h.mu.Unlock()
	return Stats{Connections: len(h.clients), Subscriptions: h.total, Inflight: len(h.inflight), Pending: len(h.queued)}
}

func (h *Hub) nudge() {
	select {
	case h.signal <- struct{}{}:
	default:
	}
}

// RequestSweep re-observes every subscription, for example after the listener
// (re)registers and notifications may have been missed.
func (h *Hub) RequestSweep() {
	h.mu.Lock()
	h.sweepAll = !h.closed
	h.mu.Unlock()
	h.nudge()
}

// Wake routes one advisory event ID. A full queue degrades to a sweep instead
// of blocking the listener.
func (h *Hub) Wake(eventID string) {
	select {
	case h.wakes <- eventID:
	default:
		h.RequestSweep()
	}
}

func (h *Hub) markDirty(campaign string) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	if len(h.dirty) >= h.cfg.DirtyLimit {
		h.sweepAll = true
	} else {
		h.dirty[campaign] = struct{}{}
	}
	h.mu.Unlock()
	h.nudge()
}

func (h *Hub) route() {
	defer h.wg.Done()
	for {
		select {
		case <-h.ctx.Done():
			return
		case id := <-h.wakes:
			if h.lookup == nil {
				h.RequestSweep()
				continue
			}
			ctx, cancel := context.WithTimeout(h.ctx, 2*time.Second)
			source, stream, found, err := h.lookup.EventStream(ctx, id)
			cancel()
			if err != nil {
				if h.ctx.Err() != nil {
					return
				}
				h.RequestSweep()
				continue
			}
			if campaign, ok := streamCampaign(source, stream); found && ok {
				h.markDirty(campaign)
			}
		}
	}
}

func streamCampaign(source, stream string) (string, bool) {
	var parts []string
	if source != "campaign" || json.Unmarshal([]byte(stream), &parts) != nil || len(parts) != 4 || parts[0] != "campaign" || !campaignPattern.MatchString(parts[1]) {
		return "", false
	}
	return parts[1], true
}

func (h *Hub) schedule() {
	defer h.wg.Done()
	ticker := time.NewTicker(h.cfg.SweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-h.ctx.Done():
			return
		case <-ticker.C:
			h.mu.Lock()
			h.sweepAll = true
			h.mu.Unlock()
		case <-h.signal:
		}
		h.dispatch()
	}
}

// enqueue queues a sweep observation. A full private sweep at the
// subscription cap takes seconds, so it never delays a wakeup: see promote.
func (h *Hub) enqueue(u unit) {
	if _, ok := h.queued[u]; ok {
		return
	}
	h.queued[u] = false
	if len(h.pending) > 2*len(h.queued)+64 {
		h.pending = h.compact(h.pending, false)
	}
	h.pending = append(h.pending, u)
}

// promote queues an observation ahead of every sweep observation; a unit
// already waiting in the sweep queue moves to the urgent queue.
func (h *Hub) promote(u unit) {
	if urgent, ok := h.queued[u]; ok && urgent {
		return
	}
	h.queued[u] = true
	if len(h.urgent) > 2*len(h.queued)+64 {
		h.urgent = h.compact(h.urgent, true)
	}
	h.urgent = append(h.urgent, u)
}

func (h *Hub) compact(queue []unit, urgent bool) []unit {
	out := queue[:0]
	for _, u := range queue {
		if state, ok := h.queued[u]; ok && state == urgent {
			out = append(out, u)
		}
	}
	return out
}

// wanted reports whether a unit still has an active subscriber, so work for
// closed or unsubscribed connections is dropped instead of observed.
func (h *Hub) wanted(u unit) bool {
	if u.sub != nil {
		return u.sub.active
	}
	for sub := range h.byCampaign[u.campaign] {
		if sub.active && sub.key == u.key {
			return true
		}
	}
	return false
}

// next pops the first live unit without starting it. While both queues hold
// work it alternates urgent and sweep units: a queued unit therefore starts
// within twice its queue position in dispatches, whatever the wakeup rate.
func (h *Hub) next() (unit, bool) {
	order := []bool{true, false}
	if h.sweepTurn {
		order = []bool{false, true}
	}
	for _, urgent := range order {
		queue := &h.pending
		if urgent {
			queue = &h.urgent
		}
		for len(*queue) > 0 {
			u := (*queue)[0]
			*queue = (*queue)[1:]
			if state, ok := h.queued[u]; !ok || state != urgent {
				continue
			}
			delete(h.queued, u)
			if !h.wanted(u) {
				continue
			}
			h.sweepTurn = urgent
			return u, true
		}
	}
	return unit{}, false
}

func unitOf(sub *subscription) unit {
	if sub.key.family == "personal" {
		return unit{key: sub.key, sub: sub}
	}
	return unit{key: sub.key}
}

func (h *Hub) dispatch() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	if h.sweepAll {
		h.sweepAll = false
		h.dirty = map[string]struct{}{}
		for _, subs := range h.byCampaign {
			for sub := range subs {
				h.enqueue(unitOf(sub))
			}
		}
	}
	for campaign := range h.dirty {
		for sub := range h.byCampaign[campaign] {
			h.promote(unitOf(sub))
		}
	}
	h.dirty = map[string]struct{}{}
	for len(h.queued) > 0 {
		select {
		case h.slots <- struct{}{}:
		default:
			return
		}
		u, ok := h.next()
		if !ok {
			<-h.slots
			return
		}
		if h.inflight[u] {
			<-h.slots
			h.again[u] = true
			continue
		}
		h.inflight[u] = true
		h.wg.Add(1)
		go h.run(u)
	}
}

type frame struct {
	conn    Conn
	event   string
	payload map[string]any
	ack     func(bool)
}

func (h *Hub) run(u unit) {
	defer h.wg.Done()
	ctx, subject := h.ctx, (*eventfeed.Subject)(nil)
	if u.sub != nil {
		h.mu.Lock()
		ctx, subject = u.sub.client.ctx, u.sub.client.subject
		h.mu.Unlock()
	}
	ctx, cancel := context.WithTimeout(ctx, h.cfg.ObserveTimeout)
	head, err := h.observer.Observe(ctx, subject, u.campaign, u.family)
	cancel()
	<-h.slots
	h.mu.Lock()
	delete(h.inflight, u)
	frames, closing := h.apply(u, head, err)
	if h.again[u] {
		delete(h.again, u)
		if !h.closed {
			h.promote(u)
		}
	}
	h.mu.Unlock()
	send(frames, closing)
	h.nudge()
}

func send(frames []frame, closing []Conn) {
	for _, f := range frames {
		f.conn.Send(f.event, f.payload, f.ack)
	}
	for _, conn := range closing {
		conn.Close()
	}
}

func (h *Hub) targets(u unit) []*subscription {
	if u.sub != nil {
		if u.sub.active {
			return []*subscription{u.sub}
		}
		return nil
	}
	var out []*subscription
	for sub := range h.byCampaign[u.campaign] {
		if sub.active && sub.key == u.key {
			out = append(out, sub)
		}
	}
	return out
}

func resetFrame(sub *subscription, reason string) frame {
	return frame{conn: sub.client.conn, event: EventReset, payload: map[string]any{"schemaVersion": 1, "campaignId": sub.key.campaign, "family": sub.key.family, "reason": reason}}
}

// apply runs under h.mu, after the observation's final authorization check.
// Frames are built here and sent immediately after unlocking; nothing private
// is queued for later release.
func (h *Hub) apply(u unit, head int64, err error) ([]frame, []Conn) {
	if h.closed {
		return nil, nil
	}
	subs := h.targets(u)
	var frames []frame
	var closing []Conn
	if err != nil {
		switch eventfeed.Code(err) {
		case "UNAUTHORIZED":
			for _, sub := range subs {
				f, c := h.revoke(sub.client)
				frames, closing = append(frames, f...), append(closing, c...)
			}
		case "WEB3_SESSION_UNAVAILABLE":
			for _, sub := range subs {
				frames = append(frames, resetFrame(sub, "session_unavailable"))
				h.remove(sub)
			}
		case "CAMPAIGN_NOT_FOUND":
			for _, sub := range subs {
				frames = append(frames, resetFrame(sub, "not_found"))
				h.remove(sub)
			}
		}
		// Other failures are transient: keep the subscription for the next sweep.
		return frames, closing
	}
	for _, sub := range subs {
		if head <= sub.delivered {
			continue
		}
		if sub.outstanding {
			sub.owed = true
			continue
		}
		sub.outstanding, sub.delivered = true, head
		frames = append(frames, frame{conn: sub.client.conn, event: EventInvalidate, ack: h.acknowledge(sub),
			payload: map[string]any{"schemaVersion": 1, "campaignId": sub.key.campaign, "family": sub.key.family, "headVersion": strconv.FormatInt(head, 10)}})
	}
	return frames, closing
}

// acknowledge releases one subscription's flow-control slot. A head owed while
// the previous hint was outstanding is re-observed, and so re-authorized,
// instead of being sent from a stale observation.
func (h *Hub) acknowledge(sub *subscription) func(bool) {
	var once sync.Once
	return func(bool) {
		once.Do(func() {
			h.mu.Lock()
			sub.outstanding = false
			owed := sub.owed && sub.active && !h.closed
			sub.owed = false
			if owed {
				h.promote(unitOf(sub))
			}
			h.mu.Unlock()
			if owed {
				h.nudge()
			}
		})
	}
}

// revoke runs under h.mu: a dead credential ends every private subscription of
// the connection, then the connection itself.
func (h *Hub) revoke(c *client) ([]frame, []Conn) {
	if c.closed {
		return nil, nil
	}
	var frames []frame
	for _, sub := range c.subs {
		if sub.key.family == "personal" && sub.active {
			frames = append(frames, resetFrame(sub, "unauthorized"))
		}
	}
	c.closed = true
	c.cancel()
	h.dropClient(c)
	return frames, []Conn{c.conn}
}

func (h *Hub) remove(sub *subscription) {
	if !sub.active && !sub.reserved {
		return
	}
	sub.active, sub.reserved = false, false
	if sub.client.subs[sub.key] == sub {
		delete(sub.client.subs, sub.key)
	}
	if set := h.byCampaign[sub.key.campaign]; set != nil {
		delete(set, sub)
		if len(set) == 0 {
			delete(h.byCampaign, sub.key.campaign)
		}
	}
	h.total--
}

func (h *Hub) dropClient(c *client) {
	for _, sub := range c.subs {
		h.remove(sub)
	}
	delete(h.clients, c)
}
