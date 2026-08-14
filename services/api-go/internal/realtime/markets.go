package realtime

import (
	"github.com/zishang520/socket.io/v2/socket"
)

// Snapshot is what the market-stream worker pushes for one (symbol, chainId).
// Ticker/Bids/Asks/Trades are already in the FE-facing JSON shape and emitted
// verbatim, mirroring MarketStreamSnapshot consumption in markets.gateway.ts.
type Snapshot struct {
	Symbol    string
	ChainID   int
	Ticker    any
	Bids      any
	Asks      any
	Trades    any
	UpdatedAt string
}

// SnapshotFunc returns the latest cached snapshot for (symbol, chainID), or nil
// on cache miss (the worker refreshes on an interval; we never DB-read here).
type SnapshotFunc func(symbol string, chainID int) *Snapshot

// emitter is satisfied by both *socket.Socket and *socket.BroadcastOperator.
type emitter interface {
	Emit(ev string, args ...any) error
}

type marketsGateway struct {
	ns       socket.Namespace
	guard    *subscriptionGuard
	snapshot SnapshotFunc
}

func registerMarketsGateway(io *socket.Server, snapshot SnapshotFunc) *marketsGateway {
	g := &marketsGateway{
		ns:       io.Of("/markets", nil),
		guard:    newSubscriptionGuard(),
		snapshot: snapshot,
	}
	g.ns.On("connection", func(args ...any) {
		client, ok := args[0].(*socket.Socket)
		if !ok {
			return
		}
		id := string(client.Id())
		g.guard.registerSocket(id)
		client.On("disconnect", func(...any) { g.guard.releaseSocket(id) })
		for _, topic := range marketTopics {
			t := topic
			client.On("subscribe:market:"+string(t), g.onSubscribe(client, t))
			client.On("unsubscribe:market:"+string(t), g.onUnsubscribe(client, t))
		}
	})
	return g
}

func (g *marketsGateway) onSubscribe(client *socket.Socket, topic marketTopic) func(...any) {
	return func(args ...any) {
		ack := extractAck(args)
		symbol := normalizeKey(extractSymbol(args))
		if symbol == "" {
			ackError(ack, "invalid_key", "symbol is required and must be a short alphanumeric string")
			return
		}
		sub := buildSubscription(topic, symbol, extractChainID(args))
		if rej := g.guard.trySubscribe(string(client.Id()), sub.room); rej != nil {
			ackReject(ack, rej)
			return
		}
		client.Join(socket.Room(sub.room))
		// Emit the most recent cached snapshot to this client only (non-volatile).
		if g.snapshot != nil {
			if snap := g.snapshot(sub.symbol, sub.chainID); snap != nil {
				emitSnapshot(client, sub, snap)
			}
		}
		ackRespond(ack, subscribeAckPayload(topic, sub))
	}
}

func (g *marketsGateway) onUnsubscribe(client *socket.Socket, topic marketTopic) func(...any) {
	return func(args ...any) {
		ack := extractAck(args)
		symbol := normalizeKey(extractSymbol(args))
		if symbol == "" {
			ackError(ack, "invalid_key", "symbol is required")
			return
		}
		sub := buildSubscription(topic, symbol, extractChainID(args))
		if rej := g.guard.tryUnsubscribe(string(client.Id()), sub.room); rej != nil {
			ackReject(ack, rej)
			return
		}
		client.Leave(socket.Room(sub.room))
		ackRespond(ack, map[string]any{"success": true, "room": sub.room})
	}
}

// Broadcast fans a snapshot out to ticker/book/trades subscribers as volatile
// (tick-style: a stalled client drops frames). Called by the market-stream
// worker on every refresh.
func (g *marketsGateway) Broadcast(snap *Snapshot) {
	if g == nil || snap == nil {
		return
	}
	for _, topic := range marketTopics {
		sub := buildSubscription(topic, snap.Symbol, snap.ChainID)
		emitSnapshot(g.ns.To(socket.Room(sub.room)).Volatile(), sub, snap)
	}
}

func extractSymbol(args []any) string {
	if len(args) == 0 {
		return ""
	}
	if m, ok := args[0].(map[string]any); ok {
		if s, ok := m["symbol"].(string); ok {
			return s
		}
	}
	return ""
}

func extractChainID(args []any) int {
	if len(args) == 0 {
		return 0
	}
	if m, ok := args[0].(map[string]any); ok {
		if v, ok := m["chainId"].(float64); ok {
			return sanitizeChainID(v, true)
		}
	}
	return 0
}

// emitSnapshot mirrors emitSnapshotPayload(): per-topic event name + shape.
func emitSnapshot(e emitter, sub marketSubscription, snap *Snapshot) {
	switch sub.topic {
	case topicTicker:
		if snap.Ticker != nil {
			_ = e.Emit(sub.event, snap.Ticker)
		}
	case topicBook:
		_ = e.Emit(sub.event, withMeta(sub, snap, map[string]any{"bids": snap.Bids, "asks": snap.Asks}))
	case topicTrades:
		_ = e.Emit(sub.event, withMeta(sub, snap, map[string]any{"trades": snap.Trades}))
	}
}

func withMeta(sub marketSubscription, snap *Snapshot, body map[string]any) map[string]any {
	body["symbol"] = sub.symbol
	if sub.chainID != 0 {
		body["chainId"] = sub.chainID
	}
	body["updatedAt"] = snap.UpdatedAt
	return body
}
