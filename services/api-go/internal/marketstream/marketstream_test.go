package marketstream

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCacheKey(t *testing.T) {
	if got := cacheKey("eth-usd", 11155111); got != "ETH-USD:11155111" {
		t.Errorf("got %q", got)
	}
	if got := cacheKey("  btc  ", 0); got != "BTC:all" {
		t.Errorf("chain 0 -> all: got %q", got)
	}
}

func TestMemoryDriverPrimeGet(t *testing.T) {
	d := NewMemoryDriver()
	ctx := context.Background()
	_ = d.PrimeSnapshot(ctx, &Snapshot{Symbol: "ETH-USD", ChainID: 1, UpdatedAt: "t1"})

	got, _ := d.GetSnapshot(ctx, "eth-usd", 1) // case-insensitive key
	if got == nil || got.UpdatedAt != "t1" {
		t.Fatalf("prime/get failed: %+v", got)
	}
	miss, _ := d.GetSnapshot(ctx, "ETH-USD", 999)
	if miss != nil {
		t.Errorf("wrong chain should miss")
	}
}

func TestMemoryDriverPublishSubscribe(t *testing.T) {
	d := NewMemoryDriver()
	ctx := context.Background()
	got := make(chan *Snapshot, 1)
	unsub, _ := d.Subscribe(func(s *Snapshot) { got <- s })

	_ = d.PublishSnapshot(ctx, &Snapshot{Symbol: "ETH-USD", ChainID: 1, UpdatedAt: "pub"})
	select {
	case s := <-got:
		if s.UpdatedAt != "pub" {
			t.Errorf("subscriber got wrong snapshot: %+v", s)
		}
	case <-time.After(time.Second):
		t.Fatal("subscriber did not receive publish")
	}
	// published snapshot is also cached
	if c, _ := d.GetSnapshot(ctx, "ETH-USD", 1); c == nil {
		t.Errorf("publish should also cache")
	}

	// unsubscribe stops delivery
	unsub()
	_ = d.PublishSnapshot(ctx, &Snapshot{Symbol: "ETH-USD", ChainID: 1, UpdatedAt: "pub2"})
	select {
	case <-got:
		t.Errorf("unsubscribed listener should not receive")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestWorkerPublishesTrackedSnapshots(t *testing.T) {
	d := NewMemoryDriver()
	got := make(chan *Snapshot, 4)
	_, _ = d.Subscribe(func(s *Snapshot) {
		select {
		case got <- s:
		default:
		}
	})

	tracked := func(context.Context) ([]TrackedMarket, error) {
		return []TrackedMarket{{Symbol: "ETH-USD", ChainID: 1}}, nil
	}
	compute := func(_ context.Context, m TrackedMarket) (*Snapshot, error) {
		return &Snapshot{Symbol: m.Symbol, ChainID: m.ChainID, UpdatedAt: "computed"}, nil
	}

	w := NewWorker(d, tracked, compute, 10*time.Millisecond)
	if err := w.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	select {
	case s := <-got:
		if s.Symbol != "ETH-USD" || s.UpdatedAt != "computed" {
			t.Errorf("worker published wrong snapshot: %+v", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not publish")
	}
	if err := w.Stop(context.Background()); err != nil {
		t.Errorf("stop: %v", err)
	}
}

func TestWorkerSkipsComputeErrorsAndNils(t *testing.T) {
	d := NewMemoryDriver()
	published := make(chan *Snapshot, 4)
	_, _ = d.Subscribe(func(s *Snapshot) { published <- s })

	tracked := func(context.Context) ([]TrackedMarket, error) {
		return []TrackedMarket{{Symbol: "ERR"}, {Symbol: "NIL"}, {Symbol: "OK"}}, nil
	}
	compute := func(_ context.Context, m TrackedMarket) (*Snapshot, error) {
		switch m.Symbol {
		case "ERR":
			return nil, errors.New("boom")
		case "NIL":
			return nil, nil
		default:
			return &Snapshot{Symbol: m.Symbol, UpdatedAt: "ok"}, nil
		}
	}
	w := NewWorker(d, tracked, compute, time.Hour) // only the immediate first refresh
	_ = w.Start(context.Background())
	defer w.Stop(context.Background())

	select {
	case s := <-published:
		if s.Symbol != "OK" {
			t.Errorf("only OK should publish, got %q", s.Symbol)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("OK snapshot not published")
	}
	// no second publish (ERR/NIL skipped)
	select {
	case s := <-published:
		t.Errorf("unexpected extra publish: %+v", s)
	case <-time.After(100 * time.Millisecond):
	}
}
