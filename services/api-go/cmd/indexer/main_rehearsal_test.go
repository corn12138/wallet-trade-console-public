package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/indexer"
)

// TestBuildWorkerRepairsSnapshotDatabase rehearses the fail-closed startup path
// of cmd/indexer against a database that already carries the post-deploy
// schema plus real rows (a production snapshot restored into a disposable
// database). It is opt-in: without INDEXER_REHEARSAL_DATABASE_URL it skips,
// and a skip is not a pass — the deploy runbook runs it with the variable set
// and keeps the PASS line with the repair counts as evidence.
func TestBuildWorkerRepairsSnapshotDatabase(t *testing.T) {
	databaseURL := strings.TrimSpace(os.Getenv("INDEXER_REHEARSAL_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("INDEXER_REHEARSAL_DATABASE_URL not set")
	}
	t.Setenv("DATABASE_URL", databaseURL)

	cfg, err := indexer.Load()
	if err != nil {
		t.Fatalf("indexer.Load: %v", err)
	}
	// Startup repair must not depend on the chain: point the reader at a closed
	// port so any RPC dependency surfaces here instead of in production.
	cfg.RPCURL = "http://127.0.0.1:1"

	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	worker, cleanup, err := buildWorker(ctx, cfg)
	t.Cleanup(cleanup)
	t.Logf("indexer startup log:\n%s", logs.String())
	if err != nil {
		t.Fatalf("buildWorker on snapshot database: %v", err)
	}
	if worker == nil {
		t.Fatal("buildWorker returned a nil worker")
	}
	// buildWorker degrades to a heartbeat-only worker on a bad database env;
	// the rehearsal only means something when the live path actually ran.
	if strings.Contains(logs.String(), "heartbeat only") {
		t.Fatal("buildWorker fell back to heartbeat-only mode; the snapshot database was not exercised")
	}
	if !strings.Contains(logs.String(), "live mode") {
		t.Fatal("buildWorker did not reach live mode")
	}
}
