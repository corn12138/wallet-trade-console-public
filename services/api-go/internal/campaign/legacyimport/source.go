package legacyimport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"

	"github.com/jackc/pgx/v5"
)

// sourceTables are the legacy tables in source-digest order. The order and the
// per-row line format match inventory, so a capture's source digest equals the
// read-only inventory's digest for the same snapshot.
var sourceTables = []string{"launchpad_campaigns", "campaign_participants", "campaign_reminders"}

// pageSize bounds how many legacy rows are held in memory at once.
const pageSize = 500

type sourceRow struct {
	table, id, campaignID, raw, digest string
}

// line is the source-digest record of one row.
func (r sourceRow) line() string { return fmt.Sprintf("%s\x00%s\x00%s\n", r.table, r.id, r.digest) }

// scanSource reads one legacy table in source order, a page at a time, within
// the caller's snapshot, and hands each page to visit. Keyset pagination keeps
// memory bounded and lets visit use the same transaction between pages.
func scanSource(ctx context.Context, tx pgx.Tx, table string, visit func([]sourceRow) error) error {
	var query string
	campaignKeyed := table != "launchpad_campaigns"
	switch table {
	case "launchpad_campaigns":
		query = `SELECT t.id, t.id, row_to_json(t)::text FROM launchpad_campaigns t
			WHERE $1::text IS NULL OR t.id > $1::text ORDER BY t.id LIMIT $2`
	case "campaign_participants", "campaign_reminders":
		query = `SELECT t.id, t.campaign_id, row_to_json(t)::text FROM ` + table + ` t
			WHERE $1::text IS NULL OR (t.campaign_id, t.id) > ($1::text, $2::text) ORDER BY t.campaign_id, t.id LIMIT $3`
	default:
		return fmt.Errorf("unknown legacy table %q", table)
	}
	var afterCampaign, afterID *string
	for {
		args := []any{afterID, pageSize}
		if campaignKeyed {
			args = []any{afterCampaign, afterID, pageSize}
		}
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return err
		}
		page := make([]sourceRow, 0, pageSize)
		for rows.Next() {
			row := sourceRow{table: table}
			if err = rows.Scan(&row.id, &row.campaignID, &row.raw); err != nil {
				rows.Close()
				return err
			}
			page = append(page, row)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return err
		}
		if len(page) == 0 {
			return nil
		}
		for i := range page {
			if page[i].digest, err = canonicalRow([]byte(page[i].raw)); err != nil {
				return fmt.Errorf("%s row: %w", table, err)
			}
		}
		if err = visit(page); err != nil {
			return err
		}
		last := page[len(page)-1]
		afterCampaign, afterID = &last.campaignID, &last.id
		if len(page) < pageSize {
			return nil
		}
	}
}

// interactionDigests accumulates, per campaign, the digest lines of its
// participant then reminder rows in source order.
type interactionDigests map[string]hash.Hash

func (d interactionDigests) add(row sourceRow) {
	h, ok := d[row.campaignID]
	if !ok {
		h = sha256.New()
		d[row.campaignID] = h
	}
	_, _ = h.Write([]byte(row.line()))
}

// sum returns the campaign's interaction digest; a campaign without
// interactions hashes the empty input.
func (d interactionDigests) sum(campaignID string) string {
	if h, ok := d[campaignID]; ok {
		return hex.EncodeToString(h.Sum(nil))
	}
	empty := sha256.Sum256(nil)
	return hex.EncodeToString(empty[:])
}
