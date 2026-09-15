// Package staking is the Go port of legacy NestJS staking/.
//
// Phase 3a added the discover-feed slice (ListActivePools, FindPool,
// ListUserStakes). Phase 4y adds the full HTTP surface — every method
// the NestJS controller calls is now available.
package staking

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PoolView is the projection of staking_pools consumed by discover/home,
// the earn-products feed, and the staking HTTP routes. APY/TVL/RewardToken
// are nullable in the DB.
type PoolView struct {
	ID           string     `json:"id"`
	Name         string     `json:"name"`
	PoolType     string     `json:"poolType"`
	TokenAddress string     `json:"tokenAddress"`
	RewardToken  *string    `json:"rewardToken"`
	APY          *float64   `json:"apy"`
	TVL          *float64   `json:"tvl"`
	ChainID      int        `json:"chainId"`
	Status       string     `json:"status"`
	StartsAt     *time.Time `json:"startsAt,omitempty"`
	EndsAt       *time.Time `json:"endsAt,omitempty"`
	// Metadata stays JSON-raw so callers can pass NestJS-shaped values
	// through unchanged.
	Metadata  json.RawMessage `json:"metadata,omitempty"`
	CreatedAt time.Time       `json:"createdAt,omitempty"`
	UpdatedAt time.Time       `json:"updatedAt,omitempty"`
	// StakeCount mirrors Prisma's `_count: { stakes: true }` include.
	StakeCount int64 `json:"stakeCount,omitempty"`
}

// PoolFilters mirrors findAllPools(filters?) — every field optional.
type PoolFilters struct {
	PoolType *string
	Status   *string
	ChainID  *int
}

// PoolStats matches getPoolStats.
type PoolStats struct {
	TotalPools int64   `json:"totalPools"`
	TotalTvl   float64 `json:"totalTvl"`
	HighestApy float64 `json:"highestApy"`
}

// CreatePoolInput matches createPool's body.
type CreatePoolInput struct {
	Name         string          `json:"name"`
	ChainID      int             `json:"chainId"`
	PoolType     string          `json:"poolType"`
	TokenAddress string          `json:"tokenAddress"`
	RewardToken  *string         `json:"rewardToken,omitempty"`
	APY          *float64        `json:"apy,omitempty"`
	TVL          *float64        `json:"tvl,omitempty"`
	StartsAt     *time.Time      `json:"startsAt,omitempty"`
	EndsAt       *time.Time      `json:"endsAt,omitempty"`
	Metadata     json.RawMessage `json:"metadata,omitempty"`
}

// UpdatePoolInput mirrors the NestJS Partial<...> body — all fields nilable.
type UpdatePoolInput struct {
	Name     *string         `json:"name,omitempty"`
	APY      *float64        `json:"apy,omitempty"`
	TVL      *float64        `json:"tvl,omitempty"`
	Status   *string         `json:"status,omitempty"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

// RecordStakeInput matches recordStake's body. Amount is a JSON
// number on the wire (NestJS accepts number) — we store it as a string
// so Decimal(36,18) round-trips faithfully.
type RecordStakeInput struct {
	UserAddress string  `json:"userAddress"`
	PoolID      string  `json:"poolId"`
	Amount      float64 `json:"amount"`
}

// Repository wraps the pgx pool with the staking-pool queries.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository binds the repo to a pgxpool. A nil pool yields a repo
// whose methods all return ErrPoolUnavailable.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// ErrPoolUnavailable mirrors the pattern from sibling repos.
var ErrPoolUnavailable = fmt.Errorf("staking repository: database pool not configured")

// ErrPoolNotFound is returned by FindPool/UpdatePool/etc. on miss.
var ErrPoolNotFound = errors.New("staking pool not found")

// ListActivePools — discover/home feed (status='active', newest first).
func (r *Repository) ListActivePools(ctx context.Context, chainID *int) ([]PoolView, error) {
	if r.pool == nil {
		return nil, ErrPoolUnavailable
	}
	var (
		rows pgx.Rows
		err  error
	)
	if chainID != nil {
		rows, err = r.pool.Query(ctx, `
			SELECT id, name, pool_type, token_address, reward_token, apy, tvl, chain_id, status
			FROM staking_pools
			WHERE status = $1 AND chain_id = $2
			ORDER BY created_at DESC
		`, "active", *chainID)
	} else {
		rows, err = r.pool.Query(ctx, `
			SELECT id, name, pool_type, token_address, reward_token, apy, tvl, chain_id, status
			FROM staking_pools
			WHERE status = $1
			ORDER BY created_at DESC
		`, "active")
	}
	if err != nil {
		return nil, fmt.Errorf("query staking_pools: %w", err)
	}
	defer rows.Close()

	out := make([]PoolView, 0)
	for rows.Next() {
		var p PoolView
		if err := rows.Scan(&p.ID, &p.Name, &p.PoolType, &p.TokenAddress, &p.RewardToken,
			&p.APY, &p.TVL, &p.ChainID, &p.Status); err != nil {
			return nil, fmt.Errorf("scan staking_pools: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate staking_pools: %w", err)
	}
	return out, nil
}

// ListAllPools is the HTTP /pools surface with the full filter set
// + a per-pool stake count.
func (r *Repository) ListAllPools(ctx context.Context, f PoolFilters) ([]PoolView, error) {
	if r.pool == nil {
		return nil, ErrPoolUnavailable
	}

	clauses := []string{}
	args := []any{}
	add := func(clause string, value any) {
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf(clause, len(args)))
	}
	if f.PoolType != nil && *f.PoolType != "" {
		add("pool_type = $%d", *f.PoolType)
	}
	if f.Status != nil && *f.Status != "" {
		add("status = $%d", *f.Status)
	}
	if f.ChainID != nil {
		add("chain_id = $%d", *f.ChainID)
	}

	query := `
		SELECT p.id, p.name, p.pool_type, p.token_address, p.reward_token,
		       p.apy, p.tvl, p.chain_id, p.status,
		       p.starts_at, p.ends_at, p.metadata, p.created_at, p.updated_at,
		       COALESCE(c.cnt, 0)::bigint AS stake_count
		FROM staking_pools p
		LEFT JOIN (
			SELECT pool_id, COUNT(*)::bigint AS cnt
			FROM user_stakes
			GROUP BY pool_id
		) c ON c.pool_id = p.id
	`
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	query += " ORDER BY p.created_at DESC"

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query staking_pools (all): %w", err)
	}
	defer rows.Close()
	return scanPoolViews(rows, true)
}

func scanPoolViews(rows pgx.Rows, withMeta bool) ([]PoolView, error) {
	out := make([]PoolView, 0)
	for rows.Next() {
		var (
			p        PoolView
			metaJSON []byte
		)
		if withMeta {
			if err := rows.Scan(
				&p.ID, &p.Name, &p.PoolType, &p.TokenAddress, &p.RewardToken,
				&p.APY, &p.TVL, &p.ChainID, &p.Status,
				&p.StartsAt, &p.EndsAt, &metaJSON, &p.CreatedAt, &p.UpdatedAt,
				&p.StakeCount,
			); err != nil {
				return nil, fmt.Errorf("scan staking_pools: %w", err)
			}
			if len(metaJSON) > 0 {
				p.Metadata = metaJSON
			}
		} else {
			if err := rows.Scan(
				&p.ID, &p.Name, &p.PoolType, &p.TokenAddress, &p.RewardToken,
				&p.APY, &p.TVL, &p.ChainID, &p.Status,
			); err != nil {
				return nil, fmt.Errorf("scan staking_pools: %w", err)
			}
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate staking_pools: %w", err)
	}
	return out, nil
}

// FindPool — single pool by id. Includes stake count.
func (r *Repository) FindPool(ctx context.Context, id string) (PoolView, error) {
	if r.pool == nil {
		return PoolView{}, ErrPoolUnavailable
	}
	var (
		p        PoolView
		metaJSON []byte
	)
	err := r.pool.QueryRow(ctx, `
		SELECT p.id, p.name, p.pool_type, p.token_address, p.reward_token,
		       p.apy, p.tvl, p.chain_id, p.status,
		       p.starts_at, p.ends_at, p.metadata, p.created_at, p.updated_at,
		       COALESCE((SELECT COUNT(*)::bigint FROM user_stakes WHERE pool_id = p.id), 0) AS stake_count
		FROM staking_pools p
		WHERE p.id = $1
	`, id).Scan(
		&p.ID, &p.Name, &p.PoolType, &p.TokenAddress, &p.RewardToken,
		&p.APY, &p.TVL, &p.ChainID, &p.Status,
		&p.StartsAt, &p.EndsAt, &metaJSON, &p.CreatedAt, &p.UpdatedAt,
		&p.StakeCount,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return PoolView{}, ErrPoolNotFound
	}
	if err != nil {
		return PoolView{}, fmt.Errorf("query staking_pools (by id): %w", err)
	}
	if len(metaJSON) > 0 {
		p.Metadata = metaJSON
	}
	return p, nil
}

// CreatePool inserts a staking_pools row. Returns the inserted row.
func (r *Repository) CreatePool(ctx context.Context, in CreatePoolInput) (PoolView, error) {
	if r.pool == nil {
		return PoolView{}, ErrPoolUnavailable
	}
	var (
		p        PoolView
		metaJSON []byte
	)
	err := r.pool.QueryRow(ctx, `
		INSERT INTO staking_pools
			(id, name, chain_id, pool_type, token_address, reward_token, apy, tvl,
			 status, starts_at, ends_at, metadata, created_at, updated_at)
		VALUES
			(gen_random_uuid()::text, $1, $2, $3, $4, $5, $6, $7,
			 'active', $8, $9, $10::jsonb, NOW(), NOW())
		RETURNING id, name, pool_type, token_address, reward_token, apy, tvl,
		          chain_id, status, starts_at, ends_at, metadata, created_at, updated_at
	`, in.Name, in.ChainID, in.PoolType, in.TokenAddress, in.RewardToken,
		in.APY, in.TVL, in.StartsAt, in.EndsAt, nullableJSON(in.Metadata),
	).Scan(
		&p.ID, &p.Name, &p.PoolType, &p.TokenAddress, &p.RewardToken,
		&p.APY, &p.TVL, &p.ChainID, &p.Status,
		&p.StartsAt, &p.EndsAt, &metaJSON, &p.CreatedAt, &p.UpdatedAt,
	)
	if err != nil {
		return PoolView{}, fmt.Errorf("insert staking_pool: %w", err)
	}
	if len(metaJSON) > 0 {
		p.Metadata = metaJSON
	}
	return p, nil
}

// UpdatePool mirrors Prisma's Partial<...> update — fields nil → leave
// unchanged. Returns the updated row or ErrPoolNotFound.
func (r *Repository) UpdatePool(ctx context.Context, id string, in UpdatePoolInput) (PoolView, error) {
	if r.pool == nil {
		return PoolView{}, ErrPoolUnavailable
	}

	sets := []string{}
	args := []any{}
	add := func(clause string, value any) {
		args = append(args, value)
		sets = append(sets, fmt.Sprintf(clause, len(args)))
	}
	if in.Name != nil {
		add("name = $%d", *in.Name)
	}
	if in.APY != nil {
		add("apy = $%d", *in.APY)
	}
	if in.TVL != nil {
		add("tvl = $%d", *in.TVL)
	}
	if in.Status != nil {
		add("status = $%d", *in.Status)
	}
	if in.Metadata != nil {
		add("metadata = $%d::jsonb", string(in.Metadata))
	}
	if len(sets) == 0 {
		// No-op; just fetch and return the row so the caller sees the
		// current state (matches Prisma's behavior of always returning).
		return r.FindPool(ctx, id)
	}
	sets = append(sets, "updated_at = NOW()")
	args = append(args, id)

	query := fmt.Sprintf(`
		UPDATE staking_pools
		SET %s
		WHERE id = $%d
		RETURNING id, name, pool_type, token_address, reward_token, apy, tvl,
		          chain_id, status, starts_at, ends_at, metadata, created_at, updated_at
	`, strings.Join(sets, ", "), len(args))

	var (
		p        PoolView
		metaJSON []byte
	)
	err := r.pool.QueryRow(ctx, query, args...).Scan(
		&p.ID, &p.Name, &p.PoolType, &p.TokenAddress, &p.RewardToken,
		&p.APY, &p.TVL, &p.ChainID, &p.Status,
		&p.StartsAt, &p.EndsAt, &metaJSON, &p.CreatedAt, &p.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return PoolView{}, ErrPoolNotFound
	}
	if err != nil {
		return PoolView{}, fmt.Errorf("update staking_pool: %w", err)
	}
	if len(metaJSON) > 0 {
		p.Metadata = metaJSON
	}
	return p, nil
}

// GetPoolStats — totalPools/totalTvl/highestApy over status='active'.
func (r *Repository) GetPoolStats(ctx context.Context) (PoolStats, error) {
	if r.pool == nil {
		return PoolStats{}, ErrPoolUnavailable
	}
	var (
		stats    PoolStats
		totalTvl *float64
		maxApy   *float64
	)
	if err := r.pool.QueryRow(ctx, `
		SELECT COUNT(*)::bigint, COALESCE(SUM(tvl), 0), COALESCE(MAX(apy), 0)
		FROM staking_pools
		WHERE status = 'active'
	`).Scan(&stats.TotalPools, &totalTvl, &maxApy); err != nil {
		return PoolStats{}, fmt.Errorf("aggregate staking_pools: %w", err)
	}
	if totalTvl != nil {
		stats.TotalTvl = *totalTvl
	}
	if maxApy != nil {
		stats.HighestApy = *maxApy
	}
	return stats, nil
}

// UserStake is a user_stakes row joined with pool fields.
type UserStake struct {
	ID             string     `json:"id"`
	UserAddress    string     `json:"userAddress,omitempty"`
	PoolID         string     `json:"poolId,omitempty"`
	Amount         string     `json:"amount"`
	RewardsClaimed string     `json:"rewardsClaimed,omitempty"`
	StakedAt       time.Time  `json:"stakedAt"`
	UnstakedAt     *time.Time `json:"unstakedAt"`
	// Joined pool fields. Portfolio's caller uses these directly.
	PoolName      string `json:"poolName,omitempty"`
	PoolChainID   int    `json:"poolChainId,omitempty"`
	PoolTokenAddr string `json:"poolTokenAddress,omitempty"`
	// Pool nests the full pool view when an HTTP caller needs it
	// (NestJS returns `include: { pool: true }`).
	Pool *PoolView `json:"pool,omitempty"`
}

// ListUserStakes — every stake by address, joined with pool fields.
// Used by portfolio (which wants both active and unstaked rows).
func (r *Repository) ListUserStakes(ctx context.Context, userAddress string) ([]UserStake, error) {
	if r.pool == nil {
		return nil, ErrPoolUnavailable
	}
	rows, err := r.pool.Query(ctx, `
		SELECT s.id, s.amount::text, s.staked_at, s.unstaked_at,
		       p.name, p.chain_id, p.token_address
		FROM user_stakes s
		JOIN staking_pools p ON p.id = s.pool_id
		WHERE s.user_address = $1
		ORDER BY s.staked_at DESC
	`, userAddress)
	if err != nil {
		return nil, fmt.Errorf("query user_stakes: %w", err)
	}
	defer rows.Close()
	out := make([]UserStake, 0)
	for rows.Next() {
		var s UserStake
		if err := rows.Scan(&s.ID, &s.Amount, &s.StakedAt, &s.UnstakedAt,
			&s.PoolName, &s.PoolChainID, &s.PoolTokenAddr); err != nil {
			return nil, fmt.Errorf("scan user_stakes: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate user_stakes: %w", err)
	}
	return out, nil
}

// ListActiveUserStakes mirrors getUserStakes — filters unstakedAt IS NULL
// AND includes the full PoolView projection (matches NestJS's
// `include: { pool: true }`).
func (r *Repository) ListActiveUserStakes(ctx context.Context, userAddress string) ([]UserStake, error) {
	if r.pool == nil {
		return nil, ErrPoolUnavailable
	}
	rows, err := r.pool.Query(ctx, `
		SELECT s.id, s.user_address, s.pool_id, s.amount::text,
		       s.rewards_claimed::text, s.staked_at, s.unstaked_at,
		       p.id, p.name, p.pool_type, p.token_address, p.reward_token,
		       p.apy, p.tvl, p.chain_id, p.status
		FROM user_stakes s
		JOIN staking_pools p ON p.id = s.pool_id
		WHERE s.user_address = $1 AND s.unstaked_at IS NULL
		ORDER BY s.staked_at DESC
	`, userAddress)
	if err != nil {
		return nil, fmt.Errorf("query active user_stakes: %w", err)
	}
	defer rows.Close()
	out := make([]UserStake, 0)
	for rows.Next() {
		var (
			s    UserStake
			pool PoolView
		)
		if err := rows.Scan(
			&s.ID, &s.UserAddress, &s.PoolID, &s.Amount,
			&s.RewardsClaimed, &s.StakedAt, &s.UnstakedAt,
			&pool.ID, &pool.Name, &pool.PoolType, &pool.TokenAddress, &pool.RewardToken,
			&pool.APY, &pool.TVL, &pool.ChainID, &pool.Status,
		); err != nil {
			return nil, fmt.Errorf("scan active user_stakes: %w", err)
		}
		s.Pool = &pool
		s.PoolName = pool.Name
		s.PoolChainID = pool.ChainID
		s.PoolTokenAddr = pool.TokenAddress
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active user_stakes: %w", err)
	}
	return out, nil
}

// RecordStake inserts a user_stakes row. Amount is shifted to a string
// via fmt.Sprintf so Decimal(36,18) preserves precision.
func (r *Repository) RecordStake(ctx context.Context, in RecordStakeInput) (UserStake, error) {
	if r.pool == nil {
		return UserStake{}, ErrPoolUnavailable
	}
	amountStr := formatDecimal(in.Amount)
	var s UserStake
	err := r.pool.QueryRow(ctx, `
		INSERT INTO user_stakes (id, user_address, pool_id, amount, staked_at)
		VALUES (gen_random_uuid()::text, $1, $2, $3::numeric, NOW())
		RETURNING id, user_address, pool_id, amount::text, rewards_claimed::text,
		          staked_at, unstaked_at
	`, in.UserAddress, in.PoolID, amountStr).Scan(
		&s.ID, &s.UserAddress, &s.PoolID, &s.Amount,
		&s.RewardsClaimed, &s.StakedAt, &s.UnstakedAt,
	)
	if err != nil {
		return UserStake{}, fmt.Errorf("insert user_stake: %w", err)
	}
	return s, nil
}

// RecordUnstake updates only an active stake owned by the authenticated wallet.
func (r *Repository) RecordUnstake(ctx context.Context, stakeID, ownerAddress string) (UserStake, error) {
	if r.pool == nil {
		return UserStake{}, ErrPoolUnavailable
	}
	var s UserStake
	err := r.pool.QueryRow(ctx, `
		UPDATE user_stakes
		SET unstaked_at = NOW()
		WHERE id = $1
		  AND LOWER(user_address) = LOWER($2)
		  AND unstaked_at IS NULL
		RETURNING id, user_address, pool_id, amount::text, rewards_claimed::text,
		          staked_at, unstaked_at
	`, stakeID, ownerAddress).Scan(
		&s.ID, &s.UserAddress, &s.PoolID, &s.Amount,
		&s.RewardsClaimed, &s.StakedAt, &s.UnstakedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return UserStake{}, ErrPoolNotFound
	}
	if err != nil {
		return UserStake{}, fmt.Errorf("update user_stake (unstake): %w", err)
	}
	return s, nil
}

// formatDecimal renders a float as a fixed-point string. We pick a
// safe precision (12 digits after the decimal — well under 18) so
// recordStake's payload survives the float64 → Decimal(36,18) trip.
func formatDecimal(v float64) string {
	return fmt.Sprintf("%.12f", v)
}

func nullableJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return string(raw)
}
