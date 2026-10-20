package web3events

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func captureContextOutcomes(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &logs
}

func requireContextOutcomes(t *testing.T, logs *bytes.Buffer, want map[string]int) {
	t.Helper()
	got := map[string]int{}
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if record["msg"] != "execution context report" {
			continue
		}
		outcome, ok := record["contextOutcome"].(string)
		if !ok {
			t.Errorf("context outcome is unknown: %s", line)
		}
		got[outcome]++
	}
	if len(got) != len(want) {
		t.Fatalf("context outcomes = %v, want %v", got, want)
	}
	for outcome, count := range want {
		if got[outcome] != count {
			t.Errorf("context outcome %s count = %d, want %d", outcome, got[outcome], count)
		}
	}
}

func TestContextOutcomeResolutionThenRetry(t *testing.T) {
	pool, repo := contextDB(t)
	revision := aRevision(t, pool)
	for _, state := range []string{ContextRecorded, ContextClaimMismatch} {
		t.Run(state, func(t *testing.T) {
			chainID, hash := isolatedChain(t, pool), hashOf("c8")
			digest := strings.Repeat("d", 64)
			if err := submitCtx(t, repo, chainID, hash, 8, boundWrite(ContextClaimUnverified, digest, revision, "")); err != nil {
				t.Fatal(err)
			}
			reason := ""
			if state == ContextClaimMismatch {
				reason = "bytesMismatch"
			}
			resolved := boundWrite(state, digest, revision, reason)
			logs := captureContextOutcomes(t)
			if err := submitCtx(t, repo, chainID, hash, 8, resolved); err != nil {
				t.Fatal(err)
			}
			if err := receiptCtx(t, repo, chainID, hash, 8, resolved); err != nil {
				t.Fatal(err)
			}
			requireContextOutcomes(t, logs, map[string]int{"upgraded": 1, "ignoredExisting": 1})
			if got := contextOf(t, pool, chainID, hash); got.state != state || got.digest != digest {
				t.Fatalf("resolved context = %+v", got)
			}
		})
	}
}

func TestContextOutcomeConcurrentResolvers(t *testing.T) {
	pool, repo := contextDB(t)
	chainID, hash := isolatedChain(t, pool), hashOf("c9")
	revision, digest := aRevision(t, pool), strings.Repeat("e", 64)
	if err := submitCtx(t, repo, chainID, hash, 9, boundWrite(ContextClaimUnverified, digest, revision, "")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	first, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Rollback(context.Background())
	logs := captureContextOutcomes(t)
	in := attemptWrite{chainID: chainID, txHash: hash, fromAddress: ownerA, senderNonce: nonceOf(9), contextColumns: true,
		context: boundWrite(ContextRecorded, digest, revision, "")}
	if _, err := recordAttemptTx(ctx, first, in); err != nil {
		t.Fatal(err)
	}
	second, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Release()
	done := runContextOutcomeReport(ctx, second, in)
	waitForReportBlocker(t, pool, second.Conn().PgConn().PID(), first.Conn().PgConn().PID())
	if err := first.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	requireContextOutcomes(t, logs, map[string]int{"upgraded": 1, "ignoredExisting": 1})
}

func runContextOutcomeReport(ctx context.Context, conn *pgxpool.Conn, in attemptWrite) <-chan error {
	done := make(chan error, 1)
	go func() {
		// Completion permits the caller to release the connection; rollback
		// on a refused write must finish before that signal is observable.
		done <- func() error {
			tx, err := conn.Begin(ctx)
			if err != nil {
				return err
			}
			defer tx.Rollback(context.Background())
			_, err = recordAttemptTx(ctx, tx, in)
			if err == nil {
				err = tx.Commit(ctx)
			}
			return err
		}()
	}()
	return done
}

func TestContextOutcomeInitialMissResolution(t *testing.T) {
	pool, _ := contextDB(t)
	chainID, revision := isolatedChain(t, pool), aRevision(t, pool)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	first, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Rollback(context.Background())
	in := attemptWrite{chainID: chainID, txHash: hashOf("ca"), fromAddress: ownerA, senderNonce: nonceOf(10), contextColumns: true,
		context: boundWrite(ContextClaimUnverified, strings.Repeat("f", 64), revision, "")}
	if _, err := recordAttemptTx(ctx, first, in); err != nil {
		t.Fatal(err)
	}
	second, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Release()
	logs := captureContextOutcomes(t)
	in.context = boundWrite(ContextRecorded, strings.Repeat("f", 64), revision, "")
	done := runContextOutcomeReport(ctx, second, in)
	// The first row is invisible, so this wait is on action resolution; the
	// later attempt recheck must supply the preimage after the winner commits.
	waitForReportBlocker(t, pool, second.Conn().PgConn().PID(), first.Conn().PgConn().PID())
	if err := first.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	requireContextOutcomes(t, logs, map[string]int{"upgraded": 1})
	if got := contextOf(t, pool, chainID, in.txHash); got.state != ContextRecorded {
		t.Fatalf("initial-miss resolution = %+v", got)
	}
}
