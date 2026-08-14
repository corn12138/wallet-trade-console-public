package indexer

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/rpc"
)

// --- fakes ------------------------------------------------------------

// fakeReader is a ChainReader whose head and per-range log responses are
// scripted by the test. Run is single-goroutine so no locking is needed.
type fakeReader struct {
	head     uint64
	headBig  *big.Int // overrides head verbatim (for negative/overflow cases)
	headErr  error
	getCalls int
	perRange func(from, to uint64) ([]rpc.Log, error)
}

func (f *fakeReader) BlockNumber(context.Context) (*big.Int, error) {
	if f.headErr != nil {
		return nil, f.headErr
	}
	if f.headBig != nil {
		return f.headBig, nil
	}
	return new(big.Int).SetUint64(f.head), nil
}

func (f *fakeReader) GetLogs(_ context.Context, filter rpc.LogFilter) ([]rpc.Log, error) {
	f.getCalls++
	if f.perRange == nil {
		return nil, nil
	}
	return f.perRange(filter.FromBlock, filter.ToBlock)
}

// fakeCheckpoints is an in-memory Checkpointer. Get returns current;
// Save records every block it's handed and advances current.
type fakeCheckpoints struct {
	current uint64
	getErr  error
	saveErr error
	saved   []uint64
}

func (f *fakeCheckpoints) Get(_ context.Context, _ int, _ string, deployBlock uint64) (uint64, error) {
	if f.getErr != nil {
		return 0, f.getErr
	}
	if f.current == 0 {
		return deployBlock, nil
	}
	return f.current, nil
}

func (f *fakeCheckpoints) Save(_ context.Context, _ int, _ string, block uint64, _ int, _ uint64) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	f.saved = append(f.saved, block)
	f.current = block
	return nil
}

type sleepRecorder struct{ delays []time.Duration }

func (s *sleepRecorder) sleep(_ context.Context, d time.Duration) error {
	s.delays = append(s.delays, d)
	return nil
}

type captureSink struct{ events []ParsedEvent }

func (c *captureSink) HandleEvent(_ context.Context, ev ParsedEvent) error {
	c.events = append(c.events, ev)
	return nil
}

type errSink struct{}

func (errSink) HandleEvent(context.Context, ParsedEvent) error {
	return errors.New("persist failed")
}

// transferLog builds a valid Transfer log at the given block. Reuses the
// padAddr / padUint / transferTopic0 helpers from logparser_test.go.
func transferLog(block uint64) rpc.Log {
	from := "0x1111111111111111111111111111111111111111"
	to := "0x2222222222222222222222222222222222222222"
	return rpc.Log{
		Address:     "0xfactory",
		Topics:      []string{transferTopic0, padAddr(from), padAddr(to)},
		Data:        "0x" + padUint(1000),
		BlockNumber: block,
		TxHash:      "0xtx",
		LogIndex:    0,
	}
}

func equalUint64(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --- tests ------------------------------------------------------------

func TestBackfill_MultiBatchSavesCheckpointsAndCountsEvents(t *testing.T) {
	// head=250, batch=100, checkpoint=0 → ranges 1-100, 101-200, 201-250.
	reader := &fakeReader{
		head: 250,
		perRange: func(from, _ uint64) ([]rpc.Log, error) {
			return []rpc.Log{transferLog(from)}, nil // one event per batch
		},
	}
	cp := &fakeCheckpoints{current: 0}
	sink := &CountingSink{}
	cfg := Config{ChainID: 11155111, BatchSize: 100, ConfirmationDepth: 6}

	n, err := NewBackfiller(cfg, reader, cp, sink).Run(context.Background(), "0xFactory")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n != 3 {
		t.Errorf("processed = %d, want 3", n)
	}
	if sink.Count() != 3 {
		t.Errorf("sink count = %d, want 3", sink.Count())
	}
	if want := []uint64{100, 200, 250}; !equalUint64(cp.saved, want) {
		t.Errorf("saved checkpoints = %v, want %v", cp.saved, want)
	}
	if reader.getCalls != 3 {
		t.Errorf("getLogs calls = %d, want 3", reader.getCalls)
	}
}

func TestBackfill_UpToDateDoesNothing(t *testing.T) {
	reader := &fakeReader{
		head: 100,
		perRange: func(_, _ uint64) ([]rpc.Log, error) {
			t.Fatal("getLogs should not be called when already up to date")
			return nil, nil
		},
	}
	cp := &fakeCheckpoints{current: 100}

	n, err := NewBackfiller(Config{BatchSize: 10}, reader, cp, &CountingSink{}).Run(context.Background(), "0xc")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n != 0 {
		t.Errorf("processed = %d, want 0", n)
	}
	if len(cp.saved) != 0 {
		t.Errorf("saved = %v, want none", cp.saved)
	}
}

func TestBackfill_TailBatchClampsToHead(t *testing.T) {
	// head=150, batch=100, checkpoint=0 → ranges 1-100, then 101-150.
	var ranges [][2]uint64
	reader := &fakeReader{
		head: 150,
		perRange: func(from, to uint64) ([]rpc.Log, error) {
			ranges = append(ranges, [2]uint64{from, to})
			return nil, nil
		},
	}
	cp := &fakeCheckpoints{current: 0}

	if _, err := NewBackfiller(Config{BatchSize: 100}, reader, cp, &CountingSink{}).Run(context.Background(), "0xc"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := [][2]uint64{{1, 100}, {101, 150}}
	if len(ranges) != 2 || ranges[0] != want[0] || ranges[1] != want[1] {
		t.Errorf("ranges = %v, want %v", ranges, want)
	}
	if w := []uint64{100, 150}; !equalUint64(cp.saved, w) {
		t.Errorf("saved = %v, want %v", cp.saved, w)
	}
}

func TestBackfill_RetriesThenSucceeds(t *testing.T) {
	var calls int
	reader := &fakeReader{
		head: 50,
		perRange: func(from, _ uint64) ([]rpc.Log, error) {
			calls++
			if calls < 3 {
				return nil, errors.New("temporary rpc error")
			}
			return []rpc.Log{transferLog(from)}, nil
		},
	}
	cp := &fakeCheckpoints{current: 0}
	sink := &CountingSink{}
	bf := NewBackfiller(Config{BatchSize: 100, BackfillMaxRetries: 5}, reader, cp, sink)
	rec := &sleepRecorder{}
	bf.sleep = rec.sleep

	n, err := bf.Run(context.Background(), "0xc")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n != 1 {
		t.Errorf("processed = %d, want 1", n)
	}
	if calls != 3 {
		t.Errorf("getLogs calls = %d, want 3", calls)
	}
	// two failures → two backoff sleeps, doubling 1s then 2s.
	if len(rec.delays) != 2 || rec.delays[0] != time.Second || rec.delays[1] != 2*time.Second {
		t.Errorf("backoff delays = %v, want [1s 2s]", rec.delays)
	}
}

func TestBackfill_RetryExhaustedHaltsCheckpoint(t *testing.T) {
	reader := &fakeReader{
		head: 250,
		perRange: func(from, _ uint64) ([]rpc.Log, error) {
			if from == 1 {
				return []rpc.Log{transferLog(1)}, nil // first batch ok
			}
			return nil, errors.New("rpc down") // second batch always fails
		},
	}
	cp := &fakeCheckpoints{current: 0}
	sink := &CountingSink{}
	bf := NewBackfiller(Config{BatchSize: 100, BackfillMaxRetries: 3}, reader, cp, sink)
	bf.sleep = (&sleepRecorder{}).sleep

	n, err := bf.Run(context.Background(), "0xc")
	if err == nil {
		t.Fatal("expected error after retry exhaustion")
	}
	if n != 1 {
		t.Errorf("processed = %d, want 1 (only first batch)", n)
	}
	if want := []uint64{100}; !equalUint64(cp.saved, want) {
		t.Errorf("saved = %v, want %v (failed batch must not advance)", cp.saved, want)
	}
}

func TestBackfill_ContextCancelAbortsBetweenBatches(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reader := &fakeReader{
		head: 1000,
		perRange: func(from, _ uint64) ([]rpc.Log, error) {
			cancel() // cancel during the first batch fetch
			return []rpc.Log{transferLog(from)}, nil
		},
	}
	cp := &fakeCheckpoints{current: 0}

	n, err := NewBackfiller(Config{BatchSize: 100}, reader, cp, &CountingSink{}).Run(ctx, "0xc")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	// First batch's work is durable: one event handled, checkpoint 100 saved.
	if n != 1 {
		t.Errorf("processed = %d, want 1", n)
	}
	if want := []uint64{100}; !equalUint64(cp.saved, want) {
		t.Errorf("saved = %v, want %v", cp.saved, want)
	}
}

func TestBackfill_SinkErrorHaltsCheckpoint(t *testing.T) {
	reader := &fakeReader{
		head: 50,
		perRange: func(from, _ uint64) ([]rpc.Log, error) {
			return []rpc.Log{transferLog(from)}, nil
		},
	}
	cp := &fakeCheckpoints{current: 0}

	if _, err := NewBackfiller(Config{BatchSize: 100}, reader, cp, errSink{}).Run(context.Background(), "0xc"); err == nil {
		t.Fatal("expected error from sink")
	}
	if len(cp.saved) != 0 {
		t.Errorf("checkpoint must not advance when sink fails, saved=%v", cp.saved)
	}
}

func TestBackfill_PassesDecodedEventCoordinatesToSink(t *testing.T) {
	reader := &fakeReader{
		head: 10,
		perRange: func(_, _ uint64) ([]rpc.Log, error) {
			l := transferLog(7)
			l.TxHash = "0xabc123"
			l.LogIndex = 4
			l.Address = "0xEmitter000000000000000000000000000000AAAA"
			return []rpc.Log{l}, nil
		},
	}
	cp := &fakeCheckpoints{current: 0}
	sink := &captureSink{}

	if _, err := NewBackfiller(Config{ChainID: 999, BatchSize: 100}, reader, cp, sink).Run(context.Background(), "0xWatched"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(sink.events) != 1 {
		t.Fatalf("events = %d, want 1", len(sink.events))
	}
	ev := sink.events[0]
	if ev.Parsed.EventName != "Transfer" {
		t.Errorf("event = %q, want Transfer", ev.Parsed.EventName)
	}
	if ev.ChainID != 999 {
		t.Errorf("chainID = %d, want 999", ev.ChainID)
	}
	if ev.BlockNumber != 7 {
		t.Errorf("block = %d, want 7", ev.BlockNumber)
	}
	if ev.TxHash != "0xabc123" {
		t.Errorf("txHash = %q, want 0xabc123", ev.TxHash)
	}
	if ev.LogIndex != 4 {
		t.Errorf("logIndex = %d, want 4", ev.LogIndex)
	}
	if ev.ContractAddress != "0xemitter000000000000000000000000000000aaaa" {
		t.Errorf("contract = %q, want lower-cased emitter", ev.ContractAddress)
	}
}

func TestBackfill_HeadErrorPropagates(t *testing.T) {
	reader := &fakeReader{headErr: errors.New("rpc unreachable")}
	if _, err := NewBackfiller(Config{}, reader, &fakeCheckpoints{}, &CountingSink{}).Run(context.Background(), "0xc"); err == nil {
		t.Fatal("expected head error to propagate")
	}
}

func TestBackfill_NegativeHeadRejected(t *testing.T) {
	reader := &fakeReader{headBig: big.NewInt(-1)}
	if _, err := NewBackfiller(Config{}, reader, &fakeCheckpoints{}, &CountingSink{}).Run(context.Background(), "0xc"); err == nil {
		t.Fatal("expected negative head to be rejected")
	}
}

func TestBackfill_CheckpointGetErrorPropagates(t *testing.T) {
	reader := &fakeReader{head: 100}
	cp := &fakeCheckpoints{getErr: ErrCheckpointPoolUnavailable}
	if _, err := NewBackfiller(Config{BatchSize: 10}, reader, cp, &CountingSink{}).Run(context.Background(), "0xc"); err == nil {
		t.Fatal("expected checkpoint Get error to propagate")
	}
}

func TestBackfillRetryDelay_DoublesAndCaps(t *testing.T) {
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{1, 1 * time.Second},
		{2, 2 * time.Second},
		{3, 4 * time.Second},
		{4, 8 * time.Second},
		{5, 16 * time.Second},
		{6, 30 * time.Second}, // 32s capped at 30s
		{20, 30 * time.Second},
	}
	for _, tc := range cases {
		if got := backfillRetryDelay(tc.attempt); got != tc.want {
			t.Errorf("backfillRetryDelay(%d) = %v, want %v", tc.attempt, got, tc.want)
		}
	}
}
