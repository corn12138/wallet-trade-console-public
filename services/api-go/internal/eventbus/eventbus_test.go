package eventbus

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

type recordedBroadcast struct {
	kind    string
	token   string
	payload any
}

type fakeHandler struct {
	events []recordedBroadcast
}

func (f *fakeHandler) BroadcastTrade(tokenAddr string, trade any) {
	f.events = append(f.events, recordedBroadcast{KindTrade, tokenAddr, trade})
}
func (f *fakeHandler) BroadcastPriceUpdate(tokenAddr string, update any) {
	f.events = append(f.events, recordedBroadcast{KindPriceUpdate, tokenAddr, update})
}
func (f *fakeHandler) BroadcastGraduation(tokenAddr string, payload any) {
	f.events = append(f.events, recordedBroadcast{KindGraduation, tokenAddr, payload})
}

func TestEncodeDecodeRoundtrip(t *testing.T) {
	ev, err := NewTokenEvent(KindTrade, "0xAABB00000000000000000000000000000000CCdd", TradePayload{
		TokenAddress: "0xaabb00000000000000000000000000000000ccdd",
		Type:         "BUY",
		Price:        "1.25",
		TxHash:       "0xhash",
	})
	if err != nil {
		t.Fatalf("NewTokenEvent: %v", err)
	}
	if ev.TokenAddress != "0xaabb00000000000000000000000000000000ccdd" {
		t.Errorf("address not normalized: %q", ev.TokenAddress)
	}
	raw, err := EncodeTokenEvent(ev)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	back, err := DecodeTokenEvent(raw)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if back.Kind != KindTrade || back.TokenAddress != ev.TokenAddress {
		t.Errorf("roundtrip = %+v", back)
	}
}

func TestDecodeRejectsGarbage(t *testing.T) {
	if _, err := DecodeTokenEvent("not json"); err == nil {
		t.Errorf("garbage accepted")
	}
	if _, err := DecodeTokenEvent(`{"payload":{}}`); err == nil {
		t.Errorf("missing kind accepted")
	}
}

func TestDispatchRoutesByKind(t *testing.T) {
	h := &fakeHandler{}
	for _, kind := range []string{KindTrade, KindPriceUpdate, KindGraduation} {
		ev, err := NewTokenEvent(kind, "0xToKen", map[string]any{"k": kind})
		if err != nil {
			t.Fatalf("NewTokenEvent(%s): %v", kind, err)
		}
		raw, _ := EncodeTokenEvent(ev)
		if err := DispatchEncoded(h, raw); err != nil {
			t.Fatalf("DispatchEncoded(%s): %v", kind, err)
		}
	}
	if len(h.events) != 3 {
		t.Fatalf("events = %d, want 3", len(h.events))
	}
	for i, kind := range []string{KindTrade, KindPriceUpdate, KindGraduation} {
		got := h.events[i]
		if got.kind != kind || got.token != "0xtoken" {
			t.Errorf("event[%d] = %+v", i, got)
		}
		payload, ok := got.payload.(map[string]any)
		if !ok || payload["k"] != kind {
			t.Errorf("event[%d] payload = %#v", i, got.payload)
		}
	}
}

func TestDispatchIgnoresUnknownKind(t *testing.T) {
	h := &fakeHandler{}
	if err := DispatchEncoded(h, `{"kind":"mystery","tokenAddress":"0xt","payload":{}}`); err != nil {
		t.Fatalf("unknown kind should not error: %v", err)
	}
	if len(h.events) != 0 {
		t.Errorf("unknown kind reached a broadcast: %+v", h.events)
	}
}

type fakeExecer struct {
	sql  string
	args []any
	err  error
}

func (f *fakeExecer) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	f.sql, f.args = sql, args
	return pgconn.CommandTag{}, f.err
}

func TestPgPublisherSendsNotify(t *testing.T) {
	db := &fakeExecer{}
	pub := NewPgPublisher(db)
	ev, _ := NewTokenEvent(KindPriceUpdate, "0xT", PricePayload{Price: "2.5"})
	if err := pub.PublishTokenEvent(context.Background(), ev); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if !strings.Contains(db.sql, "pg_notify") {
		t.Errorf("sql = %q", db.sql)
	}
	if len(db.args) != 2 || db.args[0] != TokenEventsChannel {
		t.Fatalf("args = %v", db.args)
	}
	// The published payload must be the exact wire encoding DecodeTokenEvent
	// accepts — proving publisher and listener share one contract.
	if _, err := DecodeTokenEvent(db.args[1].(string)); err != nil {
		t.Errorf("published payload not decodable: %v", err)
	}
}

func TestPgPublisherWithoutDBErrors(t *testing.T) {
	pub := NewPgPublisher(nil)
	ev, _ := NewTokenEvent(KindTrade, "0xT", TradePayload{})
	if err := pub.PublishTokenEvent(context.Background(), ev); err == nil {
		t.Errorf("nil db accepted")
	}
}
