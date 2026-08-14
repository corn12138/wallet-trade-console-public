package marketstream

import (
	"context"
	"os"
	"testing"
	"time"
)

func redisTestURL() string {
	if v := os.Getenv("TEST_REDIS_URL"); v != "" {
		return v
	}
	return "redis://127.0.0.1:6379/15" // db 15 to isolate from app data
}

func TestRedisDriverSnapshotKey(t *testing.T) {
	d, err := NewRedisDriver(redisTestURL(), "")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if got := d.snapshotKey("eth-usd", 11155111); got != "market-stream:snapshot:ETH-USD:11155111" {
		t.Errorf("got %q", got)
	}
	if got := d.snapshotKey("  btc  ", 0); got != "market-stream:snapshot:BTC:all" {
		t.Errorf("chain 0 -> all: got %q", got)
	}
}

// TestRedisDriverLivePubSub exercises the real cross-process path against a live
// Redis (local on :6379, or TEST_REDIS_URL). Skips when no Redis is reachable so
// CI without Redis stays green.
func TestRedisDriverLivePubSub(t *testing.T) {
	d, err := NewRedisDriver(redisTestURL(), "mstest")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	ctx := context.Background()
	if err := d.Connect(ctx); err != nil {
		t.Skipf("no reachable Redis (%v); skipping live driver test", err)
	}
	defer d.Disconnect(ctx)
	defer d.cmd.Del(context.Background(), d.snapshotKey("ETH-USD", 1))

	got := make(chan *Snapshot, 1)
	unsub, _ := d.Subscribe(func(s *Snapshot) {
		select {
		case got <- s:
		default:
		}
	})
	defer unsub()
	time.Sleep(150 * time.Millisecond) // let SUBSCRIBE establish

	if err := d.PublishSnapshot(ctx, &Snapshot{Symbol: "ETH-USD", ChainID: 1, UpdatedAt: "live"}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	select {
	case s := <-got:
		if s.UpdatedAt != "live" {
			t.Errorf("fan-out delivered wrong snapshot: %+v", s)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no snapshot received via redis pub/sub")
	}

	// cache read-back + miss
	if c, _ := d.GetSnapshot(ctx, "eth-usd", 1); c == nil || c.UpdatedAt != "live" {
		t.Errorf("cache read-back failed: %+v", c)
	}
	if c, _ := d.GetSnapshot(ctx, "NOPE", 1); c != nil {
		t.Errorf("cache miss should be nil, got %+v", c)
	}
}
