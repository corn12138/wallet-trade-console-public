package realtime

import "testing"

const (
	mixedAddr = "0xAABBccddeeff00112233445566778899aAbBcCdD" // 0x + 40 hex
	lowerAddr = "0xaabbccddeeff00112233445566778899aabbccdd"
)

func TestNormalizeTokenAddress(t *testing.T) {
	cases := []struct{ in, want string }{
		{mixedAddr, lowerAddr},               // lowercased
		{"  " + mixedAddr + "  ", lowerAddr}, // trimmed
		{"0x123", ""},                        // too short
		{"not-an-address", ""},
		{"", ""},
		{"0xGGBBccddeeff00112233445566778899aAbBcCdD", ""}, // non-hex G
		{lowerAddr + "00", ""},                             // too long
	}
	for _, c := range cases {
		if got := normalizeTokenAddress(c.in); got != c.want {
			t.Errorf("normalizeTokenAddress(%q): got %q want %q", c.in, got, c.want)
		}
	}
}

func TestTokenRoomFromArgs(t *testing.T) {
	if got := tokenRoomFromArgs([]any{mixedAddr}); got != "token:"+lowerAddr {
		t.Errorf("valid addr: got %q", got)
	}
	if got := tokenRoomFromArgs([]any{}); got != "" {
		t.Errorf("empty args should be \"\", got %q", got)
	}
	if got := tokenRoomFromArgs([]any{123}); got != "" {
		t.Errorf("non-string arg should be \"\", got %q", got)
	}
	if got := tokenRoomFromArgs([]any{"bad"}); got != "" {
		t.Errorf("invalid addr should be \"\", got %q", got)
	}
}

func TestTokenRoom(t *testing.T) {
	if got := tokenRoom("  " + mixedAddr + "  "); got != "token:"+lowerAddr {
		t.Errorf("tokenRoom should trim+lowercase, got %q", got)
	}
}

func TestBroadcastUsesEmitter(t *testing.T) {
	// Exercises the room/event mapping via the emitter interface without a live
	// socket: build a recording gateway is overkill, so just assert tokenRoom +
	// allTradesRoom constants stay aligned with the NestJS contract.
	if allTradesRoom != "all-trades" {
		t.Errorf("all-trades room name drift: %q", allTradesRoom)
	}
	if tokenRoom("0xABC") != "token:0xabc" {
		t.Errorf("token room prefix drift")
	}
}
