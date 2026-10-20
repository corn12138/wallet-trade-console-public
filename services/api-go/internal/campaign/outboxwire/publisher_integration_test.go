package outboxwire

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresNotificationContainsOnlyVersionAndEventID(t *testing.T) {
	dsn, template := os.Getenv("OUTBOX_TEST_ADMIN_URL"), os.Getenv("OUTBOX_TEST_TEMPLATE")
	if dsn == "" || template == "" {
		t.Skip("real notification unproven: OUTBOX_TEST_ADMIN_URL / OUTBOX_TEST_TEMPLATE unset")
	}
	u, err := url.Parse(dsn)
	if err != nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") || !regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`).MatchString(template) {
		t.Fatal("loopback admin and plain template required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("outboxwire_test_%d_%d", os.Getpid(), time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()+" TEMPLATE "+pgx.Identifier{template}.Sanitize()); err != nil {
		admin.Close(context.Background())
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanup, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
			t.Error(err)
		}
		admin.Close(cleanup)
	})
	u.Path = "/" + name
	pool, err := pgxpool.New(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	listener, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close(context.Background()) })
	if _, err := listener.Exec(ctx, "LISTEN "+pgx.Identifier{Channel}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	publisher := New(pool)
	var previous string
	for _, private := range []bool{false, true} {
		e := event()
		if private {
			e.Topic = "campaign.personal.changed"
			e.OwnerScope = ptr("0x" + strings.Repeat("c", 40))
		}
		if err := publisher.Publish(ctx, e); err != nil {
			t.Fatal(err)
		}
		notification, err := listener.WaitForNotification(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if notification.Channel != Channel || len(notification.Payload) > 96 || json.Unmarshal([]byte(notification.Payload), &fields) != nil || len(fields) != 2 {
			t.Fatalf("unexpected notification: %#v", notification)
		}
		if string(fields["schemaVersion"]) != "1" || string(fields["eventId"]) != `"`+e.ID+`"` {
			t.Fatalf("unexpected fields: %s", notification.Payload)
		}
		if private && notification.Payload != previous {
			t.Fatal("private metadata changed notification")
		}
		previous = notification.Payload
	}
}
