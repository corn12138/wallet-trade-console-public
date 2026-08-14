package realtime

import "github.com/zishang520/socket.io/v2/socket"

// Socket.IO ack parity (ADR 0006): NestJS @SubscribeMessage handlers return a
// value, which socket.io delivers to the client's emit callback when one was
// supplied. The Go server gets the same ability — socket.io appends an ack
// callback (socket.Ack = func([]any, error)) as the trailing handler argument
// when the JS client emits with a callback. These helpers mirror the
// markets/token-events gateway return shapes so a `socket.io-client@4` caller
// that passes `(payload, cb)` receives the same `{success:true,...}` /
// `{success:false, code, message}` acknowledgement it got from NestJS.
//
// The current frontend hook (useMarketStream) emits WITHOUT a callback, so it
// is unaffected — extractAck returns nil and no ack is sent, exactly as before.
// Parity matters for future/external clients and protocol tests.

// extractAck returns the ack callback the client supplied as the trailing emit
// argument, or nil when none was sent.
func extractAck(args []any) socket.Ack {
	if len(args) == 0 {
		return nil
	}
	if fn, ok := args[len(args)-1].(socket.Ack); ok {
		return fn
	}
	return nil
}

// ackRespond invokes the client's ack callback with a single payload, when one
// was supplied. NestJS only calls a callback the client provided; otherwise the
// returned object is dropped — so a nil ack is a no-op here.
func ackRespond(ack socket.Ack, payload map[string]any) {
	if ack != nil {
		ack([]any{payload}, nil)
	}
}

// ackError sends the NestJS `{ success:false, code, message }` shape (invalid
// key / validation failure).
func ackError(ack socket.Ack, code, message string) {
	ackRespond(ack, map[string]any{"success": false, "code": code, "message": message})
}

// ackReject maps a guard rejection to NestJS's `{ success:false, ...rejection }`.
func ackReject(ack socket.Ack, rej *guardRejection) {
	ackError(ack, rej.Code, rej.Message)
}

// subscribeAckPayload mirrors the markets.gateway.ts subscribe success return:
//
//	{ success:true, room, topic, symbol, chainId?, event }
//
// chainId is omitted when 0/absent, matching NestJS where `subscription.chainId`
// is `undefined` and JSON serialization drops it.
func subscribeAckPayload(topic marketTopic, sub marketSubscription) map[string]any {
	payload := map[string]any{
		"success": true,
		"room":    sub.room,
		"topic":   string(topic),
		"symbol":  sub.symbol,
		"event":   sub.event,
	}
	if sub.chainID != 0 {
		payload["chainId"] = sub.chainID
	}
	return payload
}
