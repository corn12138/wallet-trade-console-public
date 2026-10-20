package web3events

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type adoptionTraceKey struct{}

type adoptionDeadlockTrace struct {
	armed                     atomic.Bool
	begins, deadlocks         atomic.Int32
	beforeAction, afterAction chan uint32
	allowAction, allowRecheck chan struct{}
}

func (s *adoptionDeadlockTrace) TraceQueryStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.EqualFold(strings.TrimSpace(data.SQL), "begin") {
		s.begins.Add(1)
	}
	if strings.Contains(data.SQL, "SELECT id, request_fingerprint") && s.armed.CompareAndSwap(false, true) {
		s.beforeAction <- conn.PgConn().PID()
		select {
		case <-s.allowAction:
		case <-ctx.Done():
		}
		return context.WithValue(ctx, adoptionTraceKey{}, true)
	}
	return ctx
}

func (s *adoptionDeadlockTrace) TraceQueryEnd(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryEndData) {
	var pgErr *pgconn.PgError
	if errors.As(data.Err, &pgErr) && pgErr.Code == "40P01" {
		s.deadlocks.Add(1)
	}
	if ctx.Value(adoptionTraceKey{}) == true {
		s.afterAction <- conn.PgConn().PID()
		select {
		case <-s.allowRecheck:
		case <-ctx.Done():
		}
	}
}

func TestLegacyActionAdoptionRecoversFromActualDeadlock(t *testing.T) {
	pool := requireMigratedDatabase(t)
	for _, columns := range []bool{false, true} {
		t.Run(fmt.Sprintf("context=%t", columns), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			chainID := isolatedChain(t, pool)
			in := adoptionInput(chainID, hashOf("a7"), columns, false)
			if _, err := NewRepository(pool).recordAttempt(ctx, in); err != nil {
				t.Fatal(err)
			}
			trace := &adoptionDeadlockTrace{beforeAction: make(chan uint32, 1), afterAction: make(chan uint32, 1),
				allowAction: make(chan struct{}), allowRecheck: make(chan struct{})}
			var actionOnce, recheckOnce sync.Once
			allowAction := func() { actionOnce.Do(func() { close(trace.allowAction) }) }
			allowRecheck := func() { recheckOnce.Do(func() { close(trace.allowRecheck) }) }
			defer allowAction()
			defer allowRecheck()
			cfg, err := pgxpool.ParseConfig(os.Getenv("WEB3TX_TEST_DATABASE_URL"))
			if err != nil {
				t.Fatal(err)
			}
			cfg.ConnConfig.Tracer = trace
			cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
				// Choose the repository transaction as the detector/victim;
				// its real 40P01 must be observed before the successful retry.
				_, err := conn.Exec(ctx, `SET deadlock_timeout='50ms'`)
				return err
			}
			retryPool, err := pgxpool.NewWithConfig(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer retryPool.Close()
			in.txHash = hashOf("b7")
			done := make(chan error, 1)
			go func() {
				_, err := NewRepository(retryPool).recordAttempt(ctx, in)
				done <- err
			}()
			waitPID := func(ch <-chan uint32) uint32 {
				t.Helper()
				select {
				case pid := <-ch:
					return pid
				case <-ctx.Done():
					t.Fatal("repository did not reach the scheduled database lock")
					return 0
				}
			}
			waitPID(trace.beforeAction)
			seedUnboundAttempt(t, pool, chainID, in.txHash)
			before := contextColumnsJSON(t, pool, chainID, in.txHash)
			allowAction()
			victimPID := waitPID(trace.afterAction)
			adopter, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer adopter.Rollback(context.Background())
			if _, err := adopter.Exec(ctx, `SET LOCAL deadlock_timeout='5s'`); err != nil {
				t.Fatal(err)
			}
			if _, err := lockAttempt(ctx, adopter, chainID, in.txHash, columns); err != nil {
				t.Fatal(err)
			}
			adopted := make(chan error, 1)
			go func() {
				_, err := recordAttemptTx(ctx, adopter, in)
				if err == nil {
					err = adopter.Commit(ctx)
				}
				adopted <- err
			}()
			waitForReportBlocker(t, pool, adopter.Conn().PgConn().PID(), victimPID)
			allowRecheck()
			if err := <-adopted; err != nil {
				t.Fatalf("adopting winner failed: %v", err)
			}
			if err := <-done; err != nil {
				t.Fatalf("repository did not recover from the deadlock: %v", err)
			}
			if trace.deadlocks.Load() != 1 || trace.begins.Load() != 2 {
				t.Fatalf("actual retry proof: deadlocks=%d transactions=%d", trace.deadlocks.Load(), trace.begins.Load())
			}
			action, number, _ := actionOf(t, pool, chainID, in.txHash)
			if number != 2 {
				t.Fatalf("deadlock recovery allocated ordinal=%d want=2", number)
			}
			assertAdoptionCount(t, pool, action, 2)
			if contextColumnsJSON(t, pool, chainID, in.txHash) != before {
				t.Fatal("deadlock recovery changed historical null context")
			}
		})
	}
}
