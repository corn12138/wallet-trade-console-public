package realtime

import "testing"

func TestBuildSubscription(t *testing.T) {
	cases := []struct {
		topic           marketTopic
		symbol          string
		chainID         int
		wantRoom, event string
	}{
		{topicTicker, "eth-usd", 11155111, "market:ticker:ETH-USD:11155111", "market:ticker:ETH-USD"},
		{topicBook, "btc", 0, "market:book:BTC:all", "market:book:BTC"},
		{topicTrades, "  Eth-Usd  ", 1, "market:trades:ETH-USD:1", "market:trades:ETH-USD"},
	}
	for _, c := range cases {
		got := buildSubscription(c.topic, c.symbol, c.chainID)
		if got.room != c.wantRoom {
			t.Errorf("room: got %q want %q", got.room, c.wantRoom)
		}
		if got.event != c.event {
			t.Errorf("event: got %q want %q", got.event, c.event)
		}
	}
}

func TestSanitizeChainID(t *testing.T) {
	cases := []struct {
		v       float64
		present bool
		want    int
	}{
		{1, true, 1},
		{11155111, true, 11155111},
		{0, true, 0},
		{-5, true, 0},
		{1.5, true, 0},
		{4294967295, true, 4294967295}, // 0xffffffff ok
		{4294967296, true, 0},          // > 0xffffffff
		{42, false, 0},                 // absent
	}
	for _, c := range cases {
		if got := sanitizeChainID(c.v, c.present); got != c.want {
			t.Errorf("sanitizeChainID(%v,%v): got %d want %d", c.v, c.present, got, c.want)
		}
	}
}

func TestNormalizeKey(t *testing.T) {
	cases := []struct{ in, want string }{
		{"eth-usd", "eth-usd"},
		{"  ETH_USD  ", "ETH_USD"},
		{"a.b:c/d-e", "a.b:c/d-e"},
		{"", ""},
		{"   ", ""},
		{"bad symbol!", ""}, // space + ! fail SAFE_KEY_RE
		{"emoji😀", ""},
	}
	for _, c := range cases {
		if got := normalizeKey(c.in); got != c.want {
			t.Errorf("normalizeKey(%q): got %q want %q", c.in, got, c.want)
		}
	}
	// length cap: 64 ok, 65 rejected
	if got := normalizeKey(repeat("a", 64)); got == "" {
		t.Errorf("64-char key should be valid")
	}
	if got := normalizeKey(repeat("a", 65)); got != "" {
		t.Errorf("65-char key should be rejected, got %q", got)
	}
}

func repeat(s string, n int) string {
	out := make([]byte, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, s[0])
	}
	return string(out)
}

func TestEmitSnapshotShapes(t *testing.T) {
	rec := &recordingEmitter{}
	snap := &Snapshot{Symbol: "ETH-USD", ChainID: 1, Ticker: map[string]any{"price": "1"}, Bids: []any{}, Asks: []any{}, Trades: []any{}, UpdatedAt: "t"}

	emitSnapshot(rec, buildSubscription(topicTicker, "eth-usd", 1), snap)
	if rec.last.event != "market:ticker:ETH-USD" {
		t.Errorf("ticker event: %q", rec.last.event)
	}

	emitSnapshot(rec, buildSubscription(topicBook, "eth-usd", 1), snap)
	body, _ := rec.last.payload.(map[string]any)
	if body["symbol"] != "ETH-USD" || body["chainId"] != 1 || body["updatedAt"] != "t" {
		t.Errorf("book meta wrong: %+v", body)
	}
	if _, ok := body["bids"]; !ok {
		t.Errorf("book missing bids")
	}

	// chainID 0 omits chainId key (matches NestJS spreading undefined)
	emitSnapshot(rec, buildSubscription(topicTrades, "eth-usd", 0), snap)
	body2, _ := rec.last.payload.(map[string]any)
	if _, present := body2["chainId"]; present {
		t.Errorf("chainId should be omitted when 0")
	}
}

type recordingEmitter struct {
	last struct {
		event   string
		payload any
	}
}

func (r *recordingEmitter) Emit(ev string, args ...any) error {
	r.last.event = ev
	if len(args) > 0 {
		r.last.payload = args[0]
	}
	return nil
}
