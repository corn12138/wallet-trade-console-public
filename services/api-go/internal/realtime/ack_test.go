package realtime

import (
	"testing"

	"github.com/zishang520/socket.io/v2/socket"
)

// captureAck returns a socket.Ack that records the first payload map it receives,
// so ack shapes can be asserted without a live socket.
func captureAck(dst *map[string]any) socket.Ack {
	return func(args []any, _ error) {
		if len(args) > 0 {
			if m, ok := args[0].(map[string]any); ok {
				*dst = m
			}
		}
	}
}

func TestExtractAck(t *testing.T) {
	called := false
	ack := socket.Ack(func([]any, error) { called = true })

	got := extractAck([]any{map[string]any{"symbol": "ETH-USD"}, ack})
	if got == nil {
		t.Fatal("expected ack extracted from trailing arg")
	}
	got(nil, nil)
	if !called {
		t.Error("extracted ack is not the supplied callback")
	}

	if extractAck([]any{map[string]any{"symbol": "ETH-USD"}}) != nil {
		t.Error("payload-only args should yield nil ack (current FE emits without a callback)")
	}
	if extractAck(nil) != nil {
		t.Error("empty args should yield nil ack")
	}
}

func TestAckHelpersNilIsNoop(t *testing.T) {
	// A client that emitted without a callback => nil ack => no panic, no send.
	ackRespond(nil, map[string]any{"success": true})
	ackError(nil, "invalid_key", "symbol is required")
	ackReject(nil, &guardRejection{Code: "rate_limited", Message: "slow down"})
}

func TestSubscribeAckPayload_WithChain(t *testing.T) {
	sub := buildSubscription(topicTicker, "ETH-USD", 11155111)
	p := subscribeAckPayload(topicTicker, sub)
	if p["success"] != true || p["room"] != sub.room || p["topic"] != "ticker" ||
		p["symbol"] != "ETH-USD" || p["event"] != "market:ticker:ETH-USD" {
		t.Errorf("subscribe ack payload mismatch: %+v", p)
	}
	if p["chainId"] != 11155111 {
		t.Errorf("chainId should be present when set: %+v", p)
	}
}

func TestSubscribeAckPayload_OmitsChainWhenZero(t *testing.T) {
	sub := buildSubscription(topicBook, "ETH-USD", 0)
	p := subscribeAckPayload(topicBook, sub)
	if _, ok := p["chainId"]; ok {
		t.Errorf("chainId must be omitted when 0/absent (NestJS undefined-drop): %+v", p)
	}
}

func TestAckErrorShape(t *testing.T) {
	var got map[string]any
	ackError(captureAck(&got), "invalid_key", "symbol is required")
	if got["success"] != false || got["code"] != "invalid_key" || got["message"] != "symbol is required" {
		t.Errorf("ackError shape mismatch: %+v", got)
	}
}

func TestAckRejectShape(t *testing.T) {
	var got map[string]any
	ackReject(captureAck(&got), &guardRejection{Code: "too_many_subscriptions", Message: "Subscription limit of 64 reached"})
	if got["success"] != false || got["code"] != "too_many_subscriptions" ||
		got["message"] != "Subscription limit of 64 reached" {
		t.Errorf("ackReject shape mismatch: %+v", got)
	}
}

func TestExtractTokenAddress(t *testing.T) {
	if got := extractTokenAddress([]any{mixedAddr}); got != lowerAddr {
		t.Errorf("valid addr should normalize, got %q", got)
	}
	if got := extractTokenAddress([]any{mixedAddr, socket.Ack(func([]any, error) {})}); got != lowerAddr {
		t.Errorf("addr + trailing ack should still resolve, got %q", got)
	}
	if got := extractTokenAddress([]any{123}); got != "" {
		t.Errorf("non-string should be empty, got %q", got)
	}
	if got := extractTokenAddress(nil); got != "" {
		t.Errorf("empty should be empty, got %q", got)
	}
}
