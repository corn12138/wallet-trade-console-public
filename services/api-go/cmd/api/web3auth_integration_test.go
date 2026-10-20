//go:build integration

package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"math/big"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth/authtest"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/db"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/web3session"
	"github.com/jackc/pgx/v5/pgxpool"
)

func disposablePool(t *testing.T, rawURL string) *pgxpool.Pool {
	t.Helper()
	pool, err := db.Open(context.Background(), rawURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var name string
	if err = pool.QueryRow(context.Background(), `SELECT current_database()`).Scan(&name); err != nil || (!strings.HasPrefix(name, "campaign_test") && !strings.HasPrefix(name, "migration_probe")) {
		t.Fatal("disposable campaign_test*/migration_probe* database required")
	}
	return pool
}

func testDatabaseURL(t *testing.T) string {
	t.Helper()
	raw := os.Getenv("CAMPAIGN_TEST_DATABASE_URL")
	if raw == "" {
		t.Fatal("integration requires CAMPAIGN_TEST_DATABASE_URL for a disposable migrated database")
	}
	return raw
}

// The trusted epoch never goes back: a process configured below an epoch that
// stored sessions already used refuses to start; at that epoch it starts.
func TestStartupRefusesAnEpochBelowStoredSessions(t *testing.T) {
	pool := disposablePool(t, testDatabaseURL(t))
	ctx := context.Background()
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		t.Fatal(err)
	}
	high := 9_000_000_000 + n.Int64()
	ref := auth.SessionRef{ID: authtest.SessionID(t), Wallet: "0xcccccccccccccccccccccccccccccccccccccccc", ChainID: 11155111, Version: 1, Epoch: high, KeyID: "k-epoch"}
	now := time.Now()
	if err := web3session.New(pool).Create(ctx, ref, now, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), `DELETE FROM web3_auth_sessions WHERE id=$1`, ref.ID); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	env := validWeb3Env()
	env["WEB3_AUTH_EPOCH"] = strconv.FormatInt(high-1, 10)
	if _, err := buildWeb3Credentials(ctx, envOf(env), pool); err == nil || !strings.Contains(err.Error(), "never goes back") {
		t.Fatalf("an epoch below the stored sessions must refuse to start: %v", err)
	}
	env["WEB3_AUTH_EPOCH"] = strconv.FormatInt(high, 10)
	creds, err := buildWeb3Credentials(ctx, envOf(env), pool)
	if err != nil || creds.sessions == nil {
		t.Fatalf("the stored epoch itself must start with sessions: %+v %v", creds, err)
	}
}

// A database without the sessions table (migration not applied) refuses to
// start, and the error carries only an SQLSTATE, never connection details.
func TestStartupRefusesADatabaseWithoutTheSessionsTable(t *testing.T) {
	raw := testDatabaseURL(t)
	admin := disposablePool(t, raw)
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	schema := "lg01_empty_" + hex.EncodeToString(suffix)
	if _, err := admin.Exec(context.Background(), `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), `DROP SCHEMA `+schema+` CASCADE`) })
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Del("schema")
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	empty := disposablePool(t, parsed.String())
	_, err = buildWeb3Credentials(context.Background(), envOf(validWeb3Env()), empty)
	if err == nil || !strings.Contains(err.Error(), "SQLSTATE 42P01") {
		t.Fatalf("a missing sessions table must refuse to start: %v", err)
	}
	if strings.Contains(err.Error(), parsed.Host) || strings.Contains(err.Error(), parsed.User.Username()) {
		t.Fatalf("startup error leaks connection details: %v", err)
	}
}
