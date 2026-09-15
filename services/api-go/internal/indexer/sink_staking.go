package indexer

import (
	"context"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"
)

// Staking projection: StakingPool's Staked / Unstaked / RewardClaimed /
// EmergencyWithdraw events land append-only in staking_actions (idempotent by
// chain_id + tx_hash + log_index) and deterministically rebuild user_stakes for
// the affected user+pool. Rebuild makes a legacy action-only write repairable
// without double-applying its amount.
//
// Pool resolution: the emitting contract address is matched against
// staking_pools.metadata->>'contractAddress' for the chain; when no row
// declares the address but the chain has EXACTLY ONE active single-asset
// staking pool, that row is used. Missing or ambiguous ownership is retryable;
// attaching an event to a guessed pool would be an irreversible projection.
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

func (s *DBSink) handleStakingEvent(ctx context.Context, projection *eventProjection, ev ParsedEvent, action string) error {
	r, ok := stakingActionRowFrom(ev, action)
	if !ok {
		return fmt.Errorf("%s missing user/amount (tx %s)", action, ev.TxHash)
	}
	poolAddr := strings.ToLower(ev.ContractAddress)

	poolID, err := s.resolveStakingPoolID(ctx, projection.store, ev.ChainID, poolAddr)
	if err != nil {
		return err
	}

	if poolID == "" {
		return fmt.Errorf("staking pool dependency missing or ambiguous for %s on chain %d", poolAddr, ev.ChainID)
	}
	_, err = projection.store.Exec(ctx, `
		INSERT INTO staking_actions
			(id, chain_id, pool_id, pool_address, user_address, action, amount_raw,
			 tx_hash, log_index, block_number, created_at)
		VALUES
			(gen_random_uuid()::text, $1, $2, $3, $4, $5, $6, $7, $8, $9, NOW())
		ON CONFLICT (chain_id, tx_hash, log_index) DO UPDATE SET
			pool_id = EXCLUDED.pool_id,
			pool_address = EXCLUDED.pool_address,
			user_address = EXCLUDED.user_address,
			action = EXCLUDED.action,
			amount_raw = EXCLUDED.amount_raw,
			block_number = EXCLUDED.block_number
	`,
		ev.ChainID, poolID, poolAddr, r.user, r.action, r.amount.String(),
		ev.TxHash, ev.LogIndex, ev.BlockNumber,
	)
	if err != nil {
		return fmt.Errorf("insert staking_action %s#%d: %w", ev.TxHash, ev.LogIndex, err)
	}
	return s.rebuildUserStakes(ctx, projection.store, ev.ChainID, poolID, r.user)
}

// resolveStakingPoolID maps the emitting pool contract to a staking_pools
// row ("" when unresolved). See package comment for the matching rules.
func (s *DBSink) resolveStakingPoolID(ctx context.Context, store projectionStore, chainID int, poolAddr string) (string, error) {
	rows, err := store.Query(ctx, `
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
	rows, err = store.Query(ctx, `
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

type stakingLedgerEntry struct {
	action, amount, txHash string
	logIndex               int
	blockNumber            int64
	createdAt              time.Time
}

type rebuiltUserStake struct {
	id         string
	amount     *big.Int
	rewards    *big.Int
	stakedAt   time.Time
	unstakedAt *time.Time
}

func (s *DBSink) rebuildUserStakes(ctx context.Context, store projectionStore,
	chainID int, poolID, user string) error {
	rows, err := store.Query(ctx, `
		SELECT action, amount_raw, tx_hash, log_index, block_number, created_at
		FROM staking_actions
		WHERE chain_id = $1 AND pool_id = $2 AND user_address = $3
		ORDER BY block_number, log_index, tx_hash
	`, chainID, poolID, user)
	if err != nil {
		return fmt.Errorf("load staking repair ledger (%s/%s): %w", poolID, user, err)
	}
	entries := []stakingLedgerEntry{}
	for rows.Next() {
		var entry stakingLedgerEntry
		if err := rows.Scan(&entry.action, &entry.amount, &entry.txHash, &entry.logIndex,
			&entry.blockNumber, &entry.createdAt); err != nil {
			rows.Close()
			return fmt.Errorf("scan staking repair ledger: %w", err)
		}
		entries = append(entries, entry)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate staking repair ledger: %w", err)
	}
	if len(entries) == 0 {
		return fmt.Errorf("staking repair ledger empty for %s/%s", poolID, user)
	}

	stakes, err := rebuildStakingState(chainID, poolID, user, entries)
	if err != nil {
		return err
	}
	// user_stakes is a chain-derived read model; replacing the scoped rows avoids
	// preserving unverified/manual deltas beside authoritative event history.
	if _, err := store.Exec(ctx, `DELETE FROM user_stakes WHERE pool_id = $1 AND user_address = $2`, poolID, user); err != nil {
		return fmt.Errorf("clear user_stakes projection (%s/%s): %w", poolID, user, err)
	}
	for _, stake := range stakes {
		if _, err := store.Exec(ctx, `
			INSERT INTO user_stakes
				(id, user_address, pool_id, amount, rewards_claimed, staked_at, unstaked_at)
			VALUES ($1, $2, $3, $4::numeric / 1e18, $5::numeric / 1e18, $6, $7)
		`, stake.id, user, poolID, stake.amount.String(), stake.rewards.String(),
			stake.stakedAt, stake.unstakedAt); err != nil {
			return fmt.Errorf("insert rebuilt user_stake (%s/%s): %w", poolID, user, err)
		}
	}
	return nil
}

func rebuildStakingState(chainID int, poolID, user string,
	entries []stakingLedgerEntry) ([]rebuiltUserStake, error) {
	stakes := []rebuiltUserStake{}
	var current *rebuiltUserStake
	for _, entry := range entries {
		amount, ok := new(big.Int).SetString(strings.TrimSpace(entry.amount), 10)
		if !ok || amount.Sign() < 0 {
			return nil, fmt.Errorf("invalid staking ledger amount %q", entry.amount)
		}
		switch entry.action {
		case "STAKE":
			if current == nil {
				current = &rebuiltUserStake{
					id: projectionStableID("user-stake", strconv.Itoa(chainID), poolID, user,
						entry.txHash, strconv.Itoa(entry.logIndex)),
					amount: new(big.Int), rewards: new(big.Int), stakedAt: entry.createdAt,
				}
			}
			current.amount.Add(current.amount, amount)
		case "UNSTAKE", "EMERGENCY":
			if current == nil {
				return nil, fmt.Errorf("staking %s has no active predecessor at block %d log %d",
					entry.action, entry.blockNumber, entry.logIndex)
			}
			current.amount.Sub(current.amount, amount)
			if current.amount.Sign() <= 0 {
				current.amount.SetInt64(0)
				closedAt := entry.createdAt
				current.unstakedAt = &closedAt
				stakes = append(stakes, *current)
				current = nil
			}
		case "CLAIM":
			if current != nil {
				current.rewards.Add(current.rewards, amount)
			} else if len(stakes) > 0 {
				stakes[len(stakes)-1].rewards.Add(stakes[len(stakes)-1].rewards, amount)
			} else {
				return nil, fmt.Errorf("staking CLAIM has no stake predecessor at block %d log %d",
					entry.blockNumber, entry.logIndex)
			}
		default:
			return nil, fmt.Errorf("unknown staking action %q", entry.action)
		}
	}
	if current != nil {
		stakes = append(stakes, *current)
	}
	return stakes, nil
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
