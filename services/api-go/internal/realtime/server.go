package realtime

import (
	"net/http"

	enginetypes "github.com/zishang520/engine.io/v2/types"
	"github.com/zishang520/socket.io/v2/socket"
)

// Server hosts the Socket.IO v4-compatible realtime tier (ADR 0006). It mounts
// the same namespaces the NestJS gateways used (markets now; token-events
// next), so the unchanged socket.io-client@4 frontend connects without changes.
type Server struct {
	io          *socket.Server
	markets     *marketsGateway
	tokenEvents *tokenEventsGateway
}

// Options configures the realtime server.
type Options struct {
	// MarketSnapshot returns the latest cached market snapshot for a
	// (symbol, chainID) so a fresh subscriber gets an immediate frame. May be
	// nil (then subscribers wait for the next worker broadcast).
	MarketSnapshot SnapshotFunc
}

// NewServer builds the realtime server and registers the namespaces.
func NewServer(opts Options) *Server {
	// WebTransport requires a separate UDP/TLS listener and must not become
	// reachable through this net/http handler after a dependency upgrade.
	serverOptions := socket.DefaultServerOptions()
	serverOptions.SetTransports(enginetypes.NewSet("polling", "websocket"))
	io := socket.NewServer(nil, serverOptions)
	s := &Server{io: io}
	s.markets = registerMarketsGateway(io, opts.MarketSnapshot)
	s.tokenEvents = registerTokenEventsGateway(io)
	return s
}

// Handler is the http.Handler for the `/socket.io/` route (Engine.IO + Socket.IO
// transport). Mount it in cmd/api so nginx `/socket.io/` can repoint to Go.
func (s *Server) Handler() http.Handler {
	return s.io.ServeHandler(nil)
}

// BroadcastMarket pushes a market snapshot to subscribed rooms. Called by the
// market-stream worker (Phase 4) on every refresh.
func (s *Server) BroadcastMarket(snap *Snapshot) {
	if s == nil || s.markets == nil {
		return
	}
	s.markets.Broadcast(snap)
}

// BroadcastTrade / BroadcastPriceUpdate / BroadcastGraduation push token-events
// to subscribers. Called by the indexer when it projects a Buy/Sell or a
// bonding-curve graduation.
func (s *Server) BroadcastTrade(tokenAddr string, trade any) {
	if s != nil && s.tokenEvents != nil {
		s.tokenEvents.BroadcastTrade(tokenAddr, trade)
	}
}

func (s *Server) BroadcastPriceUpdate(tokenAddr string, update any) {
	if s != nil && s.tokenEvents != nil {
		s.tokenEvents.BroadcastPriceUpdate(tokenAddr, update)
	}
}

func (s *Server) BroadcastGraduation(tokenAddr string, payload any) {
	if s != nil && s.tokenEvents != nil {
		s.tokenEvents.BroadcastGraduation(tokenAddr, payload)
	}
}
