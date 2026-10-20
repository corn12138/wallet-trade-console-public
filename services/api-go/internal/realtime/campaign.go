package realtime

import (
	"context"
	"sync"
	"time"

	"github.com/zishang520/socket.io/v2/socket"
)

// CampaignNamespace carries authenticated Campaign invalidation hints. The
// Campaign domain owns authorization and payloads; this file only adapts the
// Socket.IO transport.
const CampaignNamespace = "/campaign-events"

// PushConn is the transport side handed to the Campaign push runtime.
type PushConn interface {
	Send(event string, payload map[string]any, ack func(delivered bool))
	Close()
}

// PushSession is one authenticated or anonymous Campaign connection.
type PushSession interface {
	Subscribe(ctx context.Context, request any) (map[string]any, func())
	Unsubscribe(request any) map[string]any
	Close()
}

// CampaignPush verifies the CONNECT packet's auth object, the only place a
// credential may travel (a non-empty code rejects the connection), and attaches
// a connected transport. Attach returns nil when the runtime is closed.
type CampaignPush interface {
	Authenticate(ctx context.Context, auth any) (identity any, code string)
	Attach(identity any, conn PushConn) PushSession
	AckTimeout() time.Duration
}

type socketPushConn struct {
	mu      sync.Mutex
	client  *socket.Socket
	timeout time.Duration
}

// Send serializes emits: Socket.IO stores per-emit flags (timeout) on the
// socket, so concurrent emits would otherwise race on them.
func (c *socketPushConn) Send(event string, payload map[string]any, ack func(bool)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.client.Connected() {
		if ack != nil {
			ack(false)
		}
		return
	}
	if ack == nil {
		_ = c.client.Emit(event, payload)
		return
	}
	_ = c.client.Timeout(c.timeout).Emit(event, payload, socket.Ack(func(_ []any, err error) { ack(err == nil) }))
}

// Close leaves only this namespace: a browser multiplexes /markets and
// /token-events over the same engine connection.
func (c *socketPushConn) Close() { c.client.Disconnect(false) }

type pushIdentity struct{ value any }

func registerCampaignGateway(io *socket.Server, push CampaignPush) {
	ns := io.Of(CampaignNamespace, nil)
	ns.Use(func(client *socket.Socket, next func(*socket.ExtendedError)) {
		var auth any
		if handshake := client.Handshake(); handshake != nil {
			auth = handshake.Auth
		}
		// Session verification reads the database; keep it off the engine's
		// connection goroutine.
		go func() {
			identity, code := push.Authenticate(context.Background(), auth)
			if code != "" {
				next(socket.NewExtendedError(code, map[string]any{"code": code}))
				return
			}
			client.SetData(pushIdentity{identity})
			next(nil)
		}()
	})
	ns.On("connection", func(args ...any) {
		client, ok := args[0].(*socket.Socket)
		if !ok {
			return
		}
		data, verified := client.Data().(pushIdentity)
		var session PushSession
		if verified {
			session = push.Attach(data.value, &socketPushConn{client: client, timeout: push.AckTimeout()})
		}
		if session == nil {
			client.Disconnect(false)
			return
		}
		client.On("disconnect", func(...any) { session.Close() })
		if client.Disconnected() {
			session.Close()
			return
		}
		client.On("campaign:subscribe", func(args ...any) {
			ack := extractAck(args)
			result, after := session.Subscribe(context.Background(), firstArg(args))
			ackRespond(ack, result)
			after()
		})
		client.On("campaign:unsubscribe", func(args ...any) {
			ackRespond(extractAck(args), session.Unsubscribe(firstArg(args)))
		})
	})
}

func firstArg(args []any) any {
	if len(args) == 0 {
		return nil
	}
	if _, isAck := args[0].(socket.Ack); isAck {
		return nil
	}
	return args[0]
}
