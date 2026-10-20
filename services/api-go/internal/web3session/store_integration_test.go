//go:build integration

package web3session_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth/authtest"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/db"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/web3session"
	"github.com/jackc/pgx/v5/pgxpool"
)

const walletA = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const walletB = "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("CAMPAIGN_TEST_DATABASE_URL")
	if url == "" {
		t.Fatal("integration requires CAMPAIGN_TEST_DATABASE_URL for a disposable migrated database")
	}
	p, err := db.Open(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	var name string
	if err = p.QueryRow(context.Background(), `SELECT current_database()`).Scan(&name); err != nil || (!strings.HasPrefix(name, "campaign_test") && !strings.HasPrefix(name, "migration_probe")) {
		t.Fatal("disposable campaign_test*/migration_probe* database required")
	}
	return p
}

func ref(t *testing.T, wallet string) auth.SessionRef {
	return auth.SessionRef{ID: authtest.SessionID(t), Wallet: wallet, ChainID: 11155111, Version: 1, Epoch: 3, KeyID: "k1"}
}

func TestSessionLifecycleAgainstPostgres(t *testing.T) {
	p := pool(t)
	store := web3session.New(p)
	ctx := context.Background()
	now := time.Now()
	a := ref(t, walletA)
	if err := store.Create(ctx, a, now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if ok, err := store.CheckSession(ctx, a); err != nil || !ok {
		t.Fatalf("fresh session: %t %v", ok, err)
	}
	for name, mutate := range map[string]func(*auth.SessionRef){
		"other wallet":  func(r *auth.SessionRef) { r.Wallet = walletB },
		"other chain":   func(r *auth.SessionRef) { r.ChainID = 1 },
		"other version": func(r *auth.SessionRef) { r.Version = 2 },
		"other epoch":   func(r *auth.SessionRef) { r.Epoch = 4 },
		"other key":     func(r *auth.SessionRef) { r.KeyID = "k2" },
		"unknown id":    func(r *auth.SessionRef) { r.ID = authtest.SessionID(t) },
	} {
		mismatch := a
		mutate(&mismatch)
		if ok, err := store.CheckSession(ctx, mismatch); err != nil || ok {
			t.Fatalf("%s must not validate", name)
		}
	}
	other := a
	other.Wallet = walletB
	if _, err := store.Revoke(ctx, other, web3session.ReasonLogout, "self"); !errors.Is(err, web3session.ErrNotOwned) {
		t.Fatalf("another wallet cannot revoke: %v", err)
	}
	for name, mutate := range map[string]func(*auth.SessionRef){
		"other chain":   func(r *auth.SessionRef) { r.ChainID = 1 },
		"other version": func(r *auth.SessionRef) { r.Version = 2 },
		"other epoch":   func(r *auth.SessionRef) { r.Epoch = 4 },
		"other key":     func(r *auth.SessionRef) { r.KeyID = "k2" },
	} {
		mismatched := a
		mutate(&mismatched)
		if _, err := store.Revoke(ctx, mismatched, web3session.ReasonLogout, "self"); !errors.Is(err, web3session.ErrNotOwned) {
			t.Fatalf("%s: a reference that differs from the session must not revoke it: %v", name, err)
		}
		if ok, err := store.CheckSession(ctx, a); err != nil || !ok {
			t.Fatalf("%s: the session must stay live: %t %v", name, ok, err)
		}
	}
	if already, err := store.Revoke(ctx, a, web3session.ReasonLogout, "self"); err != nil || already {
		t.Fatalf("first logout: %t %v", already, err)
	}
	if already, err := store.Revoke(ctx, a, web3session.ReasonLogout, "self"); err != nil || !already {
		t.Fatalf("repeated logout is idempotent: %t %v", already, err)
	}
	if ok, _ := store.CheckSession(ctx, a); ok {
		t.Fatal("revoked session must not validate")
	}
	// The trigger makes revocation one-way and identity immutable, even for
	// direct SQL by an application role.
	for name, sql := range map[string]string{
		"un-revoke":     `UPDATE web3_auth_sessions SET revoked_at=NULL, revoke_reason=NULL, revoked_by=NULL WHERE id=$1`,
		"change wallet": `UPDATE web3_auth_sessions SET wallet_address='` + walletB + `' WHERE id=$1`,
		"extend expiry": `UPDATE web3_auth_sessions SET expires_at=expires_at + interval '1 day' WHERE id=$1`,
	} {
		if _, err := p.Exec(ctx, sql, a.ID); err == nil {
			t.Fatalf("%s must be rejected", name)
		}
	}
	expired := ref(t, walletA)
	if err := store.Create(ctx, expired, now.Add(-2*time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if ok, _ := store.CheckSession(ctx, expired); ok {
		t.Fatal("expiry is judged by the database clock")
	}
	b1, b2 := ref(t, walletB), ref(t, walletB)
	store.Create(ctx, b1, now, now.Add(time.Hour))
	store.Create(ctx, b2, now, now.Add(time.Hour))
	if n, err := store.RevokeWallet(ctx, walletB, "ops:test"); err != nil || n < 2 {
		t.Fatalf("operator revocation: %d %v", n, err)
	}
	if ok, _ := store.CheckSession(ctx, b2); ok {
		t.Fatal("operator revocation must end every session of the wallet")
	}
	if epoch, err := store.MaxEpoch(ctx); err != nil || epoch < 3 {
		t.Fatalf("max epoch: %d %v", epoch, err)
	}
	bad := ref(t, "0x0000000000000000000000000000000000000000")
	if err := store.Create(ctx, bad, now, now.Add(time.Hour)); err == nil {
		t.Fatal("the zero wallet cannot own a session")
	}
}
