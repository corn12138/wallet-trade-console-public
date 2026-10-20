package legacyimport

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// RowDigestForTest exposes the row canonicalization to the external integration
// tests that check it against real PostgreSQL jsonb rendering.
var RowDigestForTest = canonicalRow

// SetVerifyHookForTest runs hook while VerifyFenced holds the exclusive fence
// lock, after it read the rows and before it commits.
func SetVerifyHookForTest(hook func()) func() {
	previous := verifyHook
	verifyHook = hook
	return func() { verifyHook = previous }
}

// SetCaptureHookForTest runs hook inside the capture transaction once the run
// row is locked.
func SetCaptureHookForTest(hook func()) func() {
	previous := captureHook
	captureHook = hook
	return func() { captureHook = previous }
}

// CaptureRunForTest runs the capture transaction for an existing run.
func CaptureRunForTest(ctx context.Context, pool *pgxpool.Pool, runID string) (CaptureResult, error) {
	result, hit, err := capture(ctx, pool, runID, DefaultLimits)
	if hit != "" {
		return result, fmt.Errorf("limit %s", hit)
	}
	return result, err
}
