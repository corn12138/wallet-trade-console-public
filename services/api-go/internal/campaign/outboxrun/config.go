// Package outboxrun composes the Campaign delivery worker as an explicitly
// enabled process with its own database pool, isolated from API request pools.
package outboxrun

import (
	"errors"
	"strconv"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/durableoutbox"
)

var (
	ErrConfig = errors.New("campaign outbox: invalid configuration")
	ErrStore  = errors.New("campaign outbox: database unavailable or schema incomplete")
	ErrRole   = errors.New("campaign outbox: delivery role privileges invalid")
)

type Config struct {
	Enabled  bool
	Delivery durableoutbox.Options
	Worker   durableoutbox.WorkerOptions
}

func Load(getenv func(string) string) (Config, error) {
	c := Config{}
	switch getenv("CAMPAIGN_OUTBOX_ENABLED") {
	case "", "false":
		return c, nil
	case "true":
		c.Enabled = true
	default:
		return c, ErrConfig
	}
	batch, err := bounded(getenv("CAMPAIGN_OUTBOX_BATCH"), 4, 1, 4)
	if err != nil {
		return c, err
	}
	poll, err := bounded(getenv("CAMPAIGN_OUTBOX_POLL_MS"), 1000, 100, 60000)
	if err != nil {
		return c, err
	}
	c.Delivery = durableoutbox.Options{Source: "campaign", Batch: batch, Lease: 30 * time.Second, MaxAttempts: 10, RetryBase: time.Second, RetryMax: 5 * time.Minute}
	c.Worker = durableoutbox.WorkerOptions{PollInterval: time.Duration(poll) * time.Millisecond, PassTimeout: 10 * time.Second}
	return c, nil
}

func bounded(raw string, fallback, min, max int) (int, error) {
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || strconv.Itoa(n) != raw || n < min || n > max {
		return 0, ErrConfig
	}
	return n, nil
}
