package legacyimport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RestoreReport proves what the archive of one run can and cannot restore.
// Row references are digest prefixes; no raw value leaves the database.
type RestoreReport struct {
	RunID string `json:"runId"`
	Rows  int64  `json:"rows"`
	// DigestMismatches lists archived rows whose stored JSON no longer hashes
	// to their recorded digest.
	DigestMismatches []string `json:"digestMismatches"`
	// SourceDigestMatches is true when the archive, read in capture order,
	// reproduces the run's recorded source digest.
	SourceDigestMatches bool `json:"sourceDigestMatches"`
	// RestoreMismatches lists rows that do not survive a restore rehearsal:
	// the JSON rebuilt into the original table's row type and rendered again
	// must hash to the recorded digest.
	RestoreMismatches []string `json:"restoreMismatches"`
	// Live* compare the archive with the current legacy rows. Drift is
	// reported, not an archive failure: unfenced legacy data may change.
	LiveUnchanged int64 `json:"liveUnchanged"`
	LiveChanged   int64 `json:"liveChanged"`
	LiveRemoved   int64 `json:"liveRemoved"`
	LiveAdded     int64 `json:"liveAdded"`
}

// Restorable reports whether the archive is intact and exactly restorable.
func (r RestoreReport) Restorable() bool {
	return r.Rows > 0 && r.SourceDigestMatches && len(r.DigestMismatches) == 0 && len(r.RestoreMismatches) == 0
}

// RestoreCheck re-hashes every archived row of a captured run, rebuilds each
// one into its original table's row type to prove it restores exactly, and
// compares the archive with the current legacy rows.
func RestoreCheck(ctx context.Context, pool *pgxpool.Pool, runID string) (RestoreReport, error) {
	report := RestoreReport{RunID: runID, DigestMismatches: []string{}, RestoreMismatches: []string{}}
	err := pgx.BeginTxFunc(ctx, pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var status string
		var recorded *string
		if err := tx.QueryRow(ctx, `SELECT status, source_sha256 FROM campaign_legacy_import_runs WHERE id=$1`, runID).Scan(&status, &recorded); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return errors.New("run not found")
			}
			return err
		}
		if status != "captured" || recorded == nil {
			return fmt.Errorf("run is %s; only a captured run has an archive to check", status)
		}
		source := sha256.New()
		archived := map[string]string{}
		for _, table := range sourceTables {
			if err := scanArchive(ctx, tx, runID, table, func(row archivedRow) {
				report.Rows++
				ref := row.table + ":" + row.digest[:16]
				if digest, err := canonicalRow([]byte(row.raw)); err != nil || digest != row.digest {
					report.DigestMismatches = append(report.DigestMismatches, ref)
				}
				if digest, err := canonicalRow([]byte(row.restored)); err != nil || digest != row.digest {
					report.RestoreMismatches = append(report.RestoreMismatches, ref)
				}
				_, _ = source.Write([]byte(sourceRow{table: row.table, id: row.id, digest: row.digest}.line()))
				archived[row.table+"\x00"+row.id] = row.digest
			}); err != nil {
				return err
			}
		}
		report.SourceDigestMatches = hex.EncodeToString(source.Sum(nil)) == *recorded
		seen := map[string]bool{}
		for _, table := range sourceTables {
			if err := scanSource(ctx, tx, table, func(page []sourceRow) error {
				for _, row := range page {
					key := row.table + "\x00" + row.id
					seen[key] = true
					switch digest, ok := archived[key]; {
					case !ok:
						report.LiveAdded++
					case digest != row.digest:
						report.LiveChanged++
					default:
						report.LiveUnchanged++
					}
				}
				return nil
			}); err != nil {
				return err
			}
		}
		for key := range archived {
			if !seen[key] {
				report.LiveRemoved++
			}
		}
		return nil
	})
	return report, err
}

type archivedRow struct {
	table, id, campaignID, raw, digest, restored string
}

// scanArchive reads a run's archive for one table in capture order, a page at
// a time, and renders each row again after rebuilding it into the original
// table's row type.
func scanArchive(ctx context.Context, tx pgx.Tx, runID, table string, visit func(archivedRow)) error {
	restore := `SELECT a.row_id, a.campaign_id, a.row_json, a.row_sha256,
		row_to_json(json_populate_record(NULL::` + pgx.Identifier{table}.Sanitize() + `, a.row_json::json))::text
		FROM campaign_legacy_archive_rows a WHERE a.run_id=$1 AND a.source_table=$2 AND `
	query := restore + `($3::text IS NULL OR (a.campaign_id, a.row_id) > ($3::text, $4::text)) ORDER BY a.campaign_id, a.row_id LIMIT $5`
	if table == "launchpad_campaigns" {
		query = restore + `($3::text IS NULL OR a.row_id > $3::text) ORDER BY a.row_id LIMIT $4`
	}
	var afterCampaign, afterID *string
	for {
		args := []any{runID, table, afterCampaign, afterID, pageSize}
		if table == "launchpad_campaigns" {
			args = []any{runID, table, afterID, pageSize}
		}
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return err
		}
		count := 0
		for rows.Next() {
			row := archivedRow{table: table}
			if err = rows.Scan(&row.id, &row.campaignID, &row.raw, &row.digest, &row.restored); err != nil {
				rows.Close()
				return err
			}
			visit(row)
			count++
			afterCampaign, afterID = &row.campaignID, &row.id
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return err
		}
		if count < pageSize {
			return nil
		}
	}
}
