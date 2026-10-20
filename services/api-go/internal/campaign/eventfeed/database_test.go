package eventfeed

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func database(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn, template := os.Getenv("OUTBOX_TEST_ADMIN_URL"), os.Getenv("OUTBOX_TEST_TEMPLATE")
	if dsn == "" || template == "" {
		t.Skip("event feed migrated database evidence requires OUTBOX_TEST_ADMIN_URL and OUTBOX_TEST_TEMPLATE")
	}
	u, err := url.Parse(dsn)
	if err != nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") || !regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`).MatchString(template) {
		t.Fatal("loopback admin and plain template required")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("eventfeed_test_%d", os.Getpid())
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()+" TEMPLATE "+pgx.Identifier{template}.Sanitize()); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	var pool *pgxpool.Pool
	t.Cleanup(func() {
		if pool != nil {
			pool.Close()
		}
		if _, err := admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
			t.Error(err)
		}
		_ = admin.Close(ctx)
	})
	u.Path = "/" + name
	pool, err = pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	return pool
}
