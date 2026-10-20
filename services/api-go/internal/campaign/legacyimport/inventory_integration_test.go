//go:build integration

package legacyimport_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/legacyimport"
	"github.com/jackc/pgx/v5/pgxpool"
)

func legacyPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	config, err := pgxpool.ParseConfig(os.Getenv("CAMPAIGN_LEGACY_TEST_DATABASE_URL"))
	if err != nil || (!strings.HasPrefix(config.ConnConfig.Database, "campaign_test_legacy") && !strings.HasPrefix(config.ConnConfig.Database, "migration_probe_legacy")) {
		t.Fatal("inventory checks require CAMPAIGN_LEGACY_TEST_DATABASE_URL for a dedicated legacy test database")
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// tableDigest fingerprints every legacy and versioned campaign table, so the
// test proves the inventory changed nothing anywhere, not just in seeded rows.
func tableDigest(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var digest strings.Builder
	for _, table := range []string{"launchpad_campaigns", "campaign_participants", "campaign_reminders", "campaign_definitions", "campaign_revisions", "campaign_publications", "campaign_commands", "campaign_interaction_states"} {
		var value string
		if err := pool.QueryRow(context.Background(), `SELECT count(*)::text || ':' || coalesce(md5(string_agg(row_to_json(t)::text, '|' ORDER BY row_to_json(t)::text)), '') FROM `+table+` t`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		digest.WriteString(table + "=" + value + ";")
	}
	return digest.String()
}

func TestInventoryReportsAnomaliesWithoutWriting(t *testing.T) {
	pool := legacyPool(t)
	random := make([]byte, 6)
	rand.Read(random)
	p := "inventory-" + hex.EncodeToString(random)
	wallet := "0xAbCdEf0123456789abcdef0123456789ABCDEF01"
	seed := []string{
		// Clean ended campaign whose counter matches its single participant,
		// stored lowercase as the legacy API looked it up.
		`INSERT INTO launchpad_campaigns(id,title,start_date,end_date,status,participants,updated_at) VALUES ('` + p + `-clean','Clean','2020-01-01','2020-02-01','ended',1,now())`,
		`INSERT INTO campaign_participants(id,campaign_id,wallet_address,joined_at) VALUES ('` + p + `-p1','` + p + `-clean','` + strings.ToLower(wallet) + `','2020-01-15')`,
		// Every review/blocker condition on one campaign.
		`INSERT INTO launchpad_campaigns(id,title,banner,reward,start_date,end_date,status,participants,metadata,updated_at) VALUES ('` + p + `-messy','Messy','https://cdn.example/b.png','100 USDC pool','2020-03-01','2020-02-01','live',7,'{"terms":"extra"}',now())`,
		`INSERT INTO campaign_participants(id,campaign_id,wallet_address,joined_at) VALUES ('` + p + `-p2','` + p + `-messy','` + wallet + `','2020-01-15'),('` + p + `-p3','` + p + `-messy','` + strings.ToLower(wallet) + `','2020-01-16'),('` + p + `-p4','` + p + `-messy','not-a-wallet','2020-01-16')`,
		// Rows pointing at a campaign that never existed.
		`INSERT INTO campaign_participants(id,campaign_id,wallet_address) VALUES ('` + p + `-p5','` + p + `-missing','` + wallet + `')`,
		`INSERT INTO campaign_reminders(id,campaign_id,wallet_address) VALUES ('` + p + `-r1','` + p + `-missing','` + wallet + `')`,
	}
	// Legacy tables are not append-only. Seeded rows are removed afterwards so
	// deliberately malformed fixtures cannot leak into other legacy read tests.
	t.Cleanup(func() {
		for _, table := range []string{"campaign_participants", "campaign_reminders", "launchpad_campaigns"} {
			if _, err := pool.Exec(context.Background(), `DELETE FROM `+table+` WHERE id LIKE $1`, p+"-%"); err != nil {
				t.Errorf("cleanup %s: %v", table, err)
			}
		}
	})
	for _, sql := range seed {
		if _, err := pool.Exec(context.Background(), sql); err != nil {
			t.Fatal(err)
		}
	}
	before := tableDigest(t, pool)
	report, err := legacyimport.Run(context.Background(), pool)
	if err != nil {
		t.Fatal(err)
	}
	if after := tableDigest(t, pool); after != before {
		t.Fatal("inventory modified the database")
	}
	again, err := legacyimport.Run(context.Background(), pool)
	if err != nil || again.SourceSHA256 != report.SourceSHA256 || again.Snapshot == "" {
		t.Fatal("unchanged source must produce the same source digest")
	}
	kinds := map[string]map[string]string{}
	for _, f := range report.Findings {
		if strings.HasPrefix(f.CampaignID, p) {
			if kinds[f.CampaignID] == nil {
				kinds[f.CampaignID] = map[string]string{}
			}
			kinds[f.CampaignID][f.Kind] = f.Severity
		}
	}
	for kind, severity := range map[string]string{"TIME_INVALID": "blocker", "STATUS_UNKNOWN": "review", "COUNTER_MISMATCH": "review", "METADATA_UNMAPPED": "review", "MEDIA_UNREGISTERED": "review", "REWARD_TEXT": "review", "PARTICIPANT_CASE_DUPLICATE": "review", "PARTICIPANT_WALLET_CASE_LOOKUP": "review", "PARTICIPANT_WALLET_INVALID": "blocker"} {
		if kinds[p+"-messy"][kind] != severity {
			t.Fatalf("messy campaign missing %s/%s: %v", kind, severity, kinds[p+"-messy"])
		}
	}
	if kinds[p+"-missing"]["PARTICIPANT_ORPHAN"] != "blocker" || kinds[p+"-missing"]["REMINDER_ORPHAN"] != "blocker" {
		t.Fatal("orphans must be reported as blockers")
	}
	for kind, severity := range kinds[p+"-clean"] {
		if severity != "info" {
			t.Fatalf("clean campaign has %s/%s", kind, severity)
		}
	}
	classes := map[string]string{}
	for _, c := range report.Campaigns {
		classes[c.ID] = c.Classification
	}
	if classes[p+"-clean"] != "importable" || classes[p+"-messy"] != "blocked" {
		t.Fatalf("classification wrong: %v", classes)
	}
	raw, _ := json.Marshal(report)
	if strings.Contains(strings.ToLower(string(raw)), strings.ToLower(wallet)) {
		t.Fatal("report must reference wallets only by digest")
	}
	if _, err = pool.Exec(context.Background(), `UPDATE launchpad_campaigns SET title='Clean edited' WHERE id=$1`, p+"-clean"); err != nil {
		t.Fatal(err)
	}
	changed, err := legacyimport.Run(context.Background(), pool)
	if err != nil || changed.SourceSHA256 == report.SourceSHA256 {
		t.Fatal("a changed legacy row must change the source digest")
	}
}
