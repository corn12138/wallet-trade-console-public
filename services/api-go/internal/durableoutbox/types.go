// Package durableoutbox owns delivery state and consumer receipts, never the
// immutable business events written by domain transactions.
package durableoutbox

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrInvalidInput = errors.New("outbox: invalid input")
	ErrLeaseLost    = errors.New("outbox: lease lost")
	ErrSequenceGap  = errors.New("outbox: earlier stream event not consumed")
	ErrUnsupported  = errors.New("outbox: unsupported event schema")
)

type Event struct {
	ID             string
	Source         string
	Stream         string
	Version        int64
	Topic          string
	OwnerScope     *string
	PayloadVersion int
	Payload        json.RawMessage
	OccurredAt     time.Time
}

type Delivery struct {
	Event
	Token    string
	Attempts int
}

// Publisher must reject unknown topic/schema combinations. Success confirms
// transport acceptance only; downstream durable effects use consumer receipts.
// Implementations must be concurrency-safe and honor context cancellation.
type Publisher interface {
	Supports(Event) bool
	Publish(context.Context, Event) error
}

type Options struct {
	Source      string
	Batch       int
	Lease       time.Duration
	MaxAttempts int
	RetryBase   time.Duration
	RetryMax    time.Duration
}

func (o Options) validate() error {
	if (o.Source != "campaign" && o.Source != "chain") || o.Batch < 1 || o.Batch > 100 || o.Lease < time.Second || o.Lease > 5*time.Minute ||
		o.MaxAttempts < 1 || o.MaxAttempts > 100 || o.RetryBase < time.Second || o.RetryMax < o.RetryBase || o.RetryMax > 24*time.Hour {
		return ErrInvalidInput
	}
	return nil
}

func (o Options) retryDelay(attempt int) time.Duration {
	delay := o.RetryBase
	for n := 1; n < attempt && delay < o.RetryMax; n++ {
		if delay > o.RetryMax/2 {
			return o.RetryMax
		}
		delay *= 2
	}
	if delay > o.RetryMax {
		return o.RetryMax
	}
	return delay
}
