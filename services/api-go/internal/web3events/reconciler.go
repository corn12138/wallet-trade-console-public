package web3events

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// Reconciler repairs attempts that were left mid-flight.
//
// The gap it closes is real and reachable without any failure on our side: the
// browser only reports a receipt while it holds a web3 session, and the auth
// provider clears that session the instant the connected address changes. A
// transaction submitted just before an account switch confirms on chain, shows
// as confirmed in the UI, and stays 'pending' with block_number 0 in the
// database forever. The same happens whenever the receipt POST simply fails.
//
// Every write it makes is idempotent and derived from chain facts, so two API
// replicas running it concurrently is harmless. WP2 gives it its own role with
// real leases, which is where every other reconciler in this repository lives.
type Reconciler struct {
	store    ReconcilerStore
	verifier AttemptVerifier
	log      *slog.Logger
	cfg      ReconcilerConfig
}

// ReconcilerConfig bounds the loop.
type ReconcilerConfig struct {
	// Interval between cycles. Zero disables the reconciler entirely.
	Interval time.Duration
	// BatchSize caps how many attempts one cycle examines.
	BatchSize int
	// BaseRecheck is the minimum age before an attempt is looked at again.
	BaseRecheck time.Duration
	// MaxRecheck caps the exponential backoff.
	MaxRecheck time.Duration
	// DropAfterMisses is how many consecutive cycles must agree that a
	// transaction is gone before it is called dropped. One miss is not
	// evidence: a load-balanced or lagging node produces it routinely.
	DropAfterMisses int
	// FinalityDepth is the confirmation count at which an attempt is stamped
	// finalized. It is a depth heuristic, not canonical-chain finality — WP2
	// owns that — so it is only ever applied to an observed receipt.
	FinalityDepth int
}

// DefaultReconcilerConfig is used for every field left zero.
var DefaultReconcilerConfig = ReconcilerConfig{
	BatchSize:       100,
	BaseRecheck:     30 * time.Second,
	MaxRecheck:      30 * time.Minute,
	DropAfterMisses: 3,
	FinalityDepth:   12,
}

// StaleAttempt is one candidate for repair.
type StaleAttempt struct {
	ChainID       int
	TxHash        string
	Owner         string
	SenderNonce   *uint64
	CheckAttempts int
	// MissingStreak counts CONSECUTIVE cycles that found the transaction gone.
	// CheckAttempts is not a substitute: it grows on every outcome, so two RPC
	// outages plus one absence would otherwise look like three agreeing
	// observations.
	MissingStreak int
}

// AttemptOutcome is what one examination concluded. An empty Status means
// "nothing decided" — the attempt is still in flight, or the node could not be
// reached — and only the check bookkeeping is written.
type AttemptOutcome struct {
	ChainID       int
	TxHash        string
	Status        string
	BlockNumber   *int64
	BlockHash     *string
	Confirmations int
	GasUsed       *int64
	GasPrice      *int64
	SenderNonce   *uint64
	FailureCode   string
	FailureDetail *string
	// Missing is true only when the chain answered and said the transaction is
	// not there. An unreachable node is NOT missing, and resets nothing.
	Missing bool
	// Finalized marks that the attempt reached the configured confirmation
	// depth. WP2 owns real finality; this is depth, and it is only ever set
	// from an observed receipt.
	Finalized bool
}

// ReconcilerStore is the persistence surface. Repository satisfies it.
type ReconcilerStore interface {
	SelectStaleAttempts(ctx context.Context, cfg ReconcilerConfig) ([]StaleAttempt, error)
	ApplyAttemptOutcome(ctx context.Context, outcome AttemptOutcome) error
}

// AttemptVerifier is the chain surface. *RPCTransactionVerifier satisfies it.
type AttemptVerifier interface {
	VerifyReceipt(ctx context.Context, chainID int, txHash, owner string) (VerifiedReceipt, error)
	AccountNonce(ctx context.Context, chainID int, address string) (uint64, error)
	TransactionPresent(ctx context.Context, chainID int, txHash string) (bool, error)
}

func NewReconciler(store ReconcilerStore, verifier AttemptVerifier, cfg ReconcilerConfig, log *slog.Logger) *Reconciler {
	if log == nil {
		log = slog.Default()
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = DefaultReconcilerConfig.BatchSize
	}
	if cfg.BaseRecheck <= 0 {
		cfg.BaseRecheck = DefaultReconcilerConfig.BaseRecheck
	}
	if cfg.MaxRecheck <= 0 {
		cfg.MaxRecheck = DefaultReconcilerConfig.MaxRecheck
	}
	if cfg.DropAfterMisses <= 0 {
		cfg.DropAfterMisses = DefaultReconcilerConfig.DropAfterMisses
	}
	if cfg.FinalityDepth <= 0 {
		cfg.FinalityDepth = DefaultReconcilerConfig.FinalityDepth
	}
	return &Reconciler{store: store, verifier: verifier, cfg: cfg, log: log}
}

// Run drives cycles until the context is cancelled. A cycle error is logged and
// the loop continues: a single bad cycle must not take the repair path down.
func (r *Reconciler) Run(ctx context.Context) {
	if r == nil || r.cfg.Interval <= 0 {
		return
	}
	ticker := time.NewTicker(r.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			report, err := r.RunOnce(ctx)
			if err != nil && !errors.Is(err, context.Canceled) {
				r.log.WarnContext(ctx, "web3 transaction reconcile cycle failed", "err", err)
				continue
			}
			if report.Examined > 0 {
				r.log.InfoContext(ctx, "web3 transaction reconcile cycle",
					"examined", report.Examined, "resolved", report.Resolved, "unresolved", report.Unresolved)
			}
		}
	}
}

// ReconcileReport summarises one cycle.
type ReconcileReport struct {
	Examined   int
	Resolved   int
	Unresolved int
}

// RunOnce examines one bounded batch. It is safe to call on startup and safe to
// call concurrently from another replica.
func (r *Reconciler) RunOnce(ctx context.Context) (ReconcileReport, error) {
	var report ReconcileReport
	if r == nil || r.store == nil || r.verifier == nil {
		return report, errors.New("web3events: reconciler not configured")
	}
	attempts, err := r.store.SelectStaleAttempts(ctx, r.cfg)
	if err != nil {
		return report, err
	}
	for _, attempt := range attempts {
		if ctx.Err() != nil {
			return report, ctx.Err()
		}
		report.Examined++
		outcome := r.examine(ctx, attempt)
		// The check is recorded on EVERY outcome, including a verifier error.
		// Without that, a permanently failing row is re-selected first on every
		// cycle and starves the batch forever.
		if err := r.store.ApplyAttemptOutcome(ctx, outcome); err != nil {
			r.log.WarnContext(ctx, "web3 transaction reconcile write failed",
				"chainId", attempt.ChainID, "err", err)
			continue
		}
		if outcome.Status == "" {
			report.Unresolved++
		} else {
			report.Resolved++
		}
	}
	return report, nil
}

// examine decides one attempt's fate from chain evidence only.
func (r *Reconciler) examine(ctx context.Context, attempt StaleAttempt) AttemptOutcome {
	outcome := AttemptOutcome{ChainID: attempt.ChainID, TxHash: attempt.TxHash}

	receipt, err := r.verifier.VerifyReceipt(ctx, attempt.ChainID, attempt.TxHash, attempt.Owner)
	switch {
	case err == nil:
		block := receipt.BlockNumber
		outcome.Status = receipt.Status
		outcome.BlockNumber = &block
		outcome.BlockHash = receipt.BlockHash
		outcome.Confirmations = receipt.Confirmations
		outcome.GasUsed = receipt.GasUsed
		outcome.GasPrice = receipt.GasPrice
		outcome.SenderNonce = receipt.SenderNonce
		outcome.Finalized = receipt.Confirmations >= r.cfg.FinalityDepth
		return outcome

	case errors.Is(err, ErrReceiptPending), errors.Is(err, ErrTransactionNotFound):
		// Still in flight, or gone. Only the second is actionable, and only
		// with corroboration.
		return r.considerDropped(ctx, attempt, outcome, err)

	case errors.Is(err, ErrSenderMismatch):
		outcome.FailureCode = FailureSenderMismatch
		outcome.FailureDetail = SanitizeFailureDetail(FailureSenderMismatch, nil)
		return outcome

	case errors.Is(err, ErrUnsupportedChain):
		outcome.FailureCode = FailureUnsupportedChain
		outcome.FailureDetail = SanitizeFailureDetail(FailureUnsupportedChain, nil)
		return outcome

	default:
		outcome.FailureCode = FailureRPCUnavailable
		outcome.FailureDetail = SanitizeFailureDetail(FailureRPCUnavailable, err)
		return outcome
	}
}

// considerDropped writes 'dropped' only on converging evidence: the node no
// longer knows the transaction, the sender's account nonce has moved past it,
// and enough consecutive cycles have agreed. Absence alone is not evidence — a
// lagging or load-balanced node produces it routinely, and nothing would ever
// repair a wrongly-dropped row.
func (r *Reconciler) considerDropped(
	ctx context.Context,
	attempt StaleAttempt,
	outcome AttemptOutcome,
	cause error,
) AttemptOutcome {
	if attempt.SenderNonce == nil {
		// Without the nonce there is no corroboration available at all, so
		// 'dropped' stays unreachable rather than guessed.
		outcome.FailureCode = FailureNotFound
		outcome.FailureDetail = SanitizeFailureDetail(FailureNotFound, nil)
		return outcome
	}
	if !errors.Is(cause, ErrTransactionNotFound) {
		outcome.FailureCode = FailureReceiptPending
		return outcome
	}

	present, err := r.verifier.TransactionPresent(ctx, attempt.ChainID, attempt.TxHash)
	if err != nil {
		outcome.FailureCode = FailureRPCUnavailable
		outcome.FailureDetail = SanitizeFailureDetail(FailureRPCUnavailable, err)
		return outcome
	}
	if present {
		outcome.FailureCode = FailureReceiptPending
		return outcome
	}
	// From here the chain has answered and said the transaction is gone.
	outcome.Missing = true

	accountNonce, err := r.verifier.AccountNonce(ctx, attempt.ChainID, attempt.Owner)
	if err != nil {
		outcome.FailureCode = FailureRPCUnavailable
		outcome.FailureDetail = SanitizeFailureDetail(FailureRPCUnavailable, err)
		return outcome
	}
	if accountNonce <= *attempt.SenderNonce {
		// The sender has not moved past it; it can still be mined.
		outcome.FailureCode = FailureReceiptPending
		return outcome
	}
	if attempt.MissingStreak+1 < r.cfg.DropAfterMisses {
		outcome.FailureCode = FailureNotFound
		return outcome
	}

	outcome.Status = StatusDropped
	outcome.FailureCode = FailureNotFound
	outcome.FailureDetail = SanitizeFailureDetail(FailureNotFound, fmt.Errorf(
		"account nonce %d passed attempt nonce %d", accountNonce, *attempt.SenderNonce))
	return outcome
}
