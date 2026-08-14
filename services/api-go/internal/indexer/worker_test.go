package indexer

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeBN struct {
	calls atomic.Int64
	val   *big.Int
	err   error
}

func (f *fakeBN) BlockNumber(_ context.Context) (*big.Int, error) {
	f.calls.Add(1)
	if f.err != nil {
		return nil, f.err
	}
	return f.val, nil
}

func TestWorker_StartPollsAndStopsCleanly(t *testing.T) {
	fake := &fakeBN{val: big.NewInt(42)}
	cfg := Config{PollIntervalMs: 5}
	w := NewWorkerWithClient(cfg, fake)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := w.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	for fake.calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if fake.calls.Load() < 2 {
		t.Fatalf("expected ≥2 poll ticks, got %d", fake.calls.Load())
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()
	if err := w.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}

func TestWorker_StartTwiceErrors(t *testing.T) {
	fake := &fakeBN{val: big.NewInt(1)}
	cfg := Config{PollIntervalMs: 1000}
	w := NewWorkerWithClient(cfg, fake)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := w.Start(ctx); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	defer w.Stop(context.Background())

	if err := w.Start(ctx); err == nil {
		t.Fatal("expected error on second Start, got nil")
	}
}

func TestWorker_StopWithoutStartIsNoop(t *testing.T) {
	w := NewWorkerWithClient(Config{}, &fakeBN{val: big.NewInt(0)})
	if err := w.Stop(context.Background()); err != nil {
		t.Fatalf("Stop on idle worker: %v", err)
	}
}

func TestWorker_ContextCancelStopsLoop(t *testing.T) {
	fake := &fakeBN{val: big.NewInt(1)}
	cfg := Config{PollIntervalMs: 5}
	w := NewWorkerWithClient(cfg, fake)

	ctx, cancel := context.WithCancel(context.Background())
	if err := w.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	cancel()

	if err := w.Stop(context.Background()); err != nil {
		t.Fatalf("Stop after ctx cancel: %v", err)
	}
}

func TestWorker_PollPropagatesError(t *testing.T) {
	fake := &fakeBN{err: errors.New("boom")}
	cfg := Config{PollIntervalMs: 5}
	w := NewWorkerWithClient(cfg, fake)

	if err := w.poll(context.Background()); err == nil {
		t.Fatal("expected poll error, got nil")
	}
	if fake.calls.Load() != 1 {
		t.Fatalf("expected 1 call, got %d", fake.calls.Load())
	}
}

func TestWorker_PollNilClientReturnsError(t *testing.T) {
	w := NewWorkerWithClient(Config{}, nil)
	if err := w.poll(context.Background()); err == nil {
		t.Fatal("expected poll error on nil client, got nil")
	}
}

// fakeRunner is a BlockRangeRunner stub. poll() is called synchronously in
// these tests, so no locking is needed.
type fakeRunner struct {
	calls  map[string]int
	events map[string]int
	errs   map[string]error
}

func (f *fakeRunner) Run(_ context.Context, addr string) (int, error) {
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[addr]++
	return f.events[addr], f.errs[addr]
}

func TestWorker_RunnerDrivesEachWatchedAddress(t *testing.T) {
	fr := &fakeRunner{events: map[string]int{"0xA": 3, "0xB": 0}}
	w := NewWorkerWithRunner(Config{}, fr, []string{"0xA", "0xB"})
	if err := w.poll(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if fr.calls["0xA"] != 1 || fr.calls["0xB"] != 1 {
		t.Fatalf("expected each address called once, got %v", fr.calls)
	}
}

func TestWorker_IndexTickSumsEventsAndContinuesOnError(t *testing.T) {
	fr := &fakeRunner{
		events: map[string]int{"0xA": 2, "0xB": 5},
		errs:   map[string]error{"0xA": errors.New("boom")},
	}
	w := NewWorkerWithRunner(Config{}, fr, []string{"0xA", "0xB"})
	err := w.poll(context.Background())
	if err == nil {
		t.Fatal("expected the address error to surface as firstErr")
	}
	// Despite 0xA erroring, 0xB must still be processed.
	if fr.calls["0xB"] != 1 {
		t.Fatalf("expected 0xB still called after 0xA error, got %v", fr.calls)
	}
}

func TestWorker_RunnerSkipsEmptyAddresses(t *testing.T) {
	fr := &fakeRunner{}
	w := NewWorkerWithRunner(Config{}, fr, []string{"0xA", "", "0xB"})
	if err := w.poll(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if len(fr.calls) != 2 {
		t.Fatalf("empty address should be filtered; calls=%v", fr.calls)
	}
	if _, ok := fr.calls[""]; ok {
		t.Fatal("empty address must not be run")
	}
}

func TestWorker_IndexTickContextCancelAbortsBeforeRun(t *testing.T) {
	fr := &fakeRunner{}
	w := NewWorkerWithRunner(Config{}, fr, []string{"0xA", "0xB"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := w.poll(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if len(fr.calls) != 0 {
		t.Fatalf("no address should run under a cancelled context; calls=%v", fr.calls)
	}
}

func TestWorker_RunnerPathIgnoresNilClient(t *testing.T) {
	// A runner-backed worker has no BlockNumberLooker; poll must take the
	// runner path and not trip the nil-client guard.
	fr := &fakeRunner{}
	w := NewWorkerWithRunner(Config{}, fr, []string{"0xA"})
	if w.client != nil {
		t.Fatal("runner worker should not have a client")
	}
	if err := w.poll(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if fr.calls["0xA"] != 1 {
		t.Fatalf("runner not driven; calls=%v", fr.calls)
	}
}

func TestReconnectBackoff(t *testing.T) {
	base := 100 * time.Millisecond
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{1, 100 * time.Millisecond},
		{2, 200 * time.Millisecond},
		{3, 400 * time.Millisecond},
		{4, 800 * time.Millisecond},
		{20, maxReconnectBackoff}, // capped
	}
	for _, tc := range cases {
		if got := reconnectBackoff(base, tc.attempt); got != tc.want {
			t.Errorf("reconnectBackoff(%v, %d) = %v, want %v", base, tc.attempt, got, tc.want)
		}
	}

	// When the poll interval exceeds the default cap, the backoff floors at the
	// interval (never shorter than a healthy tick) and grows from there.
	big := 60 * time.Second
	if got := reconnectBackoff(big, 1); got != big {
		t.Errorf("attempt 1 = %v, want %v (the interval)", got, big)
	}
	if got := reconnectBackoff(big, 5); got != big {
		t.Errorf("capped = %v, want %v (max(cap, interval))", got, big)
	}
}

func TestWorker_BackoffLoopStaysStoppableWhileFailing(t *testing.T) {
	// A persistently-failing poll must keep the loop alive AND remain promptly
	// stoppable (waitOrStop selects on the stop signal during the backoff).
	fake := &fakeBN{err: errors.New("rpc down")}
	cfg := Config{PollIntervalMs: 5, MaxReconnectAttempts: 3}
	w := NewWorkerWithClient(cfg, fake)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := w.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for fake.calls.Load() < 1 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if fake.calls.Load() < 1 {
		t.Fatalf("expected ≥1 poll attempt while failing, got %d", fake.calls.Load())
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()
	if err := w.Stop(stopCtx); err != nil {
		t.Fatalf("Stop while failing: %v", err)
	}
}

func TestWorker_WatchCurveNormalizesAndDedupes(t *testing.T) {
	w := NewWorkerWithRunner(Config{}, &fakeRunner{}, nil)
	w.WatchCurve("0xAbC")     // lower-cased
	w.WatchCurve("  0xabc  ") // trimmed → duplicate of the above
	w.WatchCurve("")          // ignored
	w.WatchCurve("0xDef")

	got := w.watchSnapshot()
	want := map[string]bool{"0xabc": true, "0xdef": true}
	if len(got) != len(want) {
		t.Fatalf("watch set = %v, want %d unique addresses", got, len(want))
	}
	for _, a := range got {
		if !want[a] {
			t.Errorf("unexpected watched address %q", a)
		}
	}
}

func TestWorker_WatchCurvePickedUpByIndexTick(t *testing.T) {
	fr := &fakeRunner{}
	w := NewWorkerWithRunner(Config{}, fr, []string{"0xfactory"})
	// A curve created at runtime must be scanned on the next tick.
	w.WatchCurve("0xcurve")
	if err := w.poll(context.Background()); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if fr.calls["0xcurve"] != 1 {
		t.Fatalf("runtime-added curve not driven; calls=%v", fr.calls)
	}
	if fr.calls["0xfactory"] != 1 {
		t.Fatalf("static address should still be driven; calls=%v", fr.calls)
	}
}

func TestWorker_WatchCurveConcurrentWithSnapshot(t *testing.T) {
	// -race asserts WatchCurve (write lock) and watchSnapshot (read lock) don't
	// race. The final set must contain every distinct address added.
	w := NewWorkerWithRunner(Config{}, &fakeRunner{}, nil)
	var wg sync.WaitGroup
	const n = 50
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w.WatchCurve(fmt.Sprintf("0xcurve%02d", i))
			_ = w.watchSnapshot()
		}(i)
	}
	wg.Wait()
	if got := len(w.watchSnapshot()); got != n {
		t.Fatalf("watched = %d, want %d distinct curves", got, n)
	}
}

type fakeCurveSource struct {
	curves []string
	err    error
}

func (f fakeCurveSource) KnownCurves(context.Context) ([]string, error) {
	return f.curves, f.err
}

func TestSeedWatchset(t *testing.T) {
	w := NewWorkerWithRunner(Config{}, &fakeRunner{}, nil)
	n, err := SeedWatchset(context.Background(), w, fakeCurveSource{curves: []string{"0xA", "0xa", "0xB"}})
	if err != nil {
		t.Fatalf("SeedWatchset: %v", err)
	}
	if n != 3 {
		t.Fatalf("seeded count = %d, want 3 (rows from source)", n)
	}
	// 0xA / 0xa collapse via WatchCurve's normalize+dedup → {0xa, 0xb}.
	if got := len(w.watchSnapshot()); got != 2 {
		t.Fatalf("watch set = %d, want 2 after dedup", got)
	}
}

func TestSeedWatchset_NilArgsAndErrorPropagation(t *testing.T) {
	if n, err := SeedWatchset(context.Background(), nil, fakeCurveSource{curves: []string{"0xA"}}); n != 0 || err != nil {
		t.Fatalf("nil watcher should no-op, got n=%d err=%v", n, err)
	}
	w := NewWorkerWithRunner(Config{}, &fakeRunner{}, nil)
	if _, err := SeedWatchset(context.Background(), w, fakeCurveSource{err: errors.New("boom")}); err == nil {
		t.Fatal("expected the source error to propagate")
	}
}

func TestBlockNumberCaller_NilGuards(t *testing.T) {
	var bc *BlockNumberCaller
	if _, err := bc.BlockNumber(context.Background()); err == nil {
		t.Fatal("expected error on nil receiver, got nil")
	}
	bc = NewBlockNumberCaller(nil)
	if _, err := bc.BlockNumber(context.Background()); err == nil {
		t.Fatal("expected error on nil rpc client, got nil")
	}
}
