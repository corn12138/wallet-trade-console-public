//go:build integration

package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth/authtest"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/db"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/web3session"
)

// The real command against a disposable database: every live session of the
// wallet ends, another wallet's session survives, and the row records the
// operator with reason "operator".
func TestRevokeWalletAgainstPostgres(t *testing.T) {
	url := os.Getenv("CAMPAIGN_TEST_DATABASE_URL")
	if url == "" {
		t.Fatal("integration requires CAMPAIGN_TEST_DATABASE_URL for a disposable migrated database")
	}
	ctx := context.Background()
	pool, err := db.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var name string
	if err = pool.QueryRow(ctx, `SELECT current_database()`).Scan(&name); err != nil || !strings.HasPrefix(name, "campaign_test") {
		t.Fatal("disposable campaign_test* database required")
	}
	t.Setenv("DATABASE_URL", url)
	store := web3session.New(pool)
	target := "0x" + strings.Repeat("d", 40)
	other := "0x" + strings.Repeat("e", 40)
	refs := []auth.SessionRef{}
	for _, wallet := range []string{target, target, other} {
		ref := auth.SessionRef{ID: authtest.SessionID(t), Wallet: wallet, ChainID: 1, Version: 1, Epoch: 1, KeyID: "k1"}
		if err := store.Create(ctx, ref, time.Now(), time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		refs = append(refs, ref)
	}
	t.Cleanup(func() {
		for _, ref := range refs {
			_, _ = pool.Exec(context.Background(), `DELETE FROM web3_auth_sessions WHERE id=$1`, ref.ID)
		}
	})
	var out, errOut bytes.Buffer
	if code := run([]string{"revoke-wallet", "--wallet", "not-a-wallet", "--operator", "ops"}, &out, &errOut, openStore); code != 2 {
		t.Fatalf("a malformed wallet must be refused before connecting: %d", code)
	}
	out.Reset()
	if code := run([]string{"revoke-wallet", "--wallet", target, "--operator", "ops:integration"}, &out, &errOut, openStore); code != 0 || !strings.Contains(out.String(), `"revokedSessions": 2`) {
		t.Fatalf("exit %d out %s err %s", code, out.String(), errOut.String())
	}
	for i, ref := range refs {
		live, err := store.CheckSession(ctx, ref)
		if err != nil || live != (i == 2) {
			t.Fatalf("session %d live=%v err=%v", i, live, err)
		}
	}
	var reason, by string
	if err := pool.QueryRow(ctx, `SELECT revoke_reason, revoked_by FROM web3_auth_sessions WHERE id=$1`, refs[0].ID).Scan(&reason, &by); err != nil || reason != "operator" || by != "ops:integration" {
		t.Fatalf("revocation record %q %q %v", reason, by, err)
	}
}
