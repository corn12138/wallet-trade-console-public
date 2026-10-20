// Package legacyimport inventories unversioned launchpad campaign data before
// any baseline import. Inventory is strictly read-only: it runs in a database
// READ ONLY transaction, never repairs or deletes conflicting history, and
// reports every anomaly with enough evidence (row digests, capture snapshot)
// for an operator to decide and later prove what the import actually saw.
package legacyimport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Severity orders what an operator must do: a blocker prevents importing the
// affected campaign; review needs a recorded human decision; info is retained
// context that does not change the import plan.
const (
	SeverityBlocker = "blocker"
	SeverityReview  = "review"
	SeverityInfo    = "info"
)

type Finding struct {
	Kind       string `json:"kind"`
	Severity   string `json:"severity"`
	CampaignID string `json:"campaignId"`
	// WalletRef is a truncated SHA-256 of the lowercased wallet. Reports are
	// shared for review, so participation is never listed by raw address.
	WalletRef string `json:"walletRef,omitempty"`
	RowRef    string `json:"rowRef,omitempty"`
	Detail    string `json:"detail"`
}

type Campaign struct {
	ID                   string   `json:"id"`
	LegacyRowSHA256      string   `json:"legacyRowSha256"`
	VersionedSource      *string  `json:"versionedSource"`
	Status               string   `json:"status"`
	StartAt              string   `json:"startAt"`
	EndAt                string   `json:"endAt"`
	ParticipantsCounter  int64    `json:"participantsCounter"`
	ParticipantRows      int64    `json:"participantRows"`
	DistinctParticipants int64    `json:"distinctParticipants"`
	ReminderRows         int64    `json:"reminderRows"`
	DistinctReminders    int64    `json:"distinctReminders"`
	HasDescription       bool     `json:"hasDescription"`
	HasBanner            bool     `json:"hasBanner"`
	HasReward            bool     `json:"hasReward"`
	MetadataKeys         []string `json:"metadataKeys"`
	Classification       string   `json:"classification"`
}

type Totals struct {
	Campaigns          int `json:"campaigns"`
	AlreadyVersioned   int `json:"alreadyVersioned"`
	Unversioned        int `json:"unversioned"`
	Blocked            int `json:"blocked"`
	ReviewRequired     int `json:"reviewRequired"`
	Importable         int `json:"importable"`
	ParticipantRows    int `json:"participantRows"`
	ReminderRows       int `json:"reminderRows"`
	OrphanParticipants int `json:"orphanParticipants"`
	OrphanReminders    int `json:"orphanReminders"`
}

// Report is the captured inventory. CapturedAt and Snapshot identify the MVCC
// view it was read from; SourceSHA256 and the per-row digests are what a later
// import re-verifies (see Verify), because the snapshot string itself is not a
// backup and cannot be re-read after the transaction ends.
type Report struct {
	SchemaVersion   int        `json:"schemaVersion"`
	DigestAlgorithm string     `json:"digestAlgorithm"`
	CapturedAt      string     `json:"capturedAt"`
	Snapshot        string     `json:"snapshot"`
	ServerVersion   string     `json:"serverVersion"`
	TimeBasis       string     `json:"timeBasis"`
	Totals          Totals     `json:"totals"`
	Campaigns       []Campaign `json:"campaigns"`
	Findings        []Finding  `json:"findings"`
	SourceSHA256    string     `json:"sourceSha256"`
}

// Run captures one consistent read-only snapshot. The transaction is declared
// READ ONLY so PostgreSQL itself rejects any write that a future edit might add.
func Run(ctx context.Context, pool *pgxpool.Pool) (Report, error) {
	report := Report{SchemaVersion: 2, DigestAlgorithm: DigestAlgorithm, Campaigns: []Campaign{}, Findings: []Finding{},
		TimeBasis: "legacy columns are TIMESTAMP(3) without time zone; read as UTC per the Prisma writer convention, which the database cannot prove"}
	if pool == nil {
		return report, errors.New("database pool required")
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return report, err
	}
	defer tx.Rollback(context.Background())
	var captured time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp(), pg_current_snapshot()::text, current_setting('server_version_num')`).Scan(&captured, &report.Snapshot, &report.ServerVersion); err != nil {
		return report, err
	}
	report.CapturedAt = captured.UTC().Format(time.RFC3339Nano)
	source := sha256.New()
	if err = inventoryCampaigns(ctx, tx, captured, &report, source); err != nil {
		return report, err
	}
	for _, table := range []string{"campaign_participants", "campaign_reminders"} {
		if err = inventoryInteractions(ctx, tx, table, &report, source); err != nil {
			return report, err
		}
	}
	classify(&report)
	report.SourceSHA256 = hex.EncodeToString(source.Sum(nil))
	return report, tx.Commit(ctx)
}

func walletRef(wallet string) string {
	sum := sha256.Sum256([]byte(lowerASCII(wallet)))
	return hex.EncodeToString(sum[:8])
}

func lowerASCII(value string) string {
	out := []byte(value)
	for i, ch := range out {
		if ch >= 'A' && ch <= 'Z' {
			out[i] = ch + 32
		}
	}
	return string(out)
}

func classify(report *Report) {
	worst := map[string]string{}
	rank := map[string]int{"": 0, SeverityInfo: 1, SeverityReview: 2, SeverityBlocker: 3}
	for _, finding := range report.Findings {
		if rank[finding.Severity] > rank[worst[finding.CampaignID]] {
			worst[finding.CampaignID] = finding.Severity
		}
	}
	totals := &report.Totals
	totals.Campaigns = len(report.Campaigns)
	for i := range report.Campaigns {
		c := &report.Campaigns[i]
		switch {
		case c.VersionedSource != nil:
			c.Classification = "already-versioned"
			totals.AlreadyVersioned++
			continue
		case worst[c.ID] == SeverityBlocker:
			c.Classification = "blocked"
			totals.Blocked++
		case worst[c.ID] == SeverityReview:
			c.Classification = "review-required"
			totals.ReviewRequired++
		default:
			c.Classification = "importable"
			totals.Importable++
		}
		totals.Unversioned++
	}
	sort.SliceStable(report.Findings, func(i, j int) bool {
		a, b := report.Findings[i], report.Findings[j]
		if a.CampaignID != b.CampaignID {
			return a.CampaignID < b.CampaignID
		}
		return a.Kind+a.RowRef < b.Kind+b.RowRef
	})
}

// DigestAlgorithm names the row canonicalization. Version 1 parsed numbers as
// float64, so distinct stored values such as 9007199254740992 and
// 9007199254740993 produced one digest; its reports can never authorize an
// import, and Verify refuses any report made with a different algorithm.
//
// Version 2 equivalence policy: a digest covers exactly what PostgreSQL stores
// and renders. Numbers keep the literal PostgreSQL rendered (no float
// conversion), so every distinction jsonb preserves (precision, scale such as
// 1.0 versus 1, sign, magnitude) yields a different digest. Distinctions jsonb
// does not store (exponent spelling, key order, whitespace, duplicate keys) are
// already gone before any archive could see them, so they cannot be told apart.
// Object keys are sorted, strings must be valid UTF-8 (never replaced), and
// trailing data is rejected.
const DigestAlgorithm = "legacy-row-json/v2"

// canonicalRow hashes a row rendered by row_to_json under DigestAlgorithm.
func canonicalRow(raw []byte) (string, error) {
	if !utf8.Valid(raw) {
		return "", errors.New("row is not valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return "", errors.New("trailing data after row")
	}
	// encoding/json writes json.Number verbatim and sorts map keys.
	canonical, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}
