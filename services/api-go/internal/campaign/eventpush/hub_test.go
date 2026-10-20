package eventpush

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/eventfeed"
	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
)

func domain(code string) error { return &v2.DomainError{Code: code} }

// fakeObserver emulates eventfeed's contract: tokens map to identities whose
// state is consulted when an observation returns, i.e. after any block.
type fakeObserver struct {
	mu       sync.Mutex
	heads    map[string]int64 // campaign|family|owner
	tokens   map[string]string
	subjects map[*eventfeed.Subject]string
	state    map[string]string // token -> "" valid, "revoked", "unavailable"
	hidden   map[string]bool
	failing  map[string]error
	gate     map[string]chan struct{}
	entered  chan string
	active   int
	maxSeen  int
	calls    map[string]int
	// after runs once an observation of the keyed stream finishes, outside the
	// observer lock; tests use it to generate wakeups that never stop.
	after map[string]func()
}

func newObserver() *fakeObserver {
	return &fakeObserver{heads: map[string]int64{}, tokens: map[string]string{"tokA": "0xa", "tokB": "0xb"}, subjects: map[*eventfeed.Subject]string{},
		state: map[string]string{}, hidden: map[string]bool{}, failing: map[string]error{}, gate: map[string]chan struct{}{}, entered: make(chan string, 1024), calls: map[string]int{},
		after: map[string]func(){}}
}

func (f *fakeObserver) Authenticate(_ context.Context, token string) (*eventfeed.Subject, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.tokens[token]; !ok || f.state[token] == "revoked" {
		return nil, domain("UNAUTHORIZED")
	}
	if f.state[token] == "unavailable" {
		return nil, domain("WEB3_SESSION_UNAVAILABLE")
	}
	subject := &eventfeed.Subject{}
	f.subjects[subject] = token
	return subject, nil
}

func (f *fakeObserver) set(id, family, owner string, head int64) {
	f.mu.Lock()
	f.heads[id+"|"+family+"|"+owner] = head
	f.mu.Unlock()
}

func (f *fakeObserver) block(id, family string) chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch := make(chan struct{})
	f.gate[id+"|"+family] = ch
	return ch
}

func (f *fakeObserver) Observe(ctx context.Context, subject *eventfeed.Subject, id, family string) (int64, error) {
	f.mu.Lock()
	f.active++
	if f.active > f.maxSeen {
		f.maxSeen = f.active
	}
	f.calls[id+"|"+family]++
	gate := f.gate[id+"|"+family]
	hook := f.after[id+"|"+family]
	f.mu.Unlock()
	if hook != nil {
		defer hook()
	}
	defer func() { f.mu.Lock(); f.active--; f.mu.Unlock() }()
	f.entered <- id + "|" + family
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return 0, domain("CAMPAIGN_UNAVAILABLE")
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	owner := ""
	if family == "personal" {
		token, ok := f.subjects[subject]
		if !ok || f.state[token] == "revoked" {
			return 0, domain("UNAUTHORIZED")
		}
		if f.state[token] == "unavailable" {
			return 0, domain("WEB3_SESSION_UNAVAILABLE")
		}
		owner = f.tokens[token]
	}
	if err := f.failing[id]; err != nil {
		return 0, err
	}
	if f.hidden[id] {
		return 0, domain("CAMPAIGN_NOT_FOUND")
	}
	return f.heads[id+"|"+family+"|"+owner], nil
}

type sent struct {
	event   string
	payload map[string]any
	ack     func(bool)
}

type fakeConn struct {
	mu     sync.Mutex
	frames []sent
	closed bool
	notify chan sent
}

func newConn() *fakeConn { return &fakeConn{notify: make(chan sent, 256)} }

func (c *fakeConn) Send(event string, payload map[string]any, ack func(bool)) {
	c.mu.Lock()
	frame := sent{event, payload, ack}
	c.frames = append(c.frames, frame)
	c.mu.Unlock()
	c.notify <- frame
}
func (c *fakeConn) Close() { c.mu.Lock(); c.closed = true; c.mu.Unlock() }
func (c *fakeConn) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func (c *fakeConn) next(t *testing.T) sent {
	t.Helper()
	select {
	case frame := <-c.notify:
		return frame
	case <-time.After(5 * time.Second):
		t.Fatal("no frame")
		return sent{}
	}
}

func (c *fakeConn) none(t *testing.T, wait time.Duration) {
	t.Helper()
	select {
	case frame := <-c.notify:
		t.Fatalf("unexpected frame %s %v", frame.event, frame.payload)
	case <-time.After(wait):
	}
}

type fakeLookup struct{ streams map[string][2]string }

func (l fakeLookup) EventStream(_ context.Context, id string) (string, string, bool, error) {
	if id == "ffffffff-ffff-4fff-bfff-ffffffffffff" {
		return "", "", false, errors.New("down")
	}
	s, ok := l.streams[id]
	return s[0], s[1], ok, nil
}

func testHub(t *testing.T, observer *fakeObserver, mutate func(*Config)) *Hub {
	t.Helper()
	cfg := DefaultConfig()
	cfg.SweepInterval = time.Hour
	if mutate != nil {
		mutate(&cfg)
	}
	lookup := fakeLookup{streams: map[string][2]string{
		"11111111-1111-4111-8111-111111111111": {"campaign", `["campaign","c1","interaction","public"]`},
		"22222222-2222-4222-8222-222222222222": {"campaign", `["campaign","c2","publication","public"]`},
		"33333333-3333-4333-8333-333333333333": {"chain", `["campaign","c1","interaction","public"]`},
	}}
	h := New(observer, lookup, cfg)
	t.Cleanup(h.Close)
	return h
}

func attach(t *testing.T, h *Hub, auth any) (*Session, *fakeConn) {
	t.Helper()
	identity, code := h.Authenticate(context.Background(), auth)
	if code != "" {
		t.Fatalf("authenticate: %s", code)
	}
	conn := newConn()
	session := h.Attach(identity, conn)
	if session == nil {
		t.Fatal("attach refused")
	}
	return session, conn
}

func subscribe(t *testing.T, s *Session, campaign, family string) map[string]any {
	t.Helper()
	ack, after := s.Subscribe(context.Background(), map[string]any{"campaignId": campaign, "family": family})
	after()
	return ack
}

func mustSucceed(t *testing.T, ack map[string]any) {
	t.Helper()
	if ack["success"] != true {
		t.Fatalf("ack = %v", ack)
	}
}

func mustFail(t *testing.T, ack map[string]any, code string) {
	t.Helper()
	if ack["success"] != false || ack["code"] != code {
		t.Fatalf("ack = %v, want %s", ack, code)
	}
	if _, ok := ack["campaignId"]; ok {
		t.Fatalf("failure ack echoes key: %v", ack)
	}
}

func drain(o *fakeObserver) {
	for {
		select {
		case <-o.entered:
		default:
			return
		}
	}
}

func TestConnectionCredentialIsNeverDowngraded(t *testing.T) {
	o := newObserver()
	h := testHub(t, o, nil)
	cases := []struct {
		auth any
		code string
	}{
		{nil, ""}, {map[string]any{}, ""}, {map[string]any{"token": "tokA"}, ""},
		{map[string]any{"token": "nope"}, RejectUnauthorized},
		{map[string]any{"token": ""}, RejectInvalidAuth},
		{map[string]any{"token": "tokA", "owner": "0xb"}, RejectInvalidAuth},
		{map[string]any{"owner": "0xa"}, RejectInvalidAuth},
		{map[string]any{"token": 7}, RejectInvalidAuth},
		{map[string]any{"token": "tok\nA"}, RejectInvalidAuth},
		{map[string]any{"token": string(make([]byte, 4097))}, RejectInvalidAuth},
		{"tokA", RejectInvalidAuth},
	}
	for i, tc := range cases {
		if _, code := h.Authenticate(context.Background(), tc.auth); code != tc.code {
			t.Fatalf("case %d: code %q want %q", i, code, tc.code)
		}
	}
	o.state["tokB"] = "unavailable"
	if _, code := h.Authenticate(context.Background(), map[string]any{"token": "tokB"}); code != RejectSessionUnavailable {
		t.Fatalf("unavailable store: %q", code)
	}
}

func TestSubscriptionRequestsAreClosedAndOwnerIsDerived(t *testing.T) {
	o := newObserver()
	h := testHub(t, o, nil)
	anon, _ := attach(t, h, nil)
	mustFail(t, subscribe(t, anon, "c1", "personal"), "unauthorized")
	mustSucceed(t, subscribe(t, anon, "c1", "publication"))
	for _, raw := range []any{nil, "c1", map[string]any{"campaignId": "c1"}, map[string]any{"campaignId": "c1", "family": "personal", "owner": "0xa"},
		map[string]any{"campaignId": "c1", "family": "publication", "cursor": "x"}, map[string]any{"campaignId": "bad id", "family": "publication"},
		map[string]any{"campaignId": "c1", "family": "drafts"}, map[string]any{"campaignId": 1, "family": "publication"}} {
		ack, _ := anon.Subscribe(context.Background(), raw)
		mustFail(t, ack, "invalid_request")
		mustFail(t, anon.Unsubscribe(raw), "invalid_request")
	}
	o.hidden["draft"] = true
	mustFail(t, subscribe(t, anon, "draft", "publication"), "not_found")
	if stats := h.Stats(); stats.Subscriptions != 1 {
		t.Fatalf("failed subscribes retained slots: %+v", stats)
	}
	ack := anon.Unsubscribe(map[string]any{"campaignId": "c1", "family": "publication"})
	mustSucceed(t, ack)
	if ack["campaignId"] != "c1" || ack["family"] != "publication" || h.Stats().Subscriptions != 0 {
		t.Fatalf("unsubscribe: %v %+v", ack, h.Stats())
	}
}

func TestPrivateHintsFollowOnlyTheConnectionOwner(t *testing.T) {
	o := newObserver()
	h := testHub(t, o, nil)
	a, connA := attach(t, h, map[string]any{"token": "tokA"})
	b, connB := attach(t, h, map[string]any{"token": "tokB"})
	mustSucceed(t, subscribe(t, a, "c1", "personal"))
	mustSucceed(t, subscribe(t, b, "c1", "personal"))
	o.set("c1", "personal", "0xa", 1)
	h.markDirty("c1")
	frame := connA.next(t)
	if frame.event != EventInvalidate || frame.payload["headVersion"] != "1" || frame.payload["family"] != "personal" || len(frame.payload) != 4 {
		t.Fatalf("frame = %v", frame)
	}
	connB.none(t, 100*time.Millisecond)
}

func TestDuplicateWakeupsAndSweepsDoNotRepeatHints(t *testing.T) {
	o := newObserver()
	h := testHub(t, o, nil)
	s, conn := attach(t, h, nil)
	o.set("c1", "interaction", "", 3)
	mustSucceed(t, subscribe(t, s, "c1", "interaction"))
	h.Wake("11111111-1111-4111-8111-111111111111")
	conn.none(t, 100*time.Millisecond) // head equals baseline
	o.set("c1", "interaction", "", 4)
	h.Wake("11111111-1111-4111-8111-111111111111")
	frame := conn.next(t)
	if frame.payload["headVersion"] != "4" {
		t.Fatalf("frame = %v", frame)
	}
	frame.ack(true)
	h.Wake("11111111-1111-4111-8111-111111111111")
	h.RequestSweep()
	conn.none(t, 150*time.Millisecond)
	// Unknown, foreign-source and lookup-failure wakeups never emit by themselves.
	h.Wake("99999999-9999-4999-8999-999999999999")
	h.Wake("33333333-3333-4333-8333-333333333333")
	h.Wake("ffffffff-ffff-4fff-bfff-ffffffffffff")
	conn.none(t, 150*time.Millisecond)
}

func TestSlowSubscriberHoldsOneHintAndOwedHeadIsReauthorized(t *testing.T) {
	o := newObserver()
	h := testHub(t, o, nil)
	s, conn := attach(t, h, map[string]any{"token": "tokA"})
	mustSucceed(t, subscribe(t, s, "c1", "personal"))
	o.set("c1", "personal", "0xa", 1)
	h.markDirty("c1")
	first := conn.next(t)
	for head := int64(2); head <= 20; head++ {
		o.set("c1", "personal", "0xa", head)
		h.markDirty("c1")
	}
	conn.none(t, 200*time.Millisecond)
	// Revocation while the owed head waits behind the unacknowledged hint: the
	// acknowledgement triggers a fresh observation, which refuses delivery.
	o.mu.Lock()
	o.state["tokA"] = "revoked"
	o.mu.Unlock()
	first.ack(true)
	reset := conn.next(t)
	if reset.event != EventReset || reset.payload["reason"] != "unauthorized" {
		t.Fatalf("reset = %v", reset)
	}
	conn.none(t, 100*time.Millisecond)
	if !conn.isClosed() || h.Stats().Subscriptions != 0 || h.Stats().Connections != 0 {
		t.Fatalf("revoked connection retained: %+v", h.Stats())
	}
}

func TestOwedHeadDeliveredAfterAcknowledgement(t *testing.T) {
	o := newObserver()
	h := testHub(t, o, nil)
	s, conn := attach(t, h, nil)
	mustSucceed(t, subscribe(t, s, "c1", "publication"))
	o.set("c1", "publication", "", 1)
	h.markDirty("c1")
	first := conn.next(t)
	o.set("c1", "publication", "", 7)
	h.markDirty("c1")
	conn.none(t, 100*time.Millisecond)
	first.ack(false) // a timeout releases flow control the same way
	if frame := conn.next(t); frame.payload["headVersion"] != "7" {
		t.Fatalf("owed frame = %v", frame)
	}
}

func TestRevocationDuringBlockedObservationRefusesDelivery(t *testing.T) {
	o := newObserver()
	h := testHub(t, o, nil)
	s, conn := attach(t, h, map[string]any{"token": "tokA"})
	mustSucceed(t, subscribe(t, s, "c1", "personal"))
	mustSucceed(t, subscribe(t, s, "c2", "publication"))
	drain(o)
	release := o.block("c1", "personal")
	o.set("c1", "personal", "0xa", 5)
	h.markDirty("c1")
	if got := <-o.entered; got != "c1|personal" {
		t.Fatalf("entered %s", got)
	}
	o.mu.Lock()
	o.state["tokA"] = "revoked"
	o.mu.Unlock()
	close(release)
	frame := conn.next(t)
	if frame.event != EventReset || frame.payload["reason"] != "unauthorized" || frame.payload["campaignId"] != "c1" {
		t.Fatalf("frame = %v", frame)
	}
	conn.none(t, 100*time.Millisecond)
	if !conn.isClosed() {
		t.Fatal("revoked connection left open")
	}
}

func TestSessionOutageDuringBlockedObservationRemovesOnlyThatSubscription(t *testing.T) {
	o := newObserver()
	h := testHub(t, o, nil)
	s, conn := attach(t, h, map[string]any{"token": "tokA"})
	mustSucceed(t, subscribe(t, s, "c1", "personal"))
	mustSucceed(t, subscribe(t, s, "c2", "publication"))
	drain(o)
	release := o.block("c1", "personal")
	o.set("c1", "personal", "0xa", 5)
	h.markDirty("c1")
	<-o.entered
	o.mu.Lock()
	o.state["tokA"] = "unavailable"
	o.mu.Unlock()
	close(release)
	frame := conn.next(t)
	if frame.event != EventReset || frame.payload["reason"] != "session_unavailable" {
		t.Fatalf("frame = %v", frame)
	}
	if conn.isClosed() || h.Stats().Subscriptions != 1 {
		t.Fatalf("outage should keep the public subscription: %+v", h.Stats())
	}
	o.set("c2", "publication", "", 1)
	h.markDirty("c2")
	if frame := conn.next(t); frame.event != EventInvalidate || frame.payload["campaignId"] != "c2" {
		t.Fatalf("unrelated stream frame = %v", frame)
	}
}

func TestVisibilityLossDuringBlockedObservationResetsPublicStream(t *testing.T) {
	o := newObserver()
	h := testHub(t, o, nil)
	s1, conn1 := attach(t, h, nil)
	s2, conn2 := attach(t, h, nil)
	mustSucceed(t, subscribe(t, s1, "c1", "publication"))
	mustSucceed(t, subscribe(t, s2, "c1", "publication"))
	drain(o)
	release := o.block("c1", "publication")
	o.set("c1", "publication", "", 9)
	h.markDirty("c1")
	<-o.entered
	o.mu.Lock()
	o.hidden["c1"] = true
	o.mu.Unlock()
	close(release)
	for _, conn := range []*fakeConn{conn1, conn2} {
		if frame := conn.next(t); frame.event != EventReset || frame.payload["reason"] != "not_found" {
			t.Fatalf("frame = %v", frame)
		}
		conn.none(t, 50*time.Millisecond)
	}
	if h.Stats().Subscriptions != 0 {
		t.Fatalf("hidden stream retained subscribers: %+v", h.Stats())
	}
}

func TestTransientFailureKeepsSubscriptionWithoutDisclosure(t *testing.T) {
	o := newObserver()
	h := testHub(t, o, nil)
	s, conn := attach(t, h, nil)
	mustSucceed(t, subscribe(t, s, "c1", "publication"))
	o.set("c1", "publication", "", 2)
	o.failing["c1"] = domain("CAMPAIGN_UNAVAILABLE")
	h.markDirty("c1")
	conn.none(t, 100*time.Millisecond)
	o.mu.Lock()
	delete(o.failing, "c1")
	o.mu.Unlock()
	h.RequestSweep()
	if frame := conn.next(t); frame.payload["headVersion"] != "2" {
		t.Fatalf("recovered frame = %v", frame)
	}
}

func TestBoundsHoldAndUnrelatedStreamsProgress(t *testing.T) {
	o := newObserver()
	h := testHub(t, o, func(c *Config) { c.Total = 20; c.ObserveTimeout = 4 * time.Second })
	s, _ := attach(t, h, nil)
	for i := 0; i < 16; i++ {
		mustSucceed(t, subscribe(t, s, fmt.Sprintf("p%d", i), "publication"))
	}
	mustFail(t, subscribe(t, s, "p99", "publication"), "too_many_subscriptions")
	other, _ := attach(t, h, nil)
	for i := 0; i < 4; i++ {
		mustSucceed(t, subscribe(t, other, fmt.Sprintf("q%d", i), "publication"))
	}
	mustFail(t, subscribe(t, other, "q99", "publication"), "capacity")
	limited, _ := attach(t, h, nil)
	for i := 0; i < 30; i++ {
		mustFail(t, subscribe(t, limited, "x", "publication"), "capacity")
	}
	mustFail(t, subscribe(t, limited, "x", "publication"), "rate_limited")
	// Releasing a key is never refused by the change limit.
	mustSucceed(t, s.Unsubscribe(map[string]any{"campaignId": "p15", "family": "publication"}))
	mustSucceed(t, subscribe(t, s, "p15", "publication"))

	// One stream stuck in its observation must not stall the other units, and
	// concurrent observations never exceed the configured bound.
	drain(o)
	stuck := o.block("p0", "publication")
	defer close(stuck)
	o.set("p0", "publication", "", 1)
	h.markDirty("p0")
	if got := <-o.entered; got != "p0|publication" {
		t.Fatalf("entered %s", got)
	}
	for i := 1; i < 16; i++ {
		o.set(fmt.Sprintf("p%d", i), "publication", "", 1)
	}
	h.RequestSweep()
	// Well inside p0's observation timeout: serialized scheduling would stall here.
	deadline := time.After(1500 * time.Millisecond)
	progressed := func() bool {
		for i := 1; i < 16; i++ {
			if o.callsFor(fmt.Sprintf("p%d", i), "publication") < 2 {
				return false
			}
		}
		return true
	}
	for !progressed() {
		select {
		case <-deadline:
			t.Fatal("unrelated streams did not progress while p0 was blocked")
		case <-o.entered:
		}
	}
	o.mu.Lock()
	maxSeen := o.maxSeen
	o.mu.Unlock()
	if maxSeen > h.cfg.Concurrency {
		t.Fatalf("observations reached %d, bound %d", maxSeen, h.cfg.Concurrency)
	}
}

func (f *fakeObserver) callsFor(id, family string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[id+"|"+family]
}

func TestWakeQueueOverflowBecomesSweep(t *testing.T) {
	o := newObserver()
	h := testHub(t, o, func(c *Config) { c.WakeQueue = 1 })
	s, conn := attach(t, h, nil)
	mustSucceed(t, subscribe(t, s, "c9", "publication"))
	o.set("c9", "publication", "", 1)
	for i := 0; i < 100; i++ {
		h.Wake("99999999-9999-4999-8999-999999999999")
	}
	if frame := conn.next(t); frame.payload["campaignId"] != "c9" {
		t.Fatalf("frame = %v", frame)
	}
}

func TestDisconnectCancelsInflightAndCloseJoinsEverything(t *testing.T) {
	before := runtime.NumGoroutine()
	o := newObserver()
	cfg := DefaultConfig()
	cfg.SweepInterval = 20 * time.Millisecond
	cfg.ObserveTimeout = time.Minute // only cancellation can end the blocked observation
	h := New(o, fakeLookup{}, cfg)
	s, conn := attach(t, h, map[string]any{"token": "tokA"})
	mustSucceed(t, subscribe(t, s, "c1", "personal"))
	drain(o)
	o.block("c1", "personal") // never released: only cancellation can end it
	o.set("c1", "personal", "0xa", 3)
	<-o.entered
	s.Close()
	waitFor(t, func() bool { return h.Stats().Inflight == 0 })
	conn.none(t, 50*time.Millisecond)
	s2, _ := attach(t, h, nil)
	mustSucceed(t, subscribe(t, s2, "c2", "publication"))
	h.Close()
	h.Close()
	if h.Attach(&Identity{}, newConn()) != nil {
		t.Fatal("closed hub attached a connection")
	}
	if stats := h.Stats(); stats != (Stats{}) {
		t.Fatalf("stats after close: %+v", stats)
	}
	waitFor(t, func() bool { return runtime.NumGoroutine() <= before })
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestWakeupDecodingAndStreamRouting(t *testing.T) {
	good := `{"schemaVersion":1,"eventId":"11111111-1111-4111-8111-111111111111"}`
	if id, ok := decodeWakeup(good); !ok || id != "11111111-1111-4111-8111-111111111111" {
		t.Fatal("valid wakeup rejected")
	}
	for _, raw := range []string{"", "{}", `{"schemaVersion":2,"eventId":"11111111-1111-4111-8111-111111111111"}`,
		`{"schemaVersion":1,"eventId":"11111111-1111-4111-8111-111111111111","topic":"x"}`, good + good,
		`{"schemaVersion":1,"eventId":"11111111-1111-4111-8111-11111111111G"}`, `{"schemaVersion":1,"eventId":"11111111111141118111111111111111"}`} {
		if _, ok := decodeWakeup(raw); ok {
			t.Fatalf("accepted %q", raw)
		}
	}
	if c, ok := streamCampaign("campaign", `["campaign","abc","interaction","public"]`); !ok || c != "abc" {
		t.Fatal("stream not routed")
	}
	for _, s := range [][2]string{{"chain", `["campaign","abc","interaction","public"]`}, {"campaign", `["campaign","a b","x","y"]`}, {"campaign", `["other","abc","x","y"]`}, {"campaign", `["campaign","abc"]`}, {"campaign", `nope`}} {
		if _, ok := streamCampaign(s[0], s[1]); ok {
			t.Fatalf("routed %v", s)
		}
	}
}

func TestSweepConfiguration(t *testing.T) {
	env := func(v string) func(string) string { return func(string) string { return v } }
	if cfg, err := ConfigFromEnv(env("")); err != nil || cfg.SweepInterval != 10*time.Second {
		t.Fatal("default sweep")
	}
	if cfg, err := ConfigFromEnv(env("1000")); err != nil || cfg.SweepInterval != time.Second {
		t.Fatal("minimum sweep")
	}
	for _, bad := range []string{"999", "60001", "01000", "1e3", " 1000", "abc"} {
		if _, err := ConfigFromEnv(env(bad)); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}
