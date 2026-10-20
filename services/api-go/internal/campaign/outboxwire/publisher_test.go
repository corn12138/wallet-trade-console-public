package outboxwire

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/durableoutbox"
	"github.com/jackc/pgx/v5/pgconn"
)

type execCall struct {
	query string
	args  []any
}
type recordingExecer struct {
	mu    sync.Mutex
	calls []execCall
	err   error
}

func (r *recordingExecer) Exec(_ context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, execCall{query, args})
	return pgconn.CommandTag{}, r.err
}
func event() durableoutbox.Event {
	return durableoutbox.Event{ID: "a2345678-1234-4234-8234-123456789abc", Source: "campaign", Topic: "campaign.publication.changed", PayloadVersion: 1,
		Stream: "PRIVATE_STREAM_SENTINEL", Version: 91, Payload: json.RawMessage(`{"state":"PRIVATE_PAYLOAD_SENTINEL"}`)}
}
func ptr(s string) *string { return &s }

func TestUnsupportedNeverExecutes(t *testing.T) {
	cases := map[string]func(*durableoutbox.Event){
		"source":                  func(e *durableoutbox.Event) { e.Source = "chain" },
		"version":                 func(e *durableoutbox.Event) { e.PayloadVersion = 2 },
		"zero version":            func(e *durableoutbox.Event) { e.PayloadVersion = 0 },
		"unknown topic":           func(e *durableoutbox.Event) { e.Topic = "campaign.future.changed" },
		"topic suffix":            func(e *durableoutbox.Event) { e.Topic += ".private" },
		"empty id":                func(e *durableoutbox.Event) { e.ID = "" },
		"nil id":                  func(e *durableoutbox.Event) { e.ID = "00000000-0000-0000-0000-000000000000" },
		"oversized id":            func(e *durableoutbox.Event) { e.ID = strings.Repeat("a", 9000) },
		"id injection":            func(e *durableoutbox.Event) { e.ID = `"},"owner":"secret"` },
		"noncanonical id":         func(e *durableoutbox.Event) { e.ID = strings.ToUpper(e.ID) },
		"public owner":            func(e *durableoutbox.Event) { e.OwnerScope = ptr("0x" + strings.Repeat("a", 40)) },
		"public empty owner":      func(e *durableoutbox.Event) { e.OwnerScope = ptr("") },
		"private absent owner":    func(e *durableoutbox.Event) { e.Topic = "campaign.personal.changed" },
		"private empty owner":     func(e *durableoutbox.Event) { e.Topic = "campaign.personal.changed"; e.OwnerScope = ptr("") },
		"private malformed owner": func(e *durableoutbox.Event) { e.Topic = "campaign.personal.changed"; e.OwnerScope = ptr("secret") },
		"private zero owner": func(e *durableoutbox.Event) {
			e.Topic = "campaign.personal.changed"
			e.OwnerScope = ptr("0x" + strings.Repeat("0", 40))
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			e := event()
			mutate(&e)
			db := &recordingExecer{}
			p := New(db)
			if p.Supports(e) {
				t.Fatal("unsupported event accepted")
			}
			if err := p.Publish(context.Background(), e); !errors.Is(err, durableoutbox.ErrUnsupported) {
				t.Fatalf("Publish: %v", err)
			}
			if len(db.calls) != 0 {
				t.Fatal("unsupported event reached transport")
			}
		})
	}
}

func TestPublicAndPrivateHaveIdenticalIDOnlyWakeup(t *testing.T) {
	db := &recordingExecer{}
	p := New(db)
	for _, topic := range []string{"campaign.publication.changed", "campaign.interaction.changed", "campaign.personal.changed"} {
		e := event()
		e.Topic = topic
		if topic == "campaign.personal.changed" {
			e.OwnerScope = ptr("0x" + strings.Repeat("b", 40))
		}
		if !p.Supports(e) {
			t.Fatalf("unsupported %s", topic)
		}
		if err := p.Publish(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	want := `{"schemaVersion":1,"eventId":"a2345678-1234-4234-8234-123456789abc"}`
	for _, call := range db.calls {
		if call.query != `SELECT pg_notify($1, $2)` || len(call.args) != 2 || call.args[0] != Channel || call.args[1] != want {
			t.Fatalf("unexpected transport envelope: %#v", call)
		}
		if len(call.args[1].(string)) > 96 {
			t.Fatal("unbounded wakeup")
		}
	}
}

func TestTransportErrorReturnedWithoutWrapping(t *testing.T) {
	transportErr := errors.New("transport diagnostic must be masked by dispatcher")
	if err := New(&recordingExecer{err: transportErr}).Publish(context.Background(), event()); err != transportErr {
		t.Fatal("transport error identity changed")
	}
}
func TestCanceledContextNeverExecutes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	db := &recordingExecer{}
	if err := New(db).Publish(ctx, event()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Publish: %v", err)
	}
	if len(db.calls) != 0 {
		t.Fatal("canceled publish reached transport")
	}
}

type waitingExecer struct{}

func (waitingExecer) Exec(ctx context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
	<-ctx.Done()
	return pgconn.CommandTag{}, ctx.Err()
}
func TestInFlightDeadlinePropagates(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := New(waitingExecer{}).Publish(ctx, event()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Publish: %v", err)
	}
}
func TestConcurrentPublish(t *testing.T) {
	db := &recordingExecer{}
	p := New(db)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := p.Publish(context.Background(), event()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if len(db.calls) != 20 {
		t.Fatalf("calls=%d", len(db.calls))
	}
}
func TestMissingTransportRejected(t *testing.T) {
	if err := New(nil).Publish(context.Background(), event()); !errors.Is(err, durableoutbox.ErrInvalidInput) {
		t.Fatalf("Publish: %v", err)
	}
}
