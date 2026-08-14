package marketstream

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/redis/go-redis/v9"
)

// DefaultChannelPrefix mirrors marketStream.channelPrefix default ("market-stream").
const DefaultChannelPrefix = "market-stream"

// RedisDriver is the cross-process Driver (Go port of
// market-stream.redis-driver.ts): snapshots are cached in Redis keys and fanned
// out on the `{prefix}:snapshot` channel, so a worker process and the gateway
// process(es) share state. Key + channel formats match the NestJS driver so the
// two can coexist during migration.
type RedisDriver struct {
	cmd           *redis.Client
	sub           *redis.Client
	channelPrefix string
	channel       string

	mu          sync.Mutex
	pubsub      *redis.PubSub
	cancel      context.CancelFunc
	subscribers map[int]func(*Snapshot)
	nextID      int
}

// NewRedisDriver builds a driver from a redis URL (redis://[:pass@]host:port[/db]).
// channelPrefix defaults to "market-stream" when empty.
func NewRedisDriver(redisURL, channelPrefix string) (*RedisDriver, error) {
	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("marketstream: parse redis url: %w", err)
	}
	if channelPrefix == "" {
		channelPrefix = DefaultChannelPrefix
	}
	return &RedisDriver{
		cmd:           redis.NewClient(opt),
		sub:           redis.NewClient(opt),
		channelPrefix: channelPrefix,
		channel:       channelPrefix + ":snapshot",
		subscribers:   map[int]func(*Snapshot){},
	}, nil
}

// snapshotKey mirrors buildSnapshotKey in market-stream.redis-driver.ts:
// {prefix}:snapshot:{SYMBOL}:{chainId|all}.
func (d *RedisDriver) snapshotKey(symbol string, chainID int) string {
	chain := "all"
	if chainID != 0 {
		chain = strconv.Itoa(chainID)
	}
	return d.channelPrefix + ":snapshot:" + strings.ToUpper(strings.TrimSpace(symbol)) + ":" + chain
}

func (d *RedisDriver) Connect(ctx context.Context) error {
	if err := d.cmd.Ping(ctx).Err(); err != nil {
		return err
	}
	return d.sub.Ping(ctx).Err()
}

func (d *RedisDriver) Disconnect(ctx context.Context) error {
	d.mu.Lock()
	if d.cancel != nil {
		d.cancel()
		d.cancel = nil
	}
	if d.pubsub != nil {
		_ = d.pubsub.Close()
		d.pubsub = nil
	}
	d.mu.Unlock()
	_ = d.cmd.Close()
	return d.sub.Close()
}

func (d *RedisDriver) GetSnapshot(ctx context.Context, symbol string, chainID int) (*Snapshot, error) {
	val, err := d.cmd.Get(ctx, d.snapshotKey(symbol, chainID)).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var snap Snapshot
	if err := json.Unmarshal([]byte(val), &snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

// PrimeSnapshot caches the snapshot (SET, no TTL) without publishing.
func (d *RedisDriver) PrimeSnapshot(ctx context.Context, snap *Snapshot) error {
	if snap == nil {
		return nil
	}
	b, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	return d.cmd.Set(ctx, d.snapshotKey(snap.Symbol, snap.ChainID), b, 0).Err()
}

// PublishSnapshot caches then publishes the snapshot to the channel.
func (d *RedisDriver) PublishSnapshot(ctx context.Context, snap *Snapshot) error {
	if snap == nil {
		return nil
	}
	if err := d.PrimeSnapshot(ctx, snap); err != nil {
		return err
	}
	b, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	return d.cmd.Publish(ctx, d.channel, b).Err()
}

// Subscribe registers a listener; the first subscriber starts a single shared
// Redis SUBSCRIBE whose messages fan out to all listeners.
func (d *RedisDriver) Subscribe(listener func(*Snapshot)) (func(), error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	id := d.nextID
	d.nextID++
	d.subscribers[id] = listener

	if d.pubsub == nil {
		ctx, cancel := context.WithCancel(context.Background())
		d.cancel = cancel
		d.pubsub = d.sub.Subscribe(ctx, d.channel)
		go d.fanout(ctx, d.pubsub)
	}

	return func() {
		d.mu.Lock()
		delete(d.subscribers, id)
		d.mu.Unlock()
	}, nil
}

func (d *RedisDriver) fanout(ctx context.Context, ps *redis.PubSub) {
	ch := ps.Channel()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			var snap Snapshot
			if err := json.Unmarshal([]byte(msg.Payload), &snap); err != nil {
				continue
			}
			d.mu.Lock()
			subs := make([]func(*Snapshot), 0, len(d.subscribers))
			for _, s := range d.subscribers {
				subs = append(subs, s)
			}
			d.mu.Unlock()
			for _, s := range subs {
				s(&snap)
			}
		}
	}
}
