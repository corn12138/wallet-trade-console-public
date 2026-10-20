package campaign

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"strings"
	"time"
)

// These repository operations remain for internal compatibility. HTTP routes
// cannot call them: versioned commands own all externally requested mutations.
// Their timestamps use NOW() AT TIME ZONE 'UTC' because the legacy columns are
// TIMESTAMP without time zone holding UTC; a bare NOW() would store the
// session-local wall clock on any non-UTC database session.
const allColumns = `id, title, description, banner, start_date, end_date, status, reward,
 participants, metadata, created_at, updated_at`

type CreateInput struct {
	Title       string
	Description *string
	Banner      *string
	StartDate   time.Time
	EndDate     time.Time
	Reward      *string
}

// Create inserts a campaign and returns the stored row. Like the sibling
// write modules (staking, papertrade, users, article) it generates the id
// with gen_random_uuid()::text and sets created_at/updated_at to UTC NOW() —
// Prisma's @default(cuid()) / @default(now()) / @updatedAt are client-side,
// not DB defaults.
func (r *Repository) Create(ctx context.Context, in CreateInput) (Campaign, error) {
	if r == nil || r.pool == nil {
		return Campaign{}, ErrPoolUnavailable
	}
	var c Campaign
	var metadata []byte
	err := r.pool.QueryRow(ctx, `
		INSERT INTO launchpad_campaigns
			(id, title, description, banner, start_date, end_date, status, reward, participants, metadata, created_at, updated_at)
		VALUES
			(gen_random_uuid()::text, $1, $2, $3, $4, $5, 'upcoming', $6, 0, NULL, NOW() AT TIME ZONE 'UTC', NOW() AT TIME ZONE 'UTC')
		RETURNING `+allColumns,
		in.Title, in.Description, in.Banner, in.StartDate, in.EndDate, in.Reward).
		Scan(&c.ID, &c.Title, &c.Description, &c.Banner, &c.StartDate, &c.EndDate, &c.Status, &c.Reward,
			&c.Participants, &metadata, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return Campaign{}, fmt.Errorf("insert launchpad_campaigns: %w", err)
	}
	c.Metadata = decodeMetadata(metadata)
	return c, nil
}

// Join records participation. Idempotent; reports whether the row is new.
func (r *Repository) Join(ctx context.Context, campaignID, wallet string) (bool, error) {
	if r == nil || r.pool == nil {
		return false, ErrPoolUnavailable
	}
	if err := r.assertCampaignExists(ctx, campaignID); err != nil {
		return false, err
	}
	tag, err := r.pool.Exec(ctx, `
		INSERT INTO campaign_participants (id, campaign_id, wallet_address, joined_at)
		VALUES (gen_random_uuid()::text, $1, $2, NOW() AT TIME ZONE 'UTC')
		ON CONFLICT (campaign_id, wallet_address) DO NOTHING
	`, campaignID, strings.ToLower(wallet))
	if err != nil {
		return false, fmt.Errorf("insert campaign_participant: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// Leave removes participation (idempotent).
func (r *Repository) Leave(ctx context.Context, campaignID, wallet string) error {
	if r == nil || r.pool == nil {
		return ErrPoolUnavailable
	}
	_, err := r.pool.Exec(ctx, `
		DELETE FROM campaign_participants WHERE campaign_id = $1 AND wallet_address = $2
	`, campaignID, strings.ToLower(wallet))
	if err != nil {
		return fmt.Errorf("delete campaign_participant: %w", err)
	}
	return nil
}

// SetReminder stores reminder intent. Idempotent; reports newness.
func (r *Repository) SetReminder(ctx context.Context, campaignID, wallet string) (bool, error) {
	if r == nil || r.pool == nil {
		return false, ErrPoolUnavailable
	}
	if err := r.assertCampaignExists(ctx, campaignID); err != nil {
		return false, err
	}
	tag, err := r.pool.Exec(ctx, `
		INSERT INTO campaign_reminders (id, campaign_id, wallet_address, created_at)
		VALUES (gen_random_uuid()::text, $1, $2, NOW() AT TIME ZONE 'UTC')
		ON CONFLICT (campaign_id, wallet_address) DO NOTHING
	`, campaignID, strings.ToLower(wallet))
	if err != nil {
		return false, fmt.Errorf("insert campaign_reminder: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// RemoveReminder deletes reminder intent (idempotent).
func (r *Repository) RemoveReminder(ctx context.Context, campaignID, wallet string) error {
	if r == nil || r.pool == nil {
		return ErrPoolUnavailable
	}
	_, err := r.pool.Exec(ctx, `
		DELETE FROM campaign_reminders WHERE campaign_id = $1 AND wallet_address = $2
	`, campaignID, strings.ToLower(wallet))
	if err != nil {
		return fmt.Errorf("delete campaign_reminder: %w", err)
	}
	return nil
}

func (r *Repository) assertCampaignExists(ctx context.Context, campaignID string) error {
	var one int
	err := r.pool.QueryRow(ctx, `SELECT 1 FROM launchpad_campaigns c WHERE c.id = $1 AND NOT EXISTS (SELECT 1 FROM campaign_definitions d WHERE d.id = c.id)`, campaignID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("check campaign %s: %w", campaignID, err)
	}
	return nil
}
