package web3events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

type constraintProofClassifier struct{ boundState, boundReason, exitState, exitReason string }

func (c constraintProofClassifier) ClassifyBound(context.Context, int64, int, string, string, string, string, string, string, string, bool) (string, string, error) {
	if c.boundState == ContextClaimUnverified {
		return "", "", errors.New("synthetic registry outage")
	}
	return c.boundState, c.boundReason, nil
}

func (c constraintProofClassifier) ClassifyUnboundExit(string, bool) (string, string) {
	return c.exitState, c.exitReason
}

func contextConstraintCorpus(t *testing.T, revision int64) []json.RawMessage {
	t.Helper()
	bound := claimJSON(t, boundClaim(fmt.Sprint(revision), "ETH-USD", "web-trade"))
	corpus := []json.RawMessage{nil, []byte("null"), bound, []byte(`{"mode":"bound","mode":"bound"}`), []byte("[1]"), []byte("{")}
	for _, reason := range []string{"authorityUnavailable", "noActiveRevision", "targetNotActive", "consumerIncompatible"} {
		corpus = append(corpus, claimJSON(t, map[string]any{"mode": "unboundExit", "operation": "trade.closePosition", "market": "ETH-USD", "reason": reason,
			"consumer": map[string]any{"id": "web-trade", "manifestDigest": strings.Repeat("b", 64)}}))
	}
	values := []any{nil, false, 1, "", "0", "-1", "01", "9223372036854775808", strings.Repeat("A", 34), strings.Repeat("x", 2049), "not-a-known-value"}
	for _, field := range []string{"mode", "registryRevision", "operation", "market", "bindingDigest", "consumer", "reason", "unknownField"} {
		for _, value := range values {
			var doc map[string]any
			if err := json.Unmarshal(bound, &doc); err != nil {
				t.Fatal(err)
			}
			doc[field] = value
			corpus = append(corpus, claimJSON(t, doc))
		}
	}
	for _, field := range []string{"id", "manifestDigest", "unknownField"} {
		for _, value := range values {
			doc := boundClaim(fmt.Sprint(revision), "ETH-USD", "web-trade")
			doc["consumer"].(map[string]any)[field] = value
			corpus = append(corpus, claimJSON(t, doc))
		}
	}
	rng := rand.New(rand.NewSource(49207))
	alphabet := []byte("{}[],:\"nulltrue0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ- ")
	for i := 0; i < 1000; i++ {
		raw := make([]byte, rng.Intn(2300))
		for j := range raw {
			raw[j] = alphabet[rng.Intn(len(alphabet))]
		}
		corpus = append(corpus, raw)
	}
	return corpus
}

func TestNormalizerClassificationSatisfiesEveryRealContextConstraint(t *testing.T) {
	pool, _ := contextDB(t)
	revision := aRevision(t, pool)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// PostgreSQL forbids temporary-to-permanent foreign keys. The ordinary
	// proof table and every inserted row disappear with this transaction's rollback.
	if _, err := tx.Exec(ctx, `CREATE TABLE context_constraint_proof AS SELECT `+contextColumnList+` FROM web3_transactions WHERE false`); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query(ctx, `SELECT conname, pg_get_constraintdef(oid) FROM pg_constraint WHERE conrelid='web3_transactions'::regclass AND conname LIKE 'web3_tx_ctx_%' ORDER BY conname`)
	if err != nil {
		t.Fatal(err)
	}
	constraints := map[string]string{}
	for rows.Next() {
		var name, definition string
		if err := rows.Scan(&name, &definition); err != nil {
			t.Fatal(err)
		}
		constraints[name] = definition
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	for _, suffix := range []string{"state_check", "mode_check", "claim_digest_check", "claimed_revision_check", "operation_check", "market_check", "binding_check", "consumer_check", "manifest_check", "reason_check", "diagnostic_check", "shape_check", "revision_fkey"} {
		name := "web3_tx_ctx_" + suffix
		definition, found := constraints[name]
		if !found {
			t.Fatalf("missing migration-owned constraint %s", name)
		}
		if _, err := tx.Exec(ctx, "ALTER TABLE context_constraint_proof ADD CONSTRAINT "+pgx.Identifier{name}.Sanitize()+" "+definition); err != nil {
			t.Fatal(err)
		}
	}
	if len(constraints) != 13 {
		t.Fatalf("update proof for new constraints: got %d", len(constraints))
	}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	defer slog.SetDefault(old)
	classifiers := []constraintProofClassifier{
		{ContextRecorded, "", ContextUnboundExit, ""},
		{ContextClaimUnverified, "", ContextClaimMismatch, "evidenceIncomplete"},
		{ContextClaimMismatch, "unknownRevision", ContextClaimMismatch, "notAnExit"},
	}
	for _, reason := range []string{"unknownBinding", "chainMismatch", "targetMismatch", "bytesMismatch", "evidenceIncomplete"} {
		classifiers = append(classifiers, constraintProofClassifier{ContextClaimMismatch, reason, ContextUnboundExit, ""})
	}
	batch, seen := &pgx.Batch{}, map[string]bool{}
	for _, raw := range contextConstraintCorpus(t, revision) {
		for _, classifier := range classifiers {
			write := NewService(nil).WithContextClassifier(classifier).classifyContext(ctx, raw, 11155111, nil, nil)
			key := "notRecorded"
			if write != nil {
				key = write.State
				if write.Mode != nil {
					seen["mode:"+*write.Mode] = true
				}
				if write.Reason != nil {
					seen["reason:"+*write.Reason] = true
				}
			}
			seen[key] = true
			batch.Queue(`INSERT INTO context_constraint_proof (`+contextColumnList+`) VALUES (`+contextValues(1)+`)`, contextArgs(write)...)
		}
	}
	results := tx.SendBatch(ctx, batch)
	defer results.Close()
	for i := 0; i < batch.Len(); i++ {
		if _, err := results.Exec(); err != nil {
			t.Fatalf("normalized/classified corpus row %d violates a real constraint: %v", i, err)
		}
	}
	if err := results.Close(); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"notRecorded", ContextRecorded, ContextClaimUnverified, ContextClaimMismatch, ContextUnboundExit, "mode:bound", "mode:unboundExit", "reason:malformed", "reason:unknownRevision", "reason:unknownBinding", "reason:chainMismatch", "reason:targetMismatch", "reason:bytesMismatch", "reason:evidenceIncomplete", "reason:notAnExit", "reason:authorityUnavailable", "reason:noActiveRevision", "reason:targetNotActive", "reason:consumerIncompatible"} {
		if !seen[key] {
			t.Errorf("unexercised shape or vocabulary: %s", key)
		}
	}
	t.Logf("%d normalized/classified corpus rows satisfy all 13 actual migration constraints without report fallback", batch.Len())
}
