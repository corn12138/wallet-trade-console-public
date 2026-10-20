package legacyimport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	ErrRunNotFound    = errors.New("run not found")
	ErrNoPlanItem     = errors.New("the run has no plan item for this campaign")
	ErrRunNotCaptured = errors.New("only a captured run can be activated")
)

// ArchivedRow is one row a captured run archived, exactly as captured.
type ArchivedRow struct {
	Table, ID, JSON, SHA256 string
}

// PlanItem is the captured plan's entry for one campaign.
type PlanItem struct {
	Decision                            string
	LegacyRowSHA256, InteractionsSHA256 string
	ParticipantRows, ReminderRows       int
	Findings                            []Finding
}

// ArchivedCampaign is what a captured run holds for one campaign.
type ArchivedCampaign struct {
	RunID, CampaignID string
	CapturedAt        time.Time
	Item              PlanItem
	Campaign          ArchivedRow
	Participants      []ArchivedRow
	Reminders         []ArchivedRow
}

// LoadArchived reads, inside the caller's transaction, a captured run's plan
// item and archived rows for one campaign and checks them against each other:
// the campaign row digest, the interaction digest (recomputed in capture
// order) and the row counts must all equal the plan's.
func LoadArchived(ctx context.Context, tx pgx.Tx, runID, campaignID string) (ArchivedCampaign, error) {
	out := ArchivedCampaign{RunID: runID, CampaignID: campaignID}
	var status string
	var captured *time.Time
	err := tx.QueryRow(ctx, `SELECT status, captured_at FROM campaign_legacy_import_runs WHERE id=$1`, runID).Scan(&status, &captured)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrRunNotFound
	}
	if err != nil {
		return out, err
	}
	if status != "captured" || captured == nil {
		return out, fmt.Errorf("run is %s: %w", status, ErrRunNotCaptured)
	}
	out.CapturedAt = captured.UTC()
	var findings []byte
	item := &out.Item
	err = tx.QueryRow(ctx, `SELECT decision, legacy_row_sha256, interactions_sha256, participant_rows, reminder_rows, findings
		FROM campaign_legacy_import_items WHERE run_id=$1 AND campaign_id=$2`, runID, campaignID).
		Scan(&item.Decision, &item.LegacyRowSHA256, &item.InteractionsSHA256, &item.ParticipantRows, &item.ReminderRows, &findings)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrNoPlanItem
	}
	if err != nil {
		return out, err
	}
	if err = json.Unmarshal(findings, &item.Findings); err != nil {
		return out, fmt.Errorf("plan item findings: %w", err)
	}
	// Table names sort participants, reminders, then the campaign row: the
	// capture's interaction order within one campaign.
	rows, err := tx.Query(ctx, `SELECT source_table, row_id, row_json, row_sha256 FROM campaign_legacy_archive_rows
		WHERE run_id=$1 AND campaign_id=$2 ORDER BY source_table, row_id`, runID, campaignID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	interactions := sha256.New()
	campaigns := 0
	for rows.Next() {
		var row ArchivedRow
		if err = rows.Scan(&row.Table, &row.ID, &row.JSON, &row.SHA256); err != nil {
			return out, err
		}
		if digest, err := canonicalRow([]byte(row.JSON)); err != nil || digest != row.SHA256 {
			return out, fmt.Errorf("archived %s row %s does not match its digest", row.Table, row.SHA256[:16])
		}
		switch row.Table {
		case "launchpad_campaigns":
			out.Campaign, campaigns = row, campaigns+1
		case "campaign_participants":
			out.Participants = append(out.Participants, row)
			_, _ = interactions.Write([]byte(sourceRow{table: row.Table, id: row.ID, digest: row.SHA256}.line()))
		case "campaign_reminders":
			out.Reminders = append(out.Reminders, row)
			_, _ = interactions.Write([]byte(sourceRow{table: row.Table, id: row.ID, digest: row.SHA256}.line()))
		}
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	switch {
	case campaigns != 1 || out.Campaign.ID != campaignID || out.Campaign.SHA256 != item.LegacyRowSHA256:
		return out, errors.New("the archived campaign row does not match the plan item")
	case hex.EncodeToString(interactions.Sum(nil)) != item.InteractionsSHA256:
		return out, errors.New("the archived interactions do not match the plan item")
	case len(out.Participants) != item.ParticipantRows || len(out.Reminders) != item.ReminderRows:
		return out, errors.New("the archived interaction counts do not match the plan item")
	}
	return out, nil
}

// WalletRef is the truncated SHA-256 of a lowercased wallet, the form reports
// use instead of raw addresses.
func WalletRef(wallet string) string { return walletRef(wallet) }
