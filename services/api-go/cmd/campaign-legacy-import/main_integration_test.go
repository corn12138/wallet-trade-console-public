//go:build integration

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The whole staging sequence through the real command: capture with a report,
// status, fence, verify, restore-check, lift and abandon, against a disposable
// legacy database. Output never carries the seeded wallet.
func TestStagingSequenceThroughTheCommand(t *testing.T) {
	url := os.Getenv("CAMPAIGN_LEGACY_TEST_DATABASE_URL")
	if !strings.Contains(url, "campaign_test_legacy") {
		t.Fatal("integration requires CAMPAIGN_LEGACY_TEST_DATABASE_URL for a dedicated legacy test database")
	}
	t.Setenv("LEGACY_IMPORT_DATABASE_URL", url)
	ctx := context.Background()
	pool, err := db.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	random := make([]byte, 6)
	_, _ = rand.Read(random)
	id := "cli-" + hex.EncodeToString(random)
	wallet := "0x" + strings.Repeat("d", 40)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `UPDATE campaign_legacy_write_fences SET lifted_by='test-cleanup', lifted_at=clock_timestamp() WHERE campaign_id=$1 AND lifted_at IS NULL`, id)
		_, _ = pool.Exec(context.Background(), `DELETE FROM campaign_participants WHERE campaign_id=$1`, id)
		_, _ = pool.Exec(context.Background(), `DELETE FROM launchpad_campaigns WHERE id=$1`, id)
	})
	mustExec(t, pool, `INSERT INTO launchpad_campaigns(id,title,start_date,end_date,status,participants,updated_at) VALUES ($1,'CLI','2020-01-01','2999-01-01','active',1,now())`, id)
	mustExec(t, pool, `INSERT INTO campaign_participants(id,campaign_id,wallet_address) VALUES ($1,$1,$2)`, id, wallet)

	report := filepath.Join(t.TempDir(), "report.json")
	out := command(t, exitOK, "capture", "--operator", "cli-test", "--report", report)
	var captured struct {
		RunID     string         `json:"runId"`
		Status    string         `json:"status"`
		Decisions map[string]int `json:"decisions"`
	}
	if err = json.Unmarshal([]byte(out), &captured); err != nil || captured.Status != "captured" || captured.Decisions["import-public"] == 0 {
		t.Fatalf("capture: %s %v", out, err)
	}
	info, err := os.Stat(report)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("report must be owner-only: %v", err)
	}
	command(t, exitError, "capture", "--operator", "cli-test", "--report", report) // never overwrite a report
	command(t, exitOK, "status", "--run", captured.RunID)
	command(t, exitOK, "fence", "--run", captured.RunID, "--operator", "cli-test", "--campaign", id)
	if out = command(t, exitOK, "verify", "--run", captured.RunID, "--campaign", id); !strings.Contains(out, `"verified": true`) {
		t.Fatalf("verify: %s", out)
	}
	if out = command(t, exitOK, "restore-check", "--run", captured.RunID); !strings.Contains(out, `"restorable": true`) {
		t.Fatalf("restore-check: %s", out)
	}
	command(t, exitError, "abandon", "--run", captured.RunID, "--operator", "cli-test") // fences first
	command(t, exitOK, "lift", "--run", captured.RunID, "--operator", "cli-test", "--campaign", id)
	command(t, exitOK, "abandon", "--run", captured.RunID, "--operator", "cli-test")
	raw, _ := os.ReadFile(report)
	if strings.Contains(strings.ToLower(string(raw)+out), wallet[2:]) {
		t.Fatal("reports reference wallets only by digest")
	}
}

func command(t *testing.T, want int, args ...string) string {
	t.Helper()
	var out, errOut bytes.Buffer
	if code := run(args, &out, &errOut, openPool); code != want {
		t.Fatalf("%v: exit %d, want %d (%s)", args, code, want, errOut.String())
	}
	return out.String()
}

func mustExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}
