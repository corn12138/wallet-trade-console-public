package realtime

import (
	"strings"

	"github.com/zishang520/socket.io/v2/socket"
)

// allTradesRoom mirrors the NestJS token-events 'all-trades' room.
const allTradesRoom = "all-trades"

// tokenEventsGateway ports legacy NestJS token/token-events.gateway.ts: the
// `token-events` namespace with per-token rooms (token:{addr}) plus the global
// all-trades room, fed by the indexer (trade / price-update / graduation).
type tokenEventsGateway struct {
	ns    socket.Namespace
	guard *subscriptionGuard
}

func registerTokenEventsGateway(io *socket.Server) *tokenEventsGateway {
	g := &tokenEventsGateway{
		ns:    io.Of("/token-events", nil),
		guard: newSubscriptionGuard(),
	}
	g.ns.On("connection", func(args ...any) {
		client, ok := args[0].(*socket.Socket)
		if !ok {
			return
		}
		id := string(client.Id())
		g.guard.registerSocket(id)
		client.On("disconnect", func(...any) { g.guard.releaseSocket(id) })
		client.On("subscribe:token", g.onTokenSub(client, true))
		client.On("unsubscribe:token", g.onTokenSub(client, false))
		client.On("subscribe:trades", g.onTradesSub(client, true))
		client.On("unsubscribe:trades", g.onTradesSub(client, false))
	})
	return g
}

// onTokenSub ports token-events.gateway.ts handleSubscribe/handleUnsubscribe:
// the address arrives as a bare string, an invalid one returns the NestJS
// invalid_key ack, and success/guard-rejection mirror the NestJS shapes.
func (g *tokenEventsGateway) onTokenSub(client *socket.Socket, join bool) func(...any) {
	return func(args ...any) {
		ack := extractAck(args)
		addr := extractTokenAddress(args)
		if addr == "" {
			ackError(ack, "invalid_key", "tokenAddress must be a 0x-prefixed 20-byte hex string")
			return
		}
		room := "token:" + addr
		id := string(client.Id())
		if join {
			if rej := g.guard.trySubscribe(id, room); rej != nil {
				ackReject(ack, rej)
				return
			}
			client.Join(socket.Room(room))
			ackRespond(ack, map[string]any{"success": true, "room": room, "tokenAddress": addr})
			return
		}
		if rej := g.guard.tryUnsubscribe(id, room); rej != nil {
			ackReject(ack, rej)
			return
		}
		client.Leave(socket.Room(room))
		ackRespond(ack, map[string]any{"success": true, "room": room})
	}
}

// onTradesSub ports handleSubscribeTrades/handleUnsubscribeTrades: the
// all-trades room takes no body. Note the asymmetry NestJS has — subscribe acks
// {success:true, room}, unsubscribe acks {success:true} (no room).
func (g *tokenEventsGateway) onTradesSub(client *socket.Socket, join bool) func(...any) {
	return func(args ...any) {
		ack := extractAck(args)
		id := string(client.Id())
		if join {
			if rej := g.guard.trySubscribe(id, allTradesRoom); rej != nil {
				ackReject(ack, rej)
				return
			}
			client.Join(socket.Room(allTradesRoom))
			ackRespond(ack, map[string]any{"success": true, "room": allTradesRoom})
			return
		}
		if rej := g.guard.tryUnsubscribe(id, allTradesRoom); rej != nil {
			ackReject(ack, rej)
			return
		}
		client.Leave(socket.Room(allTradesRoom))
		ackRespond(ack, map[string]any{"success": true})
	}
}

// extractTokenAddress reads the bare-string token address from the subscribe
// args (ignoring any trailing ack callback) and returns the normalized lowercase
// address, or "" when missing/invalid.
func extractTokenAddress(args []any) string {
	if len(args) == 0 {
		return ""
	}
	s, ok := args[0].(string)
	if !ok {
		return ""
	}
	return normalizeTokenAddress(s)
}

// tokenRoomFromArgs returns the token room (token:{addr}) for a subscribe arg,
// or "" when the address is missing/invalid.
func tokenRoomFromArgs(args []any) string {
	addr := extractTokenAddress(args)
	if addr == "" {
		return ""
	}
	return "token:" + addr
}

// BroadcastTrade mirrors emitTrade: volatile 'trade' to the token room + all-trades.
func (g *tokenEventsGateway) BroadcastTrade(tokenAddr string, trade any) {
	room := tokenRoom(tokenAddr)
	_ = g.ns.To(socket.Room(room)).Volatile().Emit("trade", trade)
	_ = g.ns.To(socket.Room(allTradesRoom)).Volatile().Emit("trade", trade)
}

// BroadcastPriceUpdate mirrors emitPriceUpdate: volatile 'price-update' to the
// token room only.
func (g *tokenEventsGateway) BroadcastPriceUpdate(tokenAddr string, update any) {
	_ = g.ns.To(socket.Room(tokenRoom(tokenAddr))).Volatile().Emit("price-update", update)
}

// BroadcastGraduation mirrors emitGraduation: reliable (non-volatile) 'graduation'
// to the token room + all-trades.
func (g *tokenEventsGateway) BroadcastGraduation(tokenAddr string, payload any) {
	room := tokenRoom(tokenAddr)
	_ = g.ns.To(socket.Room(room)).Emit("graduation", payload)
	_ = g.ns.To(socket.Room(allTradesRoom)).Emit("graduation", payload)
}

func tokenRoom(tokenAddr string) string {
	return "token:" + strings.ToLower(strings.TrimSpace(tokenAddr))
}
