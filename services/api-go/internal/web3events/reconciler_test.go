package web3events

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeReconcileStore struct {
	pending  []StaleAttempt
	outcomes []AttemptOutcome
	selErr   error
	applyErr error
}

func (f *fakeReconcileStore) SelectStaleAttempts(context.Context, ReconcilerConfig) ([]StaleAttempt, error) {
	return f.pending, f.selErr
}

func (f *fakeReconcileStore) ApplyAttemptOutcome(_ context.Context, out AttemptOutcome) error {
	f.outcomes = append(f.outcomes, out)
	return f.applyErr
}

type fakeAttemptVerifier struct {
	receipt      VerifiedReceipt
	receiptErr   error
	present      bool
	presentErr   error
	accountNonce uint64
	nonceErr     error
	nonceCalls   int
}

func (f *fakeAttemptVerifier) VerifyReceipt(context.Context, int, string, string) (VerifiedReceipt, error) {
	return f.receipt, f.receiptErr
}

func (f *fakeAttemptVerifier) AccountNonce(context.Context, int, string) (uint64, error) {
	f.nonceCalls++
	return f.accountNonce, f.nonceErr
}

func (f *fakeAttemptVerifier) TransactionPresent(context.Context, int, string) (bool, error) {
	return f.present, f.presentErr
}

func reconcilerFor(store *fakeReconcileStore, verifier *fakeAttemptVerifier, cfg ReconcilerConfig) *Reconciler {
	return NewReconciler(store, verifier, cfg, nil)
}

func onePending(nonce *uint64, checks int) []StaleAttempt {
	return []StaleAttempt{{ChainID: 11155111, TxHash: "0xabc", Owner: ownerA, SenderNonce: nonce, CheckAttempts: checks}}
}

// missingFor builds a candidate that has already been observed absent `streak`
// times in a row. CheckAttempts is deliberately set high and independently, to
// pin that total checks alone can never trigger a drop.
func missingFor(nonce *uint64, streak int) []StaleAttempt {
	return []StaleAttempt{{
		ChainID: 11155111, TxHash: "0xabc", Owner: ownerA, SenderNonce: nonce,
		CheckAttempts: 99, MissingStreak: streak,
	}}
}

// The gap this exists for: a receipt the browser never reported.
func TestReconcilerResolvesAnAttemptWhoseReceiptWasNeverReported(t *testing.T) {
	block := "0xblock"
	nonce := uint64(4)
	store := &fakeReconcileStore{pending: onePending(&nonce, 0)}
	verifier := &fakeAttemptVerifier{receipt: VerifiedReceipt{
		Status: StatusConfirmed, BlockNumber: 900, BlockHash: &block,
		Confirmations: 6, SenderNonce: &nonce,
	}}

	report, err := reconcilerFor(store, verifier, ReconcilerConfig{}).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if report.Resolved != 1 || report.Examined != 1 {
		t.Fatalf("report = %+v, want 1 examined / 1 resolved", report)
	}
	got := store.outcomes[0]
	if got.Status != StatusConfirmed || got.BlockNumber == nil || *got.BlockNumber != 900 {
		t.Errorf("outcome = %+v, want confirmed at block 900", got)
	}
	if got.Confirmations != 6 || got.BlockHash == nil {
		t.Errorf("chain facts were dropped: %+v", got)
	}
}

// An unreachable node must still record the check, or the same row is
// re-selected first forever and starves the batch.
func TestReconcilerRecordsACheckEvenWhenTheNodeIsUnreachable(t *testing.T) {
	nonce := uint64(4)
	store := &fakeReconcileStore{pending: onePending(&nonce, 0)}
	verifier := &fakeAttemptVerifier{receiptErr: ErrVerificationUnavailable}

	report, err := reconcilerFor(store, verifier, ReconcilerConfig{}).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(store.outcomes) != 1 {
		t.Fatalf("outcomes = %d, want 1 — the check must be recorded anyway", len(store.outcomes))
	}
	if store.outcomes[0].Status != "" {
		t.Errorf("status = %q, want empty — an unreachable node decides nothing", store.outcomes[0].Status)
	}
	if store.outcomes[0].FailureCode != FailureRPCUnavailable {
		t.Errorf("failure code = %q, want %q", store.outcomes[0].FailureCode, FailureRPCUnavailable)
	}
	if report.Unresolved != 1 {
		t.Errorf("report = %+v, want 1 unresolved", report)
	}
}

// 'dropped' needs converging evidence. Each missing piece must block it.
func TestDroppedRequiresAbsenceNonceProgressAndRepeatedAgreement(t *testing.T) {
	nonce := uint64(5)
	cfg := ReconcilerConfig{DropAfterMisses: 3}

	cases := []struct {
		name     string
		attempt  []StaleAttempt
		verifier *fakeAttemptVerifier
		want     string
	}{
		{
			name:    "still in the mempool",
			attempt: missingFor(&nonce, 5),
			verifier: &fakeAttemptVerifier{
				receiptErr: ErrTransactionNotFound, present: true, accountNonce: 99,
			},
			want: "",
		},
		{
			name:    "sender has not moved past it",
			attempt: missingFor(&nonce, 5),
			verifier: &fakeAttemptVerifier{
				receiptErr: ErrTransactionNotFound, present: false, accountNonce: 5,
			},
			want: "",
		},
		{
			name:    "not enough consecutive misses yet",
			attempt: missingFor(&nonce, 0),
			verifier: &fakeAttemptVerifier{
				receiptErr: ErrTransactionNotFound, present: false, accountNonce: 9,
			},
			want: "",
		},
		{
			// The whole point of tracking a separate streak: a row that has
			// been checked many times but whose absences were interrupted by
			// outages must NOT be dropped.
			name:    "many total checks but the streak was broken",
			attempt: missingFor(&nonce, 1),
			verifier: &fakeAttemptVerifier{
				receiptErr: ErrTransactionNotFound, present: false, accountNonce: 9,
			},
			want: "",
		},
		{
			name:    "no nonce, so no corroboration is possible",
			attempt: missingFor(nil, 9),
			verifier: &fakeAttemptVerifier{
				receiptErr: ErrTransactionNotFound, present: false, accountNonce: 9,
			},
			want: "",
		},
		{
			name:    "all evidence converges",
			attempt: missingFor(&nonce, 2),
			verifier: &fakeAttemptVerifier{
				receiptErr: ErrTransactionNotFound, present: false, accountNonce: 9,
			},
			want: StatusDropped,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeReconcileStore{pending: tc.attempt}
			if _, err := reconcilerFor(store, tc.verifier, cfg).RunOnce(context.Background()); err != nil {
				t.Fatalf("RunOnce: %v", err)
			}
			if got := store.outcomes[0].Status; got != tc.want {
				t.Errorf("status = %q, want %q", got, tc.want)
			}
		})
	}
}

// Without a nonce the reconciler must not even ask for the account nonce —
// there is nothing it could conclude from the answer.
func TestReconcilerDoesNotProbeTheAccountNonceWhenTheAttemptHasNone(t *testing.T) {
	store := &fakeReconcileStore{pending: onePending(nil, 9)}
	verifier := &fakeAttemptVerifier{receiptErr: ErrTransactionNotFound, present: false, accountNonce: 9}
	if _, err := reconcilerFor(store, verifier, ReconcilerConfig{}).RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if verifier.nonceCalls != 0 {
		t.Errorf("account nonce probed %d times for an attempt with no nonce", verifier.nonceCalls)
	}
}

// A reverted transaction is a real, mined outcome — not a failure to reconcile.
func TestReconcilerRecordsARevertAsFailedRatherThanUnresolved(t *testing.T) {
	nonce := uint64(2)
	store := &fakeReconcileStore{pending: onePending(&nonce, 0)}
	verifier := &fakeAttemptVerifier{receipt: VerifiedReceipt{Status: StatusFailed, BlockNumber: 12}}
	report, err := reconcilerFor(store, verifier, ReconcilerConfig{}).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if store.outcomes[0].Status != StatusFailed || report.Resolved != 1 {
		t.Errorf("outcome = %+v report = %+v, want a resolved 'failed'", store.outcomes[0], report)
	}
}

// A zero interval means the loop never runs — the reconciler is opt-in.
func TestReconcilerWithNoIntervalDoesNothingAndReturns(t *testing.T) {
	store := &fakeReconcileStore{pending: onePending(nil, 0)}
	done := make(chan struct{})
	go func() {
		reconcilerFor(store, &fakeAttemptVerifier{}, ReconcilerConfig{}).Run(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run must return immediately when no interval is configured")
	}
	if len(store.outcomes) != 0 {
		t.Errorf("a disabled reconciler wrote %d outcomes", len(store.outcomes))
	}
}

func TestReconcilerRefusesToRunUnconfigured(t *testing.T) {
	if _, err := NewReconciler(nil, nil, ReconcilerConfig{}, nil).RunOnce(context.Background()); err == nil {
		t.Fatal("an unconfigured reconciler must not report success")
	}
	var store *fakeReconcileStore = &fakeReconcileStore{selErr: errors.New("db down")}
	if _, err := reconcilerFor(store, &fakeAttemptVerifier{}, ReconcilerConfig{}).RunOnce(context.Background()); err == nil {
		t.Fatal("a failed selection must surface as an error")
	}
}
