package eventpush

import (
	"context"
	"encoding/json"
	"strings"
	"unicode"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/eventfeed"
	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
)

// Connection rejection codes reported through the transport's connect error.
const (
	RejectInvalidAuth        = "invalid_auth"
	RejectUnauthorized       = "unauthorized"
	RejectSessionUnavailable = "session_unavailable"
	RejectUnavailable        = "unavailable"
)

var ackMessages = map[string]string{
	"invalid_request":        "Subscription request is invalid",
	"unauthorized":           "Authentication is required for this subscription",
	"session_unavailable":    "Session state cannot be confirmed",
	"not_found":              "Campaign stream is not available to this caller",
	"unavailable":            "Campaign events are temporarily unavailable",
	"too_many_subscriptions": "Subscription limit reached for this connection",
	"capacity":               "Campaign event capacity reached",
	"rate_limited":           "Too many subscription changes; slow down",
	"cancelled":              "Subscription was cancelled by a later request",
}

func ackFailure(code string) map[string]any {
	return map[string]any{"success": false, "code": code, "message": ackMessages[code]}
}

func noop() {}

func ackSuccess(k key) map[string]any {
	return map[string]any{"success": true, "campaignId": k.campaign, "family": k.family}
}

// Session is one open transport connection.
type Session struct {
	hub    *Hub
	client *client
}

// credential accepts only absent/null/{} (anonymous) or exactly {"token": s}.
// A malformed object is rejected rather than treated as anonymous.
func credential(auth any, limit int) (token string, present bool, valid bool) {
	if auth == nil {
		return "", false, true
	}
	fields, ok := auth.(map[string]any)
	if !ok {
		return "", false, false
	}
	if len(fields) == 0 {
		return "", false, true
	}
	raw, ok := fields["token"].(string)
	if len(fields) != 1 || !ok || raw == "" || len(raw) > limit || strings.IndexFunc(raw, unicode.IsControl) >= 0 {
		return "", false, false
	}
	return raw, true, true
}

// Identity is a connection's verified credential, or an anonymous identity.
type Identity struct{ subject *eventfeed.Subject }

// Authenticate verifies the CONNECT auth object. A present but failing
// credential rejects the connection; it never falls back to anonymous.
func (h *Hub) Authenticate(ctx context.Context, auth any) (*Identity, string) {
	token, present, valid := credential(auth, h.cfg.MaxToken)
	if !valid {
		return nil, RejectInvalidAuth
	}
	if !present {
		return &Identity{}, ""
	}
	ctx, cancel := context.WithTimeout(ctx, h.cfg.ObserveTimeout)
	defer cancel()
	subject, err := h.observer.Authenticate(ctx, token)
	switch {
	case err == nil:
		return &Identity{subject: subject}, ""
	case eventfeed.Code(err) == "UNAUTHORIZED":
		return nil, RejectUnauthorized
	case eventfeed.Code(err) == "WEB3_SESSION_UNAVAILABLE":
		return nil, RejectSessionUnavailable
	default:
		return nil, RejectUnavailable
	}
}

// Attach registers a connected transport. It is separate from Authenticate so
// a client that disconnects between the two never leaves a registered session.
func (h *Hub) Attach(identity *Identity, conn Conn) *Session {
	if identity == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil
	}
	cctx, cancel := context.WithCancel(h.ctx)
	c := &client{conn: conn, subject: identity.subject, subs: map[key]*subscription{}, ctx: cctx, cancel: cancel}
	h.clients[c] = struct{}{}
	return &Session{hub: h, client: c}
}

func decodeKey(raw any) (key, bool) {
	body, err := json.Marshal(raw)
	if err != nil {
		return key{}, false
	}
	var input v2.PushSubscriptionInput
	if v2.DecodeContract("PushSubscriptionInput", body, &input) != nil {
		return key{}, false
	}
	return key{campaign: input.CampaignID, family: input.Family}, true
}

// limited runs under h.mu and records one subscription change.
func (h *Hub) limited(c *client) bool {
	now := h.now()
	start := now.Add(-h.cfg.RateWindow)
	i := 0
	for i < len(c.changes) && c.changes[i].Before(start) {
		i++
	}
	c.changes = c.changes[i:]
	if len(c.changes) >= h.cfg.RateLimit {
		return true
	}
	c.changes = append(c.changes, now)
	return false
}

func ackCode(err error) string {
	switch eventfeed.Code(err) {
	case "UNAUTHORIZED":
		return "unauthorized"
	case "WEB3_SESSION_UNAVAILABLE":
		return "session_unavailable"
	case "CAMPAIGN_NOT_FOUND":
		return "not_found"
	case "CAMPAIGN_INVALID_INPUT":
		return "invalid_request"
	default:
		return "unavailable"
	}
}

// Subscribe authorizes one key and records its current durable head as the
// baseline. The caller must run a durable catch-up after a successful ack; a
// mutation before that catch-up is covered by it, and a later one advances the
// head past this baseline.
//
// The returned function must run after the acknowledgement is sent; it closes a
// connection whose credential the subscribe found revoked.
func (s *Session) Subscribe(ctx context.Context, raw any) (map[string]any, func()) {
	k, ok := decodeKey(raw)
	if !ok {
		return ackFailure("invalid_request"), noop
	}
	h, c := s.hub, s.client
	h.mu.Lock()
	if c.closed || h.closed {
		h.mu.Unlock()
		return ackFailure("unavailable"), noop
	}
	if h.limited(c) {
		h.mu.Unlock()
		return ackFailure("rate_limited"), noop
	}
	if k.family == "personal" && c.subject == nil {
		h.mu.Unlock()
		return ackFailure("unauthorized"), noop
	}
	existing := c.subs[k]
	if existing != nil && existing.reserved {
		h.mu.Unlock()
		return ackFailure("rate_limited"), noop
	}
	var reserved *subscription
	if existing == nil {
		if len(c.subs) >= h.cfg.PerConnection {
			h.mu.Unlock()
			return ackFailure("too_many_subscriptions"), noop
		}
		if h.total >= h.cfg.Total {
			h.mu.Unlock()
			return ackFailure("capacity"), noop
		}
		reserved = &subscription{client: c, key: k, reserved: true}
		c.subs[k] = reserved
		h.total++
	}
	// The subscription this operation targets is its identity: an unsubscribe,
	// a replacement or a removal while the observation runs makes it stale.
	target := existing
	if reserved != nil {
		target = reserved
	}
	cctx := c.ctx
	subject := c.subject
	h.mu.Unlock()

	head, err := h.observeNow(ctx, cctx, subject, k)

	h.mu.Lock()
	var frames []frame
	var closing []Conn
	result := ackSuccess(k)
	current := c.subs[k] == target && (target.reserved || target.active)
	switch {
	case c.closed || h.closed:
		result = ackFailure("unavailable")
	case err != nil && k.family == "personal" && ackCode(err) == "unauthorized":
		// A dead credential ends the connection whether or not this key is
		// still wanted.
		result = ackFailure("unauthorized")
		frames, closing = h.revoke(c)
	case !current:
		result = ackFailure("cancelled")
	case err != nil:
		result = ackFailure(ackCode(err))
		if reserved != nil || result["code"] == "not_found" || result["code"] == "unauthorized" || result["code"] == "session_unavailable" {
			h.remove(target)
		}
	case reserved != nil:
		reserved.reserved, reserved.active, reserved.delivered = false, true, head
		if h.byCampaign[k.campaign] == nil {
			h.byCampaign[k.campaign] = map[*subscription]struct{}{}
		}
		h.byCampaign[k.campaign][reserved] = struct{}{}
	default:
		if !existing.outstanding && head > existing.delivered {
			existing.delivered = head
		}
	}
	h.mu.Unlock()
	// Reset frames for the connection's other private subscriptions precede
	// the failing acknowledgement; the disconnect follows both.
	send(frames, nil)
	return result, func() { send(nil, closing) }
}

func (h *Hub) observeNow(ctx, owner context.Context, subject *eventfeed.Subject, k key) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, h.cfg.ObserveTimeout)
	defer cancel()
	stop := context.AfterFunc(owner, cancel)
	defer stop()
	select {
	case h.slots <- struct{}{}:
	case <-ctx.Done():
		return 0, &v2.DomainError{Code: "CAMPAIGN_UNAVAILABLE", Status: 503}
	}
	defer func() { <-h.slots; h.nudge() }()
	return h.observer.Observe(ctx, subject, k.campaign, k.family)
}

// Unsubscribe removes exactly the (campaignId, family) key it names, whether
// it is active or still being authorized. It only releases resources, so it is
// neither rate limited nor counted: a refused unsubscribe would leave the
// server holding a key the client no longer tracks.
func (s *Session) Unsubscribe(raw any) map[string]any {
	k, ok := decodeKey(raw)
	if !ok {
		return ackFailure("invalid_request")
	}
	h, c := s.hub, s.client
	h.mu.Lock()
	defer h.mu.Unlock()
	if sub := c.subs[k]; sub != nil {
		h.remove(sub)
	}
	return ackSuccess(k)
}

// Close releases the connection's subscriptions and cancels its observations.
func (s *Session) Close() {
	h, c := s.hub, s.client
	h.mu.Lock()
	defer h.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	c.cancel()
	h.dropClient(c)
}
