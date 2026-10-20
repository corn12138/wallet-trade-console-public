package durableoutbox

import (
	"context"
	"errors"
	"time"
)

var ErrPublishFailed = errors.New("outbox: publisher did not confirm acceptance")

type Result struct{ Claimed, Published, Retried, DeadLettered int }

// RunOnce performs a bounded pass. A caller owns scheduling and shutdown; no
// process-global goroutine or transport is activated by importing this package.
func (r *Repository) RunOnce(ctx context.Context, o Options, p Publisher) (Result, error) {
	var result Result
	if p == nil {
		return result, ErrInvalidInput
	}
	deliveries, err := r.Claim(ctx, o)
	if err != nil {
		return result, err
	}
	result.Claimed = len(deliveries)
	type outcome struct {
		result Result
		err    error
	}
	completed := make(chan outcome, len(deliveries))
	// All claimed stream heads begin independently. Serial publication would
	// spend healthy streams' leases waiting for an unrelated slow publisher.
	for _, delivery := range deliveries {
		go func(d Delivery) { result, err := r.publishOne(ctx, o, p, d); completed <- outcome{result, err} }(delivery)
	}
	var failures []error
	for range deliveries {
		item := <-completed
		result.Published += item.result.Published
		result.Retried += item.result.Retried
		result.DeadLettered += item.result.DeadLettered
		if item.err != nil {
			failures = append(failures, item.err)
		}
	}
	return result, errors.Join(failures...)
}

func (r *Repository) publishOne(ctx context.Context, o Options, p Publisher, d Delivery) (Result, error) {
	var result Result
	// Database time owns expiry. The token fences the later acknowledgement
	// even if transport acceptance races a timeout or another worker's reclaim.
	var remaining float64
	err := r.pool.QueryRow(ctx, `SELECT EXTRACT(EPOCH FROM lease_until-clock_timestamp())::double precision FROM durable_outbox
  WHERE id=$1::uuid AND lease_owner=$2::uuid AND published_at IS NULL AND dead_lettered_at IS NULL`, d.ID, d.Token).Scan(&remaining)
	if err != nil {
		return result, err
	}
	if remaining <= 0 {
		return result, ErrLeaseLost
	}
	if !p.Supports(d.Event) {
		if err = r.Reject(ctx, d, o, true); err != nil {
			return result, err
		}
		result.DeadLettered = 1
		return result, nil
	}
	publishCtx, cancel := context.WithTimeout(ctx, time.Duration(remaining*float64(time.Second)))
	publishErr := p.Publish(publishCtx, d.Event)
	cancel()
	if publishErr == nil {
		if err = r.Ack(ctx, d); err != nil {
			return result, err
		}
		result.Published = 1
		return result, nil
	}
	// The transport may have accepted an event despite a timeout. Retrying
	// keeps its ID, so a durable consumer can deduplicate the unknown outcome.
	if err = r.Reject(ctx, d, o, false); err != nil {
		return result, errors.Join(ErrPublishFailed, err)
	}
	if d.Attempts >= o.MaxAttempts {
		result.DeadLettered = 1
	} else {
		result.Retried = 1
	}
	return result, ErrPublishFailed
}
