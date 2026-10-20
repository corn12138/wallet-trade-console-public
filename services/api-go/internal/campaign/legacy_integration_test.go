//go:build integration

package campaign

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestLegacySQLExcludesVersionedDefinitionsAndBoundsReads(t *testing.T) {
	url := os.Getenv("CAMPAIGN_LEGACY_TEST_DATABASE_URL")
	if url == "" {
		t.Fatal("CAMPAIGN_LEGACY_TEST_DATABASE_URL is required for the isolated SQL check")
	}
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal("invalid isolated database configuration")
	}
	if !strings.HasPrefix(config.ConnConfig.Database, "campaign_test_legacy") && !strings.HasPrefix(config.ConnConfig.Database, "migration_probe_legacy") {
		t.Fatal("legacy list checks require a dedicated legacy test database")
	}
	config.MaxConns = 4
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatal("isolated database unavailable")
	}
	t.Cleanup(pool.Close)
	// Fresh public schemas carry the same guards without private migration history.
	var initialized bool
	if err := pool.QueryRow(t.Context(), `SELECT
		to_regclass('public.campaign_definitions') IS NOT NULL AND
		to_regclass('public.campaign_commands') IS NOT NULL AND
		to_regprocedure('public.campaign_command_attribution_guard()') IS NOT NULL`).Scan(&initialized); err != nil || !initialized {
		t.Fatal("complete versioned campaign schema is required")
	}
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	prefix := "legacy-query-" + hex.EncodeToString(random)
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context()) // Committed fixtures remain isolated by prefix; append-only facts are never deleted.
	for i := range 62 {
		id, status, start := fmt.Sprintf("%s-%02d", prefix, i), "active", "2000-01-01T00:00:00Z"
		if i == 60 {
			status = "upcoming"
		}
		if i >= 60 {
			start = "2100-01-01T00:00:00Z"
		}
		_, err := tx.Exec(t.Context(), `INSERT INTO launchpad_campaigns
			(id,title,start_date,end_date,status,updated_at) VALUES($1,$4,$2,'2200-01-01T00:00:00Z',$3,clock_timestamp())`, id, start, status, id)
		if err != nil {
			t.Fatal(err)
		}
		if i >= 60 {
			// Both a draft-looking and active-looking legacy shadow are private
			// to v2 once definition ownership exists, independent of publication.
			if _, err := tx.Exec(t.Context(), `INSERT INTO campaign_definitions(id,created_by) VALUES($1,$2)`, id, testWallet); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(pool)
	for _, index := range []int{60, 61} {
		if _, err := repo.FindByID(t.Context(), fmt.Sprintf("%s-%02d", prefix, index)); !errors.Is(err, ErrNotFound) {
			t.Fatalf("versioned definition leaked from legacy detail: %v", err)
		}
	}
	if _, err := repo.FindByID(t.Context(), prefix+"-00"); err != nil {
		t.Fatalf("legacy row lost during cutover: %v", err)
	}
	for _, status := range []string{"", "active", "upcoming"} {
		for _, limit := range []int{20, 50} {
			rows, err := repo.ListAll(t.Context(), status, limit)
			if err != nil {
				t.Fatal(err)
			}
			assertLegacyRows(t, rows, prefix, limit)
			if status != "upcoming" && len(rows) != limit {
				t.Fatalf("SQL list limit %d produced %d rows", limit, len(rows))
			}
		}
	}
	rows, err := repo.ListAll(t.Context(), "")
	if err != nil || len(rows) != 20 {
		t.Fatalf("SQL default limit: rows=%d err=%v", len(rows), err)
	}
	for _, limit := range []int{20, 50} {
		rows, err := repo.GetActive(t.Context(), time.Date(2300, 1, 1, 0, 0, 0, 0, time.UTC), limit)
		if err != nil {
			t.Fatal(err)
		}
		assertLegacyRows(t, rows, prefix, limit)
		if len(rows) != limit {
			t.Fatalf("SQL active limit %d produced %d rows", limit, len(rows))
		}
	}
	t.Logf("isolated fixture prefix=%s; legacy detail/list/active retained; both v2 ownership shadows excluded; default20/max50 SQL bounds passed", prefix)
}

func assertLegacyRows(t *testing.T, rows []Campaign, prefix string, limit int) {
	t.Helper()
	if len(rows) > limit {
		t.Fatalf("unbounded legacy read: %d > %d", len(rows), limit)
	}
	for _, row := range rows {
		if row.ID == prefix+"-60" || row.ID == prefix+"-61" {
			t.Fatalf("versioned definition leaked into a legacy list: %s", row.ID)
		}
	}
}
