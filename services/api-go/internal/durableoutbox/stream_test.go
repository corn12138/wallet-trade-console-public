package durableoutbox

import (
	"context"
	"errors"
	"testing"
)

func TestStreamAnchorAndVersionPagesAreIndependentOfPublication(t *testing.T) {
	pool, r := database(t)
	ids := seed(t, pool, "read-stream", 3)
	seed(t, pool, "unrelated", 2)
	exec(t, pool, `UPDATE durable_outbox SET dead_lettered_at=clock_timestamp(),error_code='unsupportedSchema' WHERE id=$1::uuid`, ids[0])
	ctx := context.Background()
	anchor, err := r.ReadStream(ctx, "read-stream", nil, 2)
	if err != nil || anchor.Head != 3 || len(anchor.Events) != 0 || anchor.ObservedAt.IsZero() {
		t.Fatalf("anchor=%+v err=%v", anchor, err)
	}
	after := int64(0)
	page, err := r.ReadStream(ctx, "read-stream", &after, 2)
	if err != nil || page.Head != 3 || len(page.Events) != 2 || page.Events[0].ID != ids[0] || page.Events[1].ID != ids[1] {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	after = page.Events[1].Version
	page, err = r.ReadStream(ctx, "read-stream", &after, 2)
	if err != nil || len(page.Events) != 1 || page.Events[0].ID != ids[2] {
		t.Fatalf("tail=%+v err=%v", page, err)
	}
	if scalar(t, pool, `SELECT count(*) FROM consumer_receipts`) != 0 || scalar(t, pool, `SELECT count(*) FROM durable_outbox WHERE published_at IS NOT NULL OR attempts>0`) != 0 {
		t.Fatal("read acknowledged publication or business consumption")
	}
}

func TestStreamReadPreservesMissingAndFutureCursorEvidence(t *testing.T) {
	pool, r := database(t)
	seed(t, pool, "cursor", 1)
	ctx := context.Background()
	missing, err := r.ReadStream(ctx, "missing", nil, 1)
	if err != nil || missing.Head != 0 || len(missing.Events) != 0 {
		t.Fatalf("missing=%+v err=%v", missing, err)
	}
	after := int64(2)
	future, err := r.ReadStream(ctx, "cursor", &after, 1)
	if err != nil || future.Head != 1 || len(future.Events) != 0 {
		t.Fatalf("future=%+v err=%v", future, err)
	}
	// The domain adapter must see the lower head and require snapshot repair;
	// silently replacing it with the caller's cursor would conceal lost history.
	if _, err = r.ReadStream(ctx, "cursor", &after, 101); !errors.Is(err, ErrInvalidInput) {
		t.Fatal("unbounded page accepted")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = r.ReadStream(canceled, "cursor", nil, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read=%v", err)
	}
}
