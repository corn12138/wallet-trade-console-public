package marketstream

import (
	"context"
	"sync"
)

// MemoryDriver is the in-process Driver (the Go port of
// market-stream.memory-driver.ts): a snapshot map plus an in-memory fan-out to
// subscribers. Used when no Redis is configured, and as the test double for the
// worker. Safe for concurrent use.
type MemoryDriver struct {
	mu          sync.RWMutex
	cache       map[string]*Snapshot
	subscribers map[int]func(*Snapshot)
	nextID      int
}

// NewMemoryDriver builds an empty in-process driver.
func NewMemoryDriver() *MemoryDriver {
	return &MemoryDriver{
		cache:       map[string]*Snapshot{},
		subscribers: map[int]func(*Snapshot){},
	}
}

func (d *MemoryDriver) Connect(context.Context) error    { return nil }
func (d *MemoryDriver) Disconnect(context.Context) error { return nil }

func (d *MemoryDriver) GetSnapshot(_ context.Context, symbol string, chainID int) (*Snapshot, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.cache[cacheKey(symbol, chainID)], nil
}

// PrimeSnapshot stores a snapshot without notifying subscribers.
func (d *MemoryDriver) PrimeSnapshot(_ context.Context, snap *Snapshot) error {
	if snap == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cache[cacheKey(snap.Symbol, snap.ChainID)] = snap
	return nil
}

// PublishSnapshot stores then fans the snapshot out to all subscribers. The
// listener slice is copied under lock so a listener can (un)subscribe without
// deadlocking or racing.
func (d *MemoryDriver) PublishSnapshot(_ context.Context, snap *Snapshot) error {
	if snap == nil {
		return nil
	}
	d.mu.Lock()
	d.cache[cacheKey(snap.Symbol, snap.ChainID)] = snap
	subs := make([]func(*Snapshot), 0, len(d.subscribers))
	for _, s := range d.subscribers {
		subs = append(subs, s)
	}
	d.mu.Unlock()

	for _, s := range subs {
		s(snap)
	}
	return nil
}

func (d *MemoryDriver) Subscribe(listener func(*Snapshot)) (func(), error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	id := d.nextID
	d.nextID++
	d.subscribers[id] = listener
	return func() {
		d.mu.Lock()
		delete(d.subscribers, id)
		d.mu.Unlock()
	}, nil
}
