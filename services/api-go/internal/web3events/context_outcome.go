package web3events

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5"
)

// The locked preimage is diagnostic only; the upsert remains the sole
// authority for insertion and context assignment.
type contextPreimage struct {
	state, digest *string
}

// Context evidence is scanned separately so it cannot leak into the public
// transaction DTO.
type contextResultRow struct {
	pgx.Row
	state, digest *string
	inserted      bool
}

func (r *contextResultRow) Scan(dest ...any) error {
	return r.Row.Scan(append(dest, &r.state, &r.digest, &r.inserted)...)
}

func scanContextTxRow(ctx context.Context, actionID string, incoming *ContextWrite, prior contextPreimage, row pgx.Row) (TransactionRow, error) {
	result := &contextResultRow{Row: row}
	tx, err := scanAttemptTxRow(actionID, result)
	if err != nil {
		return TransactionRow{}, err
	}
	outcome := contextOutcome(incoming, result.state, result.digest, result.inserted, prior)
	// This describes the successful statement, before the enclosing commit;
	// it must not be interpreted as a committed business-event counter.
	slog.InfoContext(ctx, "execution context report", "contextOutcome", outcome)
	return tx, nil
}

func contextOutcome(incoming *ContextWrite, state, digest *string, inserted bool, prior contextPreimage) string {
	if incoming == nil {
		if !inserted {
			return "ignoredExisting"
		}
		return "absent"
	}
	if inserted {
		return "applied"
	}
	sameClaim := incoming.ClaimDigest != nil && digest != nil && *incoming.ClaimDigest == *digest
	if state != nil && *state == ContextClaimUnverified && sameClaim {
		return "retainedUnverified"
	}
	if sameClaim && state != nil && *state == incoming.State &&
		(incoming.State == ContextRecorded || incoming.State == ContextClaimMismatch) &&
		prior.state != nil && *prior.state == ContextClaimUnverified &&
		prior.digest != nil && *prior.digest == *incoming.ClaimDigest {
		return "upgraded"
	}
	return "ignoredExisting"
}
