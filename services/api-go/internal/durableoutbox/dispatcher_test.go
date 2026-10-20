package durableoutbox

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type publisher struct {
	supports bool
	publish  func(context.Context, Event) error
}

func (p publisher) Supports(Event) bool                        { return p.supports }
func (p publisher) Publish(ctx context.Context, e Event) error { return p.publish(ctx, e) }

func TestUnknownPublishOutcomeRetriesSameEventWithoutRepeatingConsumption(t *testing.T) {
	pool, r := database(t)
	ids := seed(t, pool, "unknown-outcome", 1)
	ctx := context.Background()
	o := options()
	exec(t, pool, `CREATE TABLE outbox_test_effects(event_id UUID NOT NULL)`)
	calls := 0
	p := publisher{supports: true, publish: func(ctx context.Context, e Event) error {
		calls++
		if e.ID != ids[0] {
			return errors.New("retry changed event ID")
		}
		_, err := r.Consume(ctx, "durable-test", e.ID, func(Event) bool { return true }, func(ctx context.Context, tx EffectTx, e Event) error {
			_, err := tx.Exec(ctx, `INSERT INTO outbox_test_effects VALUES($1::uuid)`, e.ID)
			return err
		})
		if err != nil {
			return err
		}
		if calls == 1 {
			return errors.New("secret transport detail")
		}
		return nil
	}}
	result, err := r.RunOnce(ctx, o, p)
	if !errors.Is(err, ErrPublishFailed) || err.Error() != ErrPublishFailed.Error() || result.Retried != 1 {
		t.Fatalf("first pass %+v %v", result, err)
	}
	if len(claim(t, r, o)) != 0 {
		t.Fatal("retry ignored backoff")
	}
	exec(t, pool, `UPDATE durable_outbox SET available_at=clock_timestamp()-interval '1 second' WHERE id=$1::uuid`, ids[0])
	result, err = r.RunOnce(ctx, o, p)
	if err != nil || result.Published != 1 || calls != 2 {
		t.Fatalf("retry %+v %v calls=%d", result, err, calls)
	}
	if scalar(t, pool, `SELECT count(*) FROM outbox_test_effects`) != 1 {
		t.Fatal("unknown publication outcome repeated committed effect")
	}
}

func TestUnsupportedSchemaNeverCallsPublisher(t *testing.T) {
	pool, r := database(t)
	seed(t, pool, "unknown-schema", 2)
	result, err := r.RunOnce(context.Background(), options(), publisher{supports: false, publish: func(context.Context, Event) error { return errors.New("unsupported payload published") }})
	if err != nil || result.DeadLettered != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if len(claim(t, r, options())) != 0 {
		t.Fatal("unsupported schema was skipped")
	}
}

func TestRetryBounds(t *testing.T) {
	o := options()
	for _, attempt := range []int{1, 2, 3, 100} {
		delay := o.retryDelay(attempt)
		if delay < o.RetryBase || delay > o.RetryMax {
			t.Fatal("retry outside bounds")
		}
	}
	if o.retryDelay(1) != time.Second || o.retryDelay(2) != 2*time.Second || o.retryDelay(100) != time.Minute {
		t.Fatal("wrong exponential backoff")
	}
	o.Batch = 101
	if !errors.Is(o.validate(), ErrInvalidInput) {
		t.Fatal("unbounded batch accepted")
	}
}

func TestSlowPublisherDoesNotConsumeAnotherStreamsLease(t *testing.T) {
	pool, r := database(t)
	seed(t, pool, "stream-a", 2)
	seed(t, pool, "stream-b", 1)
	o := options()
	o.Batch = 2
	o.Lease = time.Second
	o.MaxAttempts = 1
	started := make(chan string, 1)
	var calls atomic.Int64
	p := publisher{supports: true, publish: func(ctx context.Context, e Event) error {
		// Whichever row UPDATE RETURNING yields first is slow. The failure
		// control cannot depend on PostgreSQL's unspecified result ordering.
		if calls.Add(1) == 1 {
			<-ctx.Done()
			return ctx.Err()
		}
		started <- e.ID
		return nil
	}}
	result, err := r.RunOnce(context.Background(), o, p)
	if !errors.Is(err, ErrPublishFailed) || result.Published != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	var healthy string
	select {
	case healthy = <-started:
	default:
		t.Fatal("healthy stream never reached publisher")
	}
	claim(t, r, o)
	if scalar(t, pool, `SELECT count(*) FROM durable_outbox WHERE id=$1::uuid AND published_at IS NOT NULL AND dead_lettered_at IS NULL`, healthy) != 1 {
		t.Fatal("slow stream exhausted healthy event")
	}
}
