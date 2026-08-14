package indexer

import (
	"context"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
)

// Staking projection: StakingPool's Staked / Unstaked / RewardClaimed /
// EmergencyWithdraw events land append-only in staking_actions (idempotent by
// chain_id + tx_hash + log_index) and, only for genuinely new actions,
// maintain the user_stakes read model (one active row per user+pool, amount
// in token units).
//
// Pool resolution: the emitting contract address is matched against
// staking_pools.metadata->>'contractAddress' for the chain; when no row
// declares the address but the chain has EXACTLY ONE active single-asset
// staking pool, that row is used (the deployments registry ships one
// StakingPool per chain). Ambiguity is logged and the user_stakes step is
// skipped — the raw action row is still durable evidence.
//
// Amounts: user_stakes.amount is Decimal(36,18) in token units; the chain
// amount is raw wei. The staked token (StakingToken/STK) is 18-decimals per
// the deployments registry — the division below assumes that, which is
// recorded here deliberately: a future non-18-dec pool needs a decimals
// column on staking_pools before this projection can serve it.

// stakingActionRow is the extracted, validated staking event.
type stakingActionRow struct {
	action string // STAKE | UNSTAKE | CLAIM | EMERGENCY
	user   string
	amount *big.Int // raw wei (reward amount for CLAIM)
}

func stakingActionRowFrom(ev ParsedEvent, action string) (stakingActionRow, bool) {
	a := ev.Parsed.RuntimeArgs
	r := stakingActionRow{
		action: action,
		user:   lowerAddr(a["user"]),
	}
	if action == "CLAIM" {
		r.amount = argBigInt(a["reward"])
	} else {
		r.amount = argBigInt(a["amount"])
	}
	if r.user == "" || r.amount == nil {
		return stakingActionRow{}, false
	}
	return r, true
}

func (s *DBSink) handleStakingEvent(ctx context.Context, ev ParsedEvent, action string) error {
	r, ok := stakingActionRowFrom(ev, action)
	if !ok {
		return fmt.Errorf("%s missing user/amount (tx %s)", action, ev.TxHash)
	}
	poolAddr := strings.ToLower(ev.ContractAddress)

	poolID, err := s.resolveStakingPoolID(ctx, ev.ChainID, poolAddr)
	if err != nil {
		return err
	}

	var poolIDArg any
	if poolID != "" {
		poolIDArg = poolID
	}
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO staking_actions
			(id, chain_id, pool_id, pool_address, user_address, action, amount_raw,
			 tx_hash, log_index, block_number, created_at)
		VALUES
			(gen_random_uuid()::text, $1, $2, $3, $4, $5, $6, $7, $8, $9, NOW())
		ON CONFLICT (chain_id, tx_hash, log_index) DO NOTHING
	`,
		ev.ChainID, poolIDArg, poolAddr, r.user, r.action, r.amount.String(),
		ev.TxHash, int(ev.LogIndex), int64(ev.BlockNumber),
	)
	if err != nil {
		return fmt.Errorf("insert staking_action %s#%d: %w", ev.TxHash, ev.LogIndex, err)
	}
	if tag.RowsAffected() == 0 {
		return nil // replay — user_stakes already reflects this action
	}
	if poolID == "" {
		slog.WarnContext(ctx, "indexer: staking event on unresolved pool; action recorded, user_stakes skipped",
			"pool", poolAddr, "chain", ev.ChainID, "tx", ev.TxHash)
		return nil
	}
	return s.applyUserStakeAction(ctx, poolID, r)
}

// resolveStakingPoolID maps the emitting pool contract to a staking_pools
// row ("" when unresolved). See package comment for the matching rules.
func (s *DBSink) resolveStakingPoolID(ctx context.Context, chainID int, poolAddr string) (string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id FROM staking_pools
		WHERE chain_id = $1 AND LOWER(COALESCE(metadata->>'contractAddress', '')) = $2
	`, chainID, poolAddr)
	if err != nil {
		return "", fmt.Errorf("resolve staking pool %s: %w", poolAddr, err)
	}
	ids, err := collectIDs(rows)
	if err != nil {
		return "", err
	}
	if len(ids) == 1 {
		return ids[0], nil
	}
	if len(ids) > 1 {
		return "", nil // ambiguous metadata — refuse to guess
	}

	// Fallback: a chain with exactly one active staking pool row.
	rows, err = s.pool.Query(ctx, `
		SELECT id FROM staking_pools
		WHERE chain_id = $1 AND pool_type = 'staking' AND status = 'active'
		LIMIT 2
	`, chainID)
	if err != nil {
		return "", fmt.Errorf("fallback staking pool lookup (chain %d): %w", chainID, err)
	}
	ids, err = collectIDs(rows)
	if err != nil {
		return "", err
	}
	if len(ids) == 1 {
		return ids[0], nil
	}
	return "", nil
}

// applyUserStakeAction maintains the single active user_stakes row for
// (user, pool). Amount arithmetic happens in SQL numeric space with the raw
// wei divided by 1e18 (see package comment for the decimals assumption).
func (s *DBSink) applyUserStakeAction(ctx context.Context, poolID string, r stakingActionRow) error {
	raw := r.amount.String()
	switch r.action {
	case "STAKE":
		// Update the active row; insert one when none exists.
		tag, err := s.pool.Exec(ctx, `
			UPDATE user_stakes
			SET amount = amount + ($3::numeric / 1e18)
			WHERE id = (
				SELECT id FROM user_stakes
				WHERE pool_id = $1 AND user_address = $2 AND unstaked_at IS NULL
				ORDER BY staked_at DESC LIMIT 1
			)
		`, poolID, r.user, raw)
		if err != nil {
			return fmt.Errorf("stake update (%s): %w", r.user, err)
		}
		if tag.RowsAffected() == 0 {
			if _, err := s.pool.Exec(ctx, `
				INSERT INTO user_stakes (id, user_address, pool_id, amount, staked_at)
				VALUES (gen_random_uuid()::text, $1, $2, $3::numeric / 1e18, NOW())
			`, r.user, poolID, raw); err != nil {
				return fmt.Errorf("stake insert (%s): %w", r.user, err)
			}
		}
		return nil

	case "UNSTAKE", "EMERGENCY":
		_, err := s.pool.Exec(ctx, `
			UPDATE user_stakes
			SET amount = GREATEST(0, amount - ($3::numeric / 1e18)),
			    unstaked_at = CASE
			        WHEN amount - ($3::numeric / 1e18) <= 0 THEN NOW()
			        ELSE unstaked_at
			    END
			WHERE id = (
				SELECT id FROM user_stakes
				WHERE pool_id = $1 AND user_address = $2 AND unstaked_at IS NULL
				ORDER BY staked_at DESC LIMIT 1
			)
		`, poolID, r.user, raw)
		if err != nil {
			return fmt.Errorf("unstake update (%s): %w", r.user, err)
		}
		return nil

	case "CLAIM":
		_, err := s.pool.Exec(ctx, `
			UPDATE user_stakes
			SET rewards_claimed = rewards_claimed + ($3::numeric / 1e18)
			WHERE id = (
				SELECT id FROM user_stakes
				WHERE pool_id = $1 AND user_address = $2
				ORDER BY (unstaked_at IS NULL) DESC, staked_at DESC LIMIT 1
			)
		`, poolID, r.user, raw)
		if err != nil {
			return fmt.Errorf("claim update (%s): %w", r.user, err)
		}
		return nil
	}
	return fmt.Errorf("unknown staking action %q", r.action)
}

func collectIDs(rows interface {
	Next() bool
	Scan(dest ...any) error
	Close()
	Err() error
}) ([]string, error) {
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan staking pool id: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
