package indexer

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// projectionStore is the SQL surface shared by pgx.Tx and the projection
// handlers. Keeping it small lets failure-path tests inject a transaction
// without depending on a live database.
type projectionStore interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type projectionTx interface {
	projectionStore
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
}

type beginProjectionTx func(ctx context.Context) (projectionTx, error)

type eventProjection struct {
	store       projectionStore
	afterCommit []func()
}

func (p *eventProjection) deferUntilCommit(effect func()) {
	if effect != nil {
		p.afterCommit = append(p.afterCommit, effect)
	}
}

func (p *eventProjection) publishAfterCommit(ctx context.Context, sink *DBSink, kind, tokenAddr string, payload any) {
	p.deferUntilCommit(func() {
		sink.publish(ctx, kind, tokenAddr, payload)
	})
}

func (p *eventProjection) runAfterCommit() {
	for _, effect := range p.afterCommit {
		effect()
	}
}

// HandleEvent commits the raw event and every derived database write as one
// unit. A projection failure must remain visible to Backfiller so its batch
// checkpoint cannot cross an event that still needs replay.
func (s *DBSink) HandleEvent(ctx context.Context, ev ParsedEvent) error {
	if s == nil || s.beginProjection == nil {
		return ErrSinkPoolNil
	}
	args, err := encodeArgs(ev.Parsed.PersistedArgs)
	if err != nil {
		return fmt.Errorf("indexer: encode args for %s#%d: %w", ev.TxHash, ev.LogIndex, err)
	}

	tx, err := s.beginProjection(ctx)
	if err != nil {
		return fmt.Errorf("indexer: begin event projection %s#%d: %w", ev.TxHash, ev.LogIndex, err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	projection := &eventProjection{store: tx}
	if err := upsertRawEvent(ctx, projection.store, ev, args); err != nil {
		return err
	}
	if err := s.processEvent(ctx, projection, ev); err != nil {
		return fmt.Errorf("indexer: project %s %s#%d: %w", ev.Parsed.EventName, ev.TxHash, ev.LogIndex, err)
	}
	if err := markProjectionComplete(ctx, projection.store, ev); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("indexer: commit event projection %s#%d: %w", ev.TxHash, ev.LogIndex, err)
	}

	projection.runAfterCommit()
	return nil
}

func upsertRawEvent(ctx context.Context, store projectionStore, ev ParsedEvent, args string) error {
	_, err := store.Exec(ctx, `
		INSERT INTO web3_events
			(chain_id, contract_address, event_name, tx_hash, log_index,
			 block_number, actor_address, args, occurred_at, created_at)
		VALUES
			($1, $2, $3, $4, $5, $6, $7, $8::jsonb, NULL, NOW())
		ON CONFLICT (chain_id, contract_address, tx_hash, log_index) DO UPDATE
		SET event_name     = EXCLUDED.event_name,
		    block_number   = EXCLUDED.block_number,
		    actor_address  = EXCLUDED.actor_address,
		    args           = EXCLUDED.args
	`,
		ev.ChainID,
		ev.ContractAddress,
		ev.Parsed.EventName,
		ev.TxHash,
		ev.LogIndex,
		ev.BlockNumber,
		actorOrNil(ev.Parsed.ActorAddress),
		args,
	)
	if err != nil {
		return fmt.Errorf("indexer: upsert web3_events %s#%d: %w", ev.TxHash, ev.LogIndex, err)
	}
	return nil
}

func markProjectionComplete(ctx context.Context, store projectionStore, ev ParsedEvent) error {
	tag, err := store.Exec(ctx, `
		UPDATE web3_events
		SET projection_version = $5,
		    projection_completed_at = NOW(),
		    projection_error = NULL,
		    projection_attempts = projection_attempts + 1,
		    projection_last_attempt_at = NOW()
		WHERE chain_id = $1 AND contract_address = $2
		  AND tx_hash = $3 AND log_index = $4
	`, ev.ChainID, ev.ContractAddress, ev.TxHash, ev.LogIndex, currentProjectionVersion)
	if err != nil {
		return fmt.Errorf("indexer: mark projection complete %s#%d: %w", ev.TxHash, ev.LogIndex, err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("indexer: projection completion row missing for %s#%d", ev.TxHash, ev.LogIndex)
	}
	return nil
}

// processEvent dispatches one event inside the same transaction as its raw
// record. The idempotent raw upsert deliberately does not short-circuit the
// handler: replay is also the repair path for legacy raw-only rows.
func (s *DBSink) processEvent(ctx context.Context, projection *eventProjection, ev ParsedEvent) error {
	switch ev.Parsed.EventName {
	case "TokenCreated":
		return s.handleTokenCreated(ctx, projection, ev)
	case "Buy":
		return s.handleTrade(ctx, projection, ev, "BUY")
	case "Sell":
		return s.handleTrade(ctx, projection, ev, "SELL")
	case "Graduated":
		return s.handleGraduated(ctx, projection, ev)
	case "IncreasePosition":
		return s.handlePerpEvent(ctx, projection, ev, "INCREASE")
	case "DecreasePosition":
		return s.handlePerpEvent(ctx, projection, ev, "DECREASE")
	case "LiquidatePosition":
		return s.handlePerpEvent(ctx, projection, ev, "LIQUIDATION")
	case "Staked":
		return s.handleStakingEvent(ctx, projection, ev, "STAKE")
	case "Unstaked":
		return s.handleStakingEvent(ctx, projection, ev, "UNSTAKE")
	case "RewardClaimed":
		return s.handleStakingEvent(ctx, projection, ev, "CLAIM")
	case "EmergencyWithdraw":
		return s.handleStakingEvent(ctx, projection, ev, "EMERGENCY")
	default:
		return nil
	}
}
