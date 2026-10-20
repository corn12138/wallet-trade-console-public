package durableoutbox

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func workerOptions() WorkerOptions {
	return WorkerOptions{PollInterval: 100 * time.Millisecond, PassTimeout: time.Second}
}

func TestWorkerScansOnStartupWithoutNotification(t *testing.T) {
	pool, r := database(t)
	seed(t, pool, "startup", 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	w := workerOptions()
	w.PollInterval = time.Minute
	var published int
	err := r.Run(ctx, options(), w, publisher{supports: true, publish: func(context.Context, Event) error { return nil }}, func(p PassResult) {
		published += p.Published
		if p.Published == 1 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) || published != 1 {
		t.Fatalf("startup did not drain pending event: published=%d err=%v", published, err)
	}
}

func TestWorkerCancellationAndRestartRespectExistingLease(t *testing.T) {
	pool, r := database(t)
	ids := seed(t, pool, "restart", 1)
	o := options()
	o.Lease = time.Second
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- r.Run(ctx, o, workerOptions(), publisher{supports: true, publish: func(ctx context.Context, e Event) error {
			started <- struct{}{}
			<-ctx.Done()
			return ctx.Err()
		}}, nil)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("publisher never started")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker ignored shutdown")
	}
	if scalar(t, pool, `SELECT count(*) FROM durable_outbox WHERE id=$1::uuid AND lease_owner IS NOT NULL AND published_at IS NULL`, ids[0]) != 1 {
		t.Fatal("cancellation lost recoverable claim")
	}
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var published int
	err := r.Run(ctx, o, workerOptions(), publisher{supports: true, publish: func(ctx context.Context, e Event) error {
		if e.ID != ids[0] {
			return errors.New("restart changed event identity")
		}
		return nil
	}}, func(p PassResult) {
		published += p.Published
		if p.Published == 1 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) || published != 1 || scalar(t, pool, `SELECT attempts FROM durable_outbox WHERE id=$1::uuid`, ids[0]) != 2 {
		t.Fatalf("restart failed: published=%d err=%v", published, err)
	}
}

func TestWorkerRecoversAfterDatabasePassTimeout(t *testing.T) {
	pool, r := database(t)
	seed(t, pool, "database-recovery", 1)
	lock, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback(context.Background())
	if _, err = lock.Exec(context.Background(), `LOCK TABLE durable_outbox IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var failed, published int
	var releaseErr error
	err = r.Run(ctx, options(), workerOptions(), publisher{supports: true, publish: func(context.Context, Event) error { return nil }}, func(p PassResult) {
		if p.Failed {
			failed++
			if failed == 1 {
				releaseErr = lock.Rollback(context.Background())
			}
		}
		published += p.Published
		if p.Published == 1 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) || failed != 1 || published != 1 || releaseErr != nil {
		t.Fatalf("recovery: failed=%d published=%d run=%v unlock=%v", failed, published, err, releaseErr)
	}
}

func TestWorkerRetriesPublisherFailureFromDurableState(t *testing.T) {
	pool, r := database(t)
	ids := seed(t, pool, "publisher-recovery", 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var calls atomic.Int64
	var failed, retried, published int
	err := r.Run(ctx, options(), workerOptions(), publisher{supports: true, publish: func(ctx context.Context, e Event) error {
		if e.ID != ids[0] {
			return errors.New("retry changed event identity")
		}
		if calls.Add(1) == 1 {
			return errors.New("private transport diagnostics")
		}
		return nil
	}}, func(p PassResult) {
		if p.Failed {
			failed++
		}
		retried += p.Retried
		published += p.Published
		if p.Published == 1 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) || calls.Load() != 2 || failed != 1 || retried != 1 || published != 1 {
		t.Fatalf("retry: calls=%d failed=%d retried=%d published=%d err=%v", calls.Load(), failed, retried, published, err)
	}
}
