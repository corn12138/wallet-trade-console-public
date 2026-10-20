//go:build integration

package postgres

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/application"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestQuerySnapshotIsRepeatableReadOnlyAcrossConcurrentCommit(t *testing.T) {
	ctx := context.Background()
	url := os.Getenv("CAMPAIGN_TEST_DATABASE_URL")
	if url == "" {
		t.Fatal("explicit disposable test database required")
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var name string
	if err = pool.QueryRow(ctx, `SELECT current_database()`).Scan(&name); err != nil || (!strings.HasPrefix(name, "campaign_test") && !strings.HasPrefix(name, "migration_probe")) {
		t.Fatal("disposable campaign database required")
	}
	scope, err := application.NewID()
	if err != nil {
		t.Fatal(err)
	}
	actor := "0x" + strings.ReplaceAll(scope, "-", "") + "00000000"
	store := New(pool)
	err = store.readSnapshot(ctx, func(q *querySnapshot) error {
		var isolation, readonly string
		if err := q.tx.QueryRow(q.ctx, `SELECT current_setting('transaction_isolation'),current_setting('transaction_read_only')`).Scan(&isolation, &readonly); err != nil {
			return err
		}
		if isolation != "repeatable read" || readonly != "on" || q.timestamp() == "" {
			t.Fatal("query transaction is not a timed read-only snapshot")
		}
		var count int
		if err := q.tx.QueryRow(q.ctx, `SELECT count(*) FROM campaign_private_scopes WHERE wallet_address=$1`, actor).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatal("random test principal already exists")
		}
		if _, err := pool.Exec(ctx, `INSERT INTO campaign_private_scopes(wallet_address,scope_id) VALUES ($1,$2)`, actor, scope); err != nil {
			return err
		}
		if err := q.tx.QueryRow(q.ctx, `SELECT count(*) FROM campaign_private_scopes WHERE wallet_address=$1`, actor).Scan(&count); err != nil {
			return err
		}
		if count != 0 {
			t.Fatal("one response mixed database snapshots")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM campaign_private_scopes WHERE wallet_address=$1`, actor).Scan(&count); err != nil || count != 1 {
		t.Fatal("concurrent fixture did not commit")
	}
}
