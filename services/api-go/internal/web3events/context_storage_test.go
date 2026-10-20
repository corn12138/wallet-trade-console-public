package web3events

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestContextStorageErrorUsesExactConstraintAndSQLState(t *testing.T) {
	for _, tc := range []struct {
		code, constraint string
		want             bool
	}{
		{"23514", "web3_tx_ctx_market_check", true},
		{"23514", "web3_tx_ctx_shape_check", true},
		{"23503", "web3_tx_ctx_revision_fkey", true},
		{"23514", "web3_tx_ctx_future_unrelated", false},
		{"23514", "web3_transactions_tx_type_check", false},
		{"23503", "web3_tx_ctx_market_check", false},
		{"23514", "web3_tx_ctx_revision_fkey", false},
		{"22001", "", false},
		{"22001", "web3_tx_ctx_market_check", false},
		{"25P02", "web3_tx_ctx_market_check", false},
		{"08006", "", false},
	} {
		err := fmt.Errorf("upsert: %w", &pgconn.PgError{Code: tc.code, ConstraintName: tc.constraint})
		if got := isContextStorageError(err); got != tc.want {
			t.Errorf("%s/%s = %v", tc.code, tc.constraint, got)
		}
	}
	if isContextStorageError(errors.New("web3_tx_ctx_market_check")) {
		t.Fatal("plain text is not constraint attribution")
	}
}

func TestContextStorageLogNeverIncludesDatabasePayload(t *testing.T) {
	var out bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&out, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	logContextStorageRejected(context.Background(), 1, &pgconn.PgError{
		Code: "23514", ConstraintName: "web3_tx_ctx_market_check",
		Message: "secret-message", Detail: "secret-row", Where: "secret-statement", Hint: "secret-hint",
	})
	if strings.Contains(out.String(), "secret") || !strings.Contains(out.String(), "web3_tx_ctx_market_check") {
		t.Fatalf("unsafe or unattributed log: %s", out.String())
	}
}

func TestContextOutcomeDoesNotInventAnUpgrade(t *testing.T) {
	digest := "claim"
	incoming := &ContextWrite{State: ContextRecorded, ClaimDigest: &digest}
	for _, tc := range []struct {
		name          string
		incoming      *ContextWrite
		state, digest *string
		inserted      bool
		want          string
	}{
		{"new claim", incoming, str(ContextRecorded), &digest, true, "applied"},
		{"absent claim", nil, nil, nil, true, "absent"},
		{"absent claim after recorded winner", nil, str(ContextRecorded), &digest, false, "ignoredExisting"},
		{"final null", incoming, nil, nil, false, "ignoredExisting"},
		{"different claim", incoming, str(ContextRecorded), str("other"), false, "ignoredExisting"},
		{"unresolved same claim", incoming, str(ContextClaimUnverified), &digest, false, "retainedUnverified"},
		{"no locked preimage cannot prove an upgrade", incoming, str(ContextRecorded), &digest, false, "ignoredExisting"},
	} {
		if got := contextOutcome(tc.incoming, tc.state, tc.digest, tc.inserted, contextPreimage{}); got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, got, tc.want)
		}
	}
}
