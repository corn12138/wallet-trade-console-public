package legacyimport

import (
	"context"
	"fmt"
	"hash"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var walletPattern = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)

const campaignSQL = `SELECT c.id, row_to_json(c)::text, d.source, c.status, c.start_date, c.end_date, c.participants,
 coalesce(btrim(c.description),'')<>'', coalesce(btrim(c.banner),'')<>'', coalesce(btrim(c.reward),'')<>'',
 jsonb_typeof(c.metadata),
 CASE WHEN jsonb_typeof(c.metadata)='object' THEN ARRAY(SELECT jsonb_object_keys(c.metadata) ORDER BY 1) ELSE '{}'::text[] END,
 (SELECT count(*) FROM campaign_participants p WHERE p.campaign_id=c.id),
 (SELECT count(DISTINCT lower(p.wallet_address)) FROM campaign_participants p WHERE p.campaign_id=c.id),
 (SELECT count(*) FROM campaign_reminders r WHERE r.campaign_id=c.id),
 (SELECT count(DISTINCT lower(r.wallet_address)) FROM campaign_reminders r WHERE r.campaign_id=c.id)
 FROM launchpad_campaigns c LEFT JOIN campaign_definitions d ON d.id=c.id ORDER BY c.id`

func inventoryCampaigns(ctx context.Context, tx pgx.Tx, captured time.Time, report *Report, source hash.Hash) error {
	rows, err := tx.Query(ctx, campaignSQL)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var c Campaign
		var raw string
		var metadataType *string
		var start, end time.Time
		if err = rows.Scan(&c.ID, &raw, &c.VersionedSource, &c.Status, &start, &end, &c.ParticipantsCounter, &c.HasDescription, &c.HasBanner, &c.HasReward, &metadataType, &c.MetadataKeys, &c.ParticipantRows, &c.DistinctParticipants, &c.ReminderRows, &c.DistinctReminders); err != nil {
			return err
		}
		if c.LegacyRowSHA256, err = canonicalRow([]byte(raw)); err != nil {
			return err
		}
		fmt.Fprintf(source, "launchpad_campaigns\x00%s\x00%s\n", c.ID, c.LegacyRowSHA256)
		c.StartAt, c.EndAt = start.UTC().Format(time.RFC3339Nano), end.UTC().Format(time.RFC3339Nano)
		if c.MetadataKeys == nil {
			c.MetadataKeys = []string{}
		}
		report.Campaigns = append(report.Campaigns, c)
		if c.VersionedSource == nil {
			campaignFindings(report, c, start, end, captured, metadataType)
		}
	}
	return rows.Err()
}

func campaignFindings(report *Report, c Campaign, start, end, captured time.Time, metadataType *string) {
	add := func(kind, severity, detail string) {
		report.Findings = append(report.Findings, Finding{Kind: kind, Severity: severity, CampaignID: c.ID, Detail: detail})
	}
	if !end.After(start) {
		add("TIME_INVALID", SeverityBlocker, "end_date is not after start_date; no valid window can be derived")
	}
	derived := "active"
	if captured.Before(start) {
		derived = "upcoming"
	} else if !captured.Before(end) {
		derived = "ended"
	}
	switch c.Status {
	case "upcoming", "active", "ended":
		if c.Status != derived {
			add("STATUS_TIME_MISMATCH", SeverityInfo, fmt.Sprintf("stored status %q but window implies %q at capture; versioned reads derive phase from time", c.Status, derived))
		}
	default:
		add("STATUS_UNKNOWN", SeverityReview, fmt.Sprintf("stored status %q is outside upcoming/active/ended", c.Status))
	}
	if c.ParticipantsCounter != c.DistinctParticipants {
		add("COUNTER_MISMATCH", SeverityReview, fmt.Sprintf("participants counter %d differs from %d distinct participant wallets; neither is a verified count", c.ParticipantsCounter, c.DistinctParticipants))
	}
	if metadataType != nil && (*metadataType != "object" || len(c.MetadataKeys) > 0) {
		add("METADATA_UNMAPPED", SeverityReview, fmt.Sprintf("metadata (%s, keys %s) has no versioned mapping and may carry terms; it is kept verbatim only", *metadataType, strings.Join(c.MetadataKeys, ",")))
	}
	if c.HasBanner {
		add("MEDIA_UNREGISTERED", SeverityReview, "banner URL is not a registered media asset; it cannot be rendered as trusted media")
	}
	if c.HasReward {
		add("REWARD_TEXT", SeverityReview, "reward text is a material promise; it is preserved as legacy terms and never shown as a delivery commitment")
	}
	if c.ParticipantRows+c.ReminderRows > 0 {
		add("UNKNOWN_ACCEPTANCE", SeverityInfo, "existing participation or reminders have no accepted-terms evidence; they become legacyUnknown and lock these terms")
	}
	add("SINGLE_LANGUAGE_COPY", SeverityInfo, "legacy copy is single-language; a baseline keeps it verbatim and cannot become an editable bilingual revision")
}

// inventoryInteractions scans one legacy interaction table. Neither table has a
// foreign key to launchpad_campaigns, so orphans are possible and are reported,
// never dropped. The time column differs: joined_at versus created_at.
func inventoryInteractions(ctx context.Context, tx pgx.Tx, table string, report *Report, source hash.Hash) error {
	timeColumn, kind := "joined_at", "PARTICIPANT"
	if table == "campaign_reminders" {
		timeColumn, kind = "created_at", "REMINDER"
	}
	rows, err := tx.Query(ctx, `SELECT t.id, row_to_json(t)::text, t.campaign_id, t.wallet_address, t.`+timeColumn+`, c.id IS NOT NULL, c.start_date, c.end_date, d.source
		FROM `+table+` t LEFT JOIN launchpad_campaigns c ON c.id=t.campaign_id LEFT JOIN campaign_definitions d ON d.id=t.campaign_id ORDER BY t.campaign_id, t.id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	seen := map[string]string{}
	for rows.Next() {
		var id, raw, campaignID, wallet string
		var at time.Time
		var exists bool
		var start, end *time.Time
		var versioned *string
		if err = rows.Scan(&id, &raw, &campaignID, &wallet, &at, &exists, &start, &end, &versioned); err != nil {
			return err
		}
		digest, err := canonicalRow([]byte(raw))
		if err != nil {
			return err
		}
		fmt.Fprintf(source, "%s\x00%s\x00%s\n", table, id, digest)
		add := func(suffix, severity, detail string) {
			report.Findings = append(report.Findings, Finding{Kind: kind + "_" + suffix, Severity: severity, CampaignID: campaignID, WalletRef: walletRef(wallet), RowRef: digest[:16], Detail: detail})
		}
		if table == "campaign_participants" {
			report.Totals.ParticipantRows++
		} else {
			report.Totals.ReminderRows++
		}
		if !exists {
			add("ORPHAN", SeverityBlocker, "row references a campaign that does not exist; it is kept and excluded from import")
			if table == "campaign_participants" {
				report.Totals.OrphanParticipants++
			} else {
				report.Totals.OrphanReminders++
			}
			continue
		}
		if versioned != nil && *versioned == "created" {
			add("ON_VERSIONED_CAMPAIGN", SeverityReview, "legacy row targets a campaign created through versioned commands")
		}
		if !walletPattern.MatchString(wallet) || strings.Trim(strings.ToLower(wallet[min(2, len(wallet)):]), "0") == "" {
			add("WALLET_INVALID", SeverityBlocker, "wallet is not a non-zero 0x address")
			continue
		}
		if wallet != strings.ToLower(wallet) {
			add("WALLET_CASE_LOOKUP", SeverityReview, "wallet is stored with upper-case letters; the legacy API matched the lowercased caller exactly, so it never showed this row as the caller's; the import will")
		}
		key := campaignID + "\x00" + strings.ToLower(wallet)
		if previous, ok := seen[key]; ok {
			add("CASE_DUPLICATE", SeverityReview, "same wallet differs only by letter case from row "+previous+"; import must merge to one state keeping the earliest time")
		} else {
			seen[key] = digest[:16]
		}
		if end != nil && !at.Before(*end) {
			add("AFTER_END", SeverityInfo, "recorded at or after the campaign end")
		} else if table == "campaign_participants" && start != nil && at.Before(*start) {
			add("BEFORE_START", SeverityInfo, "joined before the campaign start")
		}
	}
	return rows.Err()
}
