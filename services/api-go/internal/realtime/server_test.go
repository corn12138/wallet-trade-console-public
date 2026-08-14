package realtime

import "testing"

func TestNewServerDoesNotEnableWebTransport(t *testing.T) {
	server := NewServer(Options{})
	_ = server.Handler()
	transports := server.io.Engine().Opts().Transports()
	if transports == nil {
		t.Fatal("engine transports are nil")
	}
	if !transports.Has("polling") || !transports.Has("websocket") {
		t.Fatalf("enabled transports = %v, want polling and websocket", transports.Keys())
	}
	if transports.Has("webtransport") {
		t.Fatalf("enabled transports = %v, webtransport must stay disabled", transports.Keys())
	}
}
