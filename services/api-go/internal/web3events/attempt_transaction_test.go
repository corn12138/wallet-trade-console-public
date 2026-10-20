package web3events

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestAttemptRetryRequiresKnownAbort(t *testing.T) {
	for _, code := range []string{"23505", "40001", "08006", "57014"} {
		t.Run(code, func(t *testing.T) {
			calls := 0
			failure := &pgconn.PgError{Code: code}
			_, err := retryAbortedAttempt(context.Background(), func() (TransactionRow, error) {
				calls++
				return TransactionRow{}, failure
			})
			if calls != 1 || !errors.Is(err, failure) {
				t.Fatalf("non-deadlock error retried: calls=%d err=%v", calls, err)
			}
		})
	}
	for _, failures := range []int{2, 3} {
		t.Run(fmt.Sprintf("deadlocks=%d", failures), func(t *testing.T) {
			calls := 0
			failure := &pgconn.PgError{Code: "40P01"}
			row, err := retryAbortedAttempt(context.Background(), func() (TransactionRow, error) {
				calls++
				if calls <= failures {
					return TransactionRow{}, fmt.Errorf("wrapped database error: %w", failure)
				}
				return TransactionRow{ID: 17}, nil
			})
			if calls != 3 || (failures == 2 && (err != nil || row.ID != 17)) || (failures == 3 && !errors.Is(err, failure)) {
				t.Fatalf("deadlock retry bound/result: calls=%d row=%d err=%v", calls, row.ID, err)
			}
		})
	}
}

func TestAttemptRetryHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	_, err := retryAbortedAttempt(ctx, func() (TransactionRow, error) {
		calls++
		cancel()
		return TransactionRow{}, &pgconn.PgError{Code: "40P01"}
	})
	if calls != 1 || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled attempt retried: calls=%d err=%v", calls, err)
	}
}
