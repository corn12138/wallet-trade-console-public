package web3events

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// Context columns, in parameter order after each statement's own values.
const contextColumnList = `execution_context_state, execution_context_mode, execution_context_claim_digest,
	execution_context_claimed_revision, execution_context_operation, execution_context_market, execution_context_binding,
	execution_context_consumer, execution_context_manifest, execution_context_reason, execution_context_diagnostic,
	execution_context_revision, execution_context_classified_at`

// The only context change after insertion (§6.3): an unresolved claim
// resolved by the identical claim.
const contextUpgrade = `web3_transactions.execution_context_state = 'claimUnverified'
	AND web3_transactions.execution_context_claim_digest = EXCLUDED.execution_context_claim_digest
	AND EXCLUDED.execution_context_state IN ('recorded', 'claimMismatch')`

func contextValues(first int) string {
	p := make([]string, 12)
	for i := range p {
		p[i] = fmt.Sprintf("$%d", first+i)
	}
	return strings.Join(p, ", ") + fmt.Sprintf(", CASE WHEN $%d::text IS NULL THEN NULL ELSE now() END", first)
}

func contextAssignments() string {
	u := contextUpgrade
	return `execution_context_state = CASE WHEN ` + u + ` THEN EXCLUDED.execution_context_state ELSE web3_transactions.execution_context_state END,
			execution_context_reason = CASE WHEN ` + u + ` THEN EXCLUDED.execution_context_reason ELSE web3_transactions.execution_context_reason END,
			execution_context_revision = CASE WHEN ` + u + ` THEN EXCLUDED.execution_context_revision ELSE web3_transactions.execution_context_revision END,
			execution_context_classified_at = CASE WHEN ` + u + ` THEN now() ELSE web3_transactions.execution_context_classified_at END`
}

func contextArgs(w *ContextWrite) []any {
	if w == nil {
		return []any{nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil}
	}
	state := w.State
	return []any{&state, w.Mode, w.ClaimDigest, w.ClaimedRevision, w.Operation, w.Market, w.Binding, w.Consumer, w.Manifest,
		w.Reason, w.Diagnostic, w.Revision}
}

// writeAttemptRowContext is writeAttemptRow with the context columns written
// only in the INSERT arm: the arm PostgreSQL chooses under the unique
// (chain_id, tx_hash) index decides the insertion outcome, so a row that
// already exists — pre-migration, old binary, first reported without a
// claim — keeps its null (notRecorded) context.
func writeAttemptRowContext(ctx context.Context, tx attemptTx, in attemptWrite, actionID string, attemptNumber int, nonce *int64) (TransactionRow, error) {
	returning := `RETURNING id, chain_id, tx_hash, from_address, to_address, contract_address,
		          value, gas_used, gas_price, block_number, status, tx_type, metadata, created_at, action_id,
                  execution_context_state, execution_context_claim_digest, (xmax = 0)`
	if in.receipt == nil {
		args := append([]any{in.chainID, in.txHash, in.fromAddress, in.toAddress, in.contractAddress,
			in.value, in.txType, metaArg(in.metadata), actionID, attemptNumber, nonce, in.blockHash}, contextArgs(in.context)...)
		return scanContextTxRow(ctx, actionID, in.context, in.contextPrior, tx.QueryRow(ctx, `
			INSERT INTO web3_transactions
				(chain_id, tx_hash, from_address, to_address, contract_address,
				 value, gas_used, gas_price, block_number, status, tx_type, metadata,
				 created_at, action_id, attempt_number, sender_nonce, block_hash,
				 submitted_at, last_checked_at, `+contextColumnList+`)
			VALUES ($1,$2,$3,$4,$5,$6,NULL,NULL,0,'pending',$7,$8, now(), $9,$10,$11,$12, now(), now(), `+contextValues(13)+`)
			ON CONFLICT (chain_id, tx_hash) DO UPDATE SET
				to_address       = COALESCE(EXCLUDED.to_address, web3_transactions.to_address),
				contract_address = COALESCE(EXCLUDED.contract_address, web3_transactions.contract_address),
				value            = COALESCE(EXCLUDED.value, web3_transactions.value),
				tx_type          = COALESCE(EXCLUDED.tx_type, web3_transactions.tx_type),
				metadata         = COALESCE(EXCLUDED.metadata, web3_transactions.metadata),
				action_id        = COALESCE(web3_transactions.action_id, EXCLUDED.action_id),
				attempt_number   = CASE WHEN web3_transactions.action_id IS NULL
				                        THEN EXCLUDED.attempt_number ELSE web3_transactions.attempt_number END,
				sender_nonce     = COALESCE(web3_transactions.sender_nonce, EXCLUDED.sender_nonce),
				block_hash       = COALESCE(EXCLUDED.block_hash, web3_transactions.block_hash),
				last_checked_at  = now(),
				status           = CASE
					WHEN web3_transactions.status IN ('confirmed','failed','replaced','dropped','reorged')
						THEN web3_transactions.status
					ELSE 'pending'
				END,
				`+contextAssignments()+`
			WHERE LOWER(web3_transactions.from_address) = LOWER(EXCLUDED.from_address)
			`+returning, args...))
	}
	args := append([]any{in.chainID, in.txHash, in.fromAddress, in.toAddress, in.contractAddress,
		in.value, in.receipt.gasUsed, in.receipt.gasPrice, in.receipt.blockNumber,
		in.receipt.status, in.txType, metaArg(in.metadata), actionID, attemptNumber,
		nonce, in.blockHash, in.receipt.confirmations}, contextArgs(in.context)...)
	return scanContextTxRow(ctx, actionID, in.context, in.contextPrior, tx.QueryRow(ctx, `
		INSERT INTO web3_transactions
			(chain_id, tx_hash, from_address, to_address, contract_address,
			 value, gas_used, gas_price, block_number, status, tx_type, metadata,
			 created_at, action_id, attempt_number, sender_nonce, block_hash,
			 confirmations, submitted_at, confirmed_at, last_checked_at, `+contextColumnList+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12, now(), $13,$14,$15,$16,$17, now(), now(), now(), `+contextValues(18)+`)
		ON CONFLICT (chain_id, tx_hash) DO UPDATE SET
			to_address       = COALESCE(EXCLUDED.to_address, web3_transactions.to_address),
			contract_address = COALESCE(EXCLUDED.contract_address, web3_transactions.contract_address),
			value            = COALESCE(EXCLUDED.value, web3_transactions.value),
			tx_type          = COALESCE(EXCLUDED.tx_type, web3_transactions.tx_type),
			metadata         = COALESCE(EXCLUDED.metadata, web3_transactions.metadata),
			action_id        = COALESCE(web3_transactions.action_id, EXCLUDED.action_id),
			attempt_number   = CASE WHEN web3_transactions.action_id IS NULL
			                        THEN EXCLUDED.attempt_number ELSE web3_transactions.attempt_number END,
			sender_nonce     = COALESCE(web3_transactions.sender_nonce, EXCLUDED.sender_nonce),
			last_checked_at  = now(),
			block_number     = CASE WHEN web3_transactions.status = 'reorged'
			                        THEN web3_transactions.block_number ELSE EXCLUDED.block_number END,
			block_hash       = CASE WHEN web3_transactions.status = 'reorged'
			                        THEN web3_transactions.block_hash ELSE EXCLUDED.block_hash END,
			confirmations    = CASE WHEN web3_transactions.status = 'reorged'
			                        THEN web3_transactions.confirmations ELSE EXCLUDED.confirmations END,
			gas_used         = CASE WHEN web3_transactions.status = 'reorged'
			                        THEN web3_transactions.gas_used ELSE EXCLUDED.gas_used END,
			gas_price        = CASE WHEN web3_transactions.status = 'reorged'
			                        THEN web3_transactions.gas_price ELSE EXCLUDED.gas_price END,
			confirmed_at     = COALESCE(web3_transactions.confirmed_at, now()),
			status           = CASE
				WHEN web3_transactions.status IN ('reorged','replaced','dropped')
					THEN web3_transactions.status
				ELSE EXCLUDED.status
			END,
			`+contextAssignments()+`
		WHERE LOWER(web3_transactions.from_address) = LOWER(EXCLUDED.from_address)
		`+returning, args...))
}

const contextSavepoint = "web3_tx_context"

// Only migration-owned context constraints may downgrade a context write.
// A prefix alone could accidentally swallow unrelated constraints introduced
// by a later migration; native 22001 has no reliable column attribution.
func isContextStorageError(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	if pgErr.Code == "23503" {
		return pgErr.ConstraintName == "web3_tx_ctx_revision_fkey"
	}
	if pgErr.Code != "23514" {
		return false
	}
	switch pgErr.ConstraintName {
	case "web3_tx_ctx_state_check", "web3_tx_ctx_mode_check", "web3_tx_ctx_claim_digest_check",
		"web3_tx_ctx_claimed_revision_check", "web3_tx_ctx_operation_check", "web3_tx_ctx_market_check",
		"web3_tx_ctx_binding_check", "web3_tx_ctx_consumer_check", "web3_tx_ctx_manifest_check",
		"web3_tx_ctx_reason_check", "web3_tx_ctx_diagnostic_check", "web3_tx_ctx_shape_check":
		return true
	default:
		return false
	}
}

func logContextStorageRejected(ctx context.Context, chainID int, err error) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		// PostgreSQL error details may contain the rejected row, including
		// private metadata. Only fixed migration identifiers reach telemetry.
		slog.ErrorContext(ctx, "contextStorageRejected", "chainId", chainID,
			"sqlState", pgErr.Code, "constraint", pgErr.ConstraintName)
	}
}

// writeAttemptRowWithContext bounds the context-bearing upsert with a
// savepoint (review F1). Action resolution and attempt numbering happened
// before it and survive a rollback to it; on a context-only constraint
// failure the same upsert runs once more with no context. Its INSERT arm then
// records the attempt notRecorded, and its conflict arm leaves an existing
// row's context untouched.
func writeAttemptRowWithContext(ctx context.Context, tx attemptTx, in attemptWrite, actionID string, attemptNumber int) (TransactionRow, error) {
	if !in.contextColumns || in.context == nil {
		return writeAttemptRow(ctx, tx, in, actionID, attemptNumber)
	}
	if _, err := tx.Exec(ctx, "SAVEPOINT "+contextSavepoint); err != nil {
		return TransactionRow{}, fmt.Errorf("web3events: context savepoint: %w", err)
	}
	row, err := writeAttemptRow(ctx, tx, in, actionID, attemptNumber)
	if err != nil {
		if !isContextStorageError(err) {
			return TransactionRow{}, err
		}
		if _, rbErr := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT "+contextSavepoint); rbErr != nil {
			return TransactionRow{}, fmt.Errorf("web3events: context rollback: %w", rbErr)
		}
		logContextStorageRejected(ctx, in.chainID, err)
		fallback := in
		fallback.context = nil
		if row, err = writeAttemptRow(ctx, tx, fallback, actionID, attemptNumber); err != nil {
			return TransactionRow{}, err
		}
	}
	if _, err := tx.Exec(ctx, "RELEASE SAVEPOINT "+contextSavepoint); err != nil {
		return TransactionRow{}, fmt.Errorf("web3events: context release: %w", err)
	}
	return row, nil
}
