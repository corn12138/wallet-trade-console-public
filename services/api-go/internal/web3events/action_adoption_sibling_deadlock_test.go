package web3events

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type adoptionSiblingTrace struct{ *adoptionDeadlockTrace }

func (s *adoptionSiblingTrace) TraceQueryStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.EqualFold(strings.TrimSpace(data.SQL), "begin") {
		s.begins.Add(1)
	}
	if strings.Contains(data.SQL, "SET status = $4, replaced_by_hash = $3") && s.armed.CompareAndSwap(false, true) {
		s.beforeAction <- conn.PgConn().PID()
		select {
		case <-s.allowRecheck:
		case <-ctx.Done():
		}
	}
	return ctx
}

func TestLegacyActionAdoptionRecoversFromMinedSiblingDeadlock(t *testing.T) {
	pool := requireMigratedDatabase(t)
	for _, columns := range []bool{false, true} {
		t.Run(fmt.Sprintf("context=%t", columns), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			chainID := isolatedChain(t, pool)
			sibling := adoptionInput(chainID, hashOf("a0"), columns, false)
			if _, err := NewRepository(pool).recordAttempt(ctx, sibling); err != nil {
				t.Fatal(err)
			}
			winner := adoptionInput(chainID, hashOf("b0"), columns, true)
			seedUnboundAttempt(t, pool, chainID, winner.txHash)
			winnerBefore := contextColumnsJSON(t, pool, chainID, winner.txHash)
			siblingBefore := contextColumnsJSON(t, pool, chainID, sibling.txHash)
			trace := &adoptionSiblingTrace{&adoptionDeadlockTrace{
				beforeAction: make(chan uint32, 1), allowRecheck: make(chan struct{})}}
			var once sync.Once
			release := func() { once.Do(func() { close(trace.allowRecheck) }) }
			defer release()
			cfg, err := pgxpool.ParseConfig(os.Getenv("WEB3TX_TEST_DATABASE_URL"))
			if err != nil {
				t.Fatal(err)
			}
			cfg.ConnConfig.Tracer = trace
			cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
				_, err := conn.Exec(ctx, `SET deadlock_timeout='50ms'`)
				return err
			}
			retryPool, err := pgxpool.NewWithConfig(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { cancel(); release(); retryPool.Close() }()
			done := make(chan error, 1)
			go func() {
				_, err := NewRepository(retryPool).recordAttempt(ctx, winner)
				done <- err
			}()
			var victimPID uint32
			select {
			case victimPID = <-trace.beforeAction:
			case <-ctx.Done():
				t.Fatal("receipt did not reach sibling update after adoption")
			}
			// The successful upsert is still private to the transaction that
			// will be aborted; a reader must not see a partial binding.
			var unbound bool
			if err := pool.QueryRow(ctx, `SELECT action_id IS NULL FROM web3_transactions WHERE chain_id=$1 AND tx_hash=$2`, chainID, winner.txHash).Scan(&unbound); err != nil || !unbound {
				t.Fatalf("uncommitted adoption visible: %v", err)
			}
			other, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer other.Rollback(context.Background())
			if _, err := other.Exec(ctx, `SET LOCAL deadlock_timeout='5s'`); err != nil {
				t.Fatal(err)
			}
			otherPID := other.Conn().PgConn().PID()
			otherDone := make(chan error, 1)
			go func() {
				otherDone <- func() error {
					defer other.Rollback(context.Background())
					if _, err := recordAttemptTx(ctx, other, sibling); err != nil {
						return err
					}
					return other.Commit(ctx)
				}()
			}()
			waitForReportBlocker(t, pool, otherPID, victimPID)
			release()
			if err := <-otherDone; err != nil {
				t.Fatalf("sibling reporter failed: %v", err)
			}
			if err := <-done; err != nil {
				t.Fatalf("repository did not recover from the mined-sibling deadlock: %v", err)
			}
			if trace.deadlocks.Load() != 1 || trace.begins.Load() != 2 {
				t.Fatalf("mined-sibling retry proof: deadlocks=%d transactions=%d", trace.deadlocks.Load(), trace.begins.Load())
			}
			action, number, _ := actionOf(t, pool, chainID, winner.txHash)
			siblingAction, siblingNumber, _ := actionOf(t, pool, chainID, sibling.txHash)
			var status, replacedBy, winnerStatus string
			if err := pool.QueryRow(ctx, `SELECT status,replaced_by_hash FROM web3_transactions WHERE chain_id=$1 AND tx_hash=$2`, chainID, sibling.txHash).Scan(&status, &replacedBy); err != nil {
				t.Fatal(err)
			}
			if err := pool.QueryRow(ctx, `SELECT status FROM web3_transactions WHERE chain_id=$1 AND tx_hash=$2`, chainID, winner.txHash).Scan(&winnerStatus); err != nil {
				t.Fatal(err)
			}
			if number != 2 || siblingNumber != 1 || action != siblingAction || status != StatusReplaced || replacedBy != winner.txHash || winnerStatus != StatusConfirmed {
				t.Fatal("mined-sibling recovery changed allocation or replacement history")
			}
			assertAdoptionCount(t, pool, action, 2)
			if contextColumnsJSON(t, pool, chainID, winner.txHash) != winnerBefore || contextColumnsJSON(t, pool, chainID, sibling.txHash) != siblingBefore {
				t.Fatal("mined-sibling retry changed either context")
			}
		})
	}
}
