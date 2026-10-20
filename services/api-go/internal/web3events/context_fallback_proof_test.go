package web3events

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/executionregistry"
)

func TestRealContextStorageFallbackLogsOnlyClosedAttribution(t *testing.T) {
	pool, repo := proofReportDatabase(t)
	ctx := context.Background()
	rev, err := executionregistry.NewRepository(pool).Stage(ctx, buildSnapshot(t, "../../../../packages/shared/deployments"), "test", "fallback proof")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE web3_transactions DROP CONSTRAINT web3_tx_ctx_market_check;
		ALTER TABLE web3_transactions ADD CONSTRAINT web3_tx_ctx_market_check CHECK (execution_context_market IS NULL) NOT VALID`); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(old)
	write := boundWrite(ContextRecorded, strings.Repeat("a", 64), rev, "")
	metadata := json.RawMessage(`{"sensitiveProofSentinel":"must-not-enter-context-telemetry"}`)
	if _, err := repo.UpsertSubmittedTransaction(ctx, SubmitTxInput{ChainID: 11155111, TxHash: hashOf("a1"), FromAddress: ownerA,
		SenderNonce: nonceOf(1), Metadata: metadata, Context: write}); err != nil {
		t.Fatal(err)
	}
	if err := receiptCtx(t, repo, 11155111, hashOf("a2"), 2, write); err != nil {
		t.Fatal(err)
	}
	for _, hash := range []string{hashOf("a1"), hashOf("a2")} {
		if got := contextOf(t, pool, 11155111, hash); got.state != "notRecorded" {
			t.Fatalf("context fallback did not persist the attempt: %+v", got)
		}
	}
	count := 0
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		if event["msg"] != "contextStorageRejected" {
			continue
		}
		count++
		if event["sqlState"] != "23514" || event["constraint"] != "web3_tx_ctx_market_check" || event["chainId"] != float64(11155111) {
			t.Fatal("storage failure lacked exact closed attribution")
		}
		for key := range event {
			if key != "time" && key != "level" && key != "msg" && key != "chainId" && key != "sqlState" && key != "constraint" {
				t.Errorf("unexpected storage rejection field %q", key)
			}
		}
	}
	if count != 2 || strings.Contains(logs.String(), "sensitiveProofSentinel") || strings.Contains(logs.String(), "must-not-enter") {
		t.Fatalf("expected two safely attributed actual fallbacks; got %d", count)
	}
}
