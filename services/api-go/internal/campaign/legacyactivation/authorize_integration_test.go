//go:build integration

package legacyactivation_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/legacyactivation"
	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
)

// The engine checks the caller again inside its transaction, before it writes
// or replays anything (protocol §5): a refusal there leaves no row behind and
// does not reveal a recorded result.
func TestEngineReauthorizesBeforeWritingOrReplaying(t *testing.T) {
	f := seed(t)
	ctx := context.Background()
	refuse := func(context.Context) error {
		return &v2.DomainError{Code: "CAMPAIGN_ADMIN_REQUIRED", Status: http.StatusForbidden}
	}
	allow := func(context.Context) error { return nil }

	review := legacyactivation.ReviewInput{RunID: f.run, CampaignID: f.mixed, Decision: "import-public", AcknowledgedFindings: []string{"PARTICIPANT_WALLET_CASE_LOOKUP"},
		AcceptedDifferences: []string{"walletState"}, Reason: "checksum wallet becomes visible", Reviewer: admin, ClientCommandID: commandID(t), Authorize: refuse}
	if _, err := f.engine.RecordReview(ctx, review); code(err) != "CAMPAIGN_ADMIN_REQUIRED" {
		t.Errorf("a refused reviewer must not record a decision: %v", err)
	}
	if n := f.count(t, `SELECT count(*) FROM campaign_legacy_review_decisions WHERE run_id=$1 AND campaign_id=$2`, f.run, f.mixed); n != 0 {
		t.Errorf("a refused review wrote %d decisions", n)
	}
	review.Authorize = allow
	if _, err := f.engine.RecordReview(ctx, review); err != nil {
		t.Fatal(err)
	}
	review.Authorize = refuse
	if replayed, err := f.engine.RecordReview(ctx, review); code(err) != "CAMPAIGN_ADMIN_REQUIRED" || replayed.ID != "" {
		t.Errorf("a refused reviewer must not see the recorded decision: %+v %v", replayed, err)
	}

	input := f.input(t, f.public)
	input.Authorize = refuse
	if _, err := f.engine.Activate(ctx, input); code(err) != "CAMPAIGN_ADMIN_REQUIRED" {
		t.Errorf("a refused administrator must not activate: %v", err)
	}
	if n := f.count(t, `SELECT count(*) FROM campaign_legacy_activations WHERE campaign_id=$1`, f.public) + f.count(t, `SELECT count(*) FROM campaign_definitions WHERE id=$1`, f.public) +
		f.count(t, `SELECT count(*) FROM campaign_commands WHERE target_id=$1`, f.public); n != 0 {
		t.Errorf("a refused activation wrote %d rows", n)
	}
	input.Authorize = allow
	if _, err := f.engine.Activate(ctx, input); err != nil {
		t.Fatal(err)
	}
	input.Authorize = refuse
	if replayed, err := f.engine.Activate(ctx, input); code(err) != "CAMPAIGN_ADMIN_REQUIRED" || replayed.ID != "" {
		t.Errorf("a refused administrator must not see the recorded activation: %+v %v", replayed, err)
	}

	// The request ID is whatever the HTTP layer forwards: 1-96 characters
	// without control characters, such as a proxy's host-qualified ID.
	for _, id := range []string{"", strings.Repeat("r", 97), "line\nbreak", "bad\xffutf8"} {
		in := f.input(t, f.draft)
		in.RequestID = id
		if _, err := f.engine.Activate(ctx, in); code(err) != "CAMPAIGN_LEGACY_INVALID_INPUT" {
			t.Errorf("request ID %q: %v", id, err)
		}
	}
	in := f.input(t, f.draft)
	in.RequestID = "api-7f9c.local/AbCdEf0123-000042"
	if _, err := f.engine.Activate(ctx, in); code(err) != "CAMPAIGN_LEGACY_REVIEW_REQUIRED" {
		t.Errorf("a proxy request ID is accepted and the draft still needs its review: %v", err)
	}
}
