//go:build integration

package legacyactivation_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/legacyactivation"
)

// Two copies of one review command sent concurrently record one decision: the
// later copy waits for the item and replays it. A different command for the
// same item is refused. Neither ever meets its own insert.
func TestConcurrentReviewCopiesReplay(t *testing.T) {
	f := seed(t)
	ctx := context.Background()
	input := legacyactivation.ReviewInput{RunID: f.run, CampaignID: f.mixed, Decision: "import-public", AcknowledgedFindings: []string{"PARTICIPANT_WALLET_CASE_LOOKUP"},
		AcceptedDifferences: []string{"walletState"}, Reason: "concurrent copies", Reviewer: admin, ClientCommandID: commandID(t)}
	type result struct {
		review legacyactivation.Review
		err    error
	}
	copies := make(chan result, 2)
	var paused atomic.Bool
	// The first copy pauses between its replay lookup and its insert while the
	// others run as far as they can: they wait for the item, or finish.
	restore := legacyactivation.SetReviewHookForTest(func() {
		if !paused.CompareAndSwap(false, true) {
			return
		}
		other := input
		other.ClientCommandID = commandID(t)
		for _, in := range []legacyactivation.ReviewInput{input, other} {
			go func(in legacyactivation.ReviewInput) {
				review, err := f.engine.RecordReview(ctx, in)
				copies <- result{review, err}
			}(in)
		}
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) && len(copies) < 2 && lockWaiters(t, f, "campaign-legacy-review:"+f.run+":"+f.mixed) < 2 {
			time.Sleep(20 * time.Millisecond)
		}
	})
	first, err := f.engine.RecordReview(ctx, input)
	got := [2]result{<-copies, <-copies}
	// The copies have returned, so the hook can be restored without racing
	// their reads of it.
	restore()
	if err != nil || first.Replayed {
		t.Errorf("the first copy records the decision: %+v %v", first, err)
	}
	outcomes := map[string]int{}
	for _, got := range got {
		switch {
		case got.err == nil && got.review.Replayed && got.review.ID == first.ID && got.review.ClientCommandID == input.ClientCommandID:
			outcomes["replayed"]++
		case code(got.err) == "CAMPAIGN_LEGACY_ALREADY_REVIEWED" && got.review.ID == "":
			outcomes["refused"]++
		default:
			t.Errorf("a concurrent review: %+v %v", got.review, got.err)
		}
	}
	if outcomes["replayed"] != 1 || outcomes["refused"] != 1 {
		t.Errorf("the same command replays and another is refused: %v", outcomes)
	}
	if n := f.count(t, `SELECT count(*) FROM campaign_legacy_review_decisions WHERE run_id=$1 AND campaign_id=$2`, f.run, f.mixed); n != 1 {
		t.Errorf("one item holds one decision, found %d", n)
	}
}
