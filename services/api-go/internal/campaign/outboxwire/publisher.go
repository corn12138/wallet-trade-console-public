// Package outboxwire provides advisory Campaign wakeups, not business-event
// delivery. Listeners must recover from durable storage after disconnects and
// restart; a successful NOTIFY does not establish consumption or authorization.
package outboxwire

import (
	"context"
	"fmt"
	"regexp"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/durableoutbox"
	"github.com/jackc/pgx/v5/pgconn"
)

const Channel = "campaign_outbox_wakeup"

var (
	eventIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	ownerPattern   = regexp.MustCompile(`^0x[0-9a-f]{40}$`)
)

// Execer must be concurrency-safe and execute outside an explicit transaction
// (for example pgxpool.Pool). PostgreSQL defers NOTIFY until commit, so a queued
// notification inside a caller-owned transaction cannot confirm acceptance.
type Execer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

type Publisher struct{ db Execer }

var _ durableoutbox.Publisher = (*Publisher)(nil)

func New(db Execer) *Publisher { return &Publisher{db: db} }

func (*Publisher) Supports(e durableoutbox.Event) bool {
	if e.Source != "campaign" || e.PayloadVersion != 1 || !eventIDPattern.MatchString(e.ID) || e.ID == "00000000-0000-0000-0000-000000000000" {
		return false
	}
	switch e.Topic {
	case "campaign.publication.changed", "campaign.interaction.changed":
		return e.OwnerScope == nil
	case "campaign.personal.changed":
		return e.OwnerScope != nil && ownerPattern.MatchString(*e.OwnerScope) && *e.OwnerScope != "0x0000000000000000000000000000000000000000"
	default:
		return false
	}
}

func (p *Publisher) Publish(ctx context.Context, e durableoutbox.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !p.Supports(e) {
		return durableoutbox.ErrUnsupported
	}
	if p == nil || p.db == nil {
		return durableoutbox.ErrInvalidInput
	}
	// Only a validated fixed-length ID crosses the transport. Topic, owner,
	// stream and payload must never become a public/private routing side channel.
	wakeup := fmt.Sprintf(`{"schemaVersion":1,"eventId":"%s"}`, e.ID)
	_, err := p.db.Exec(ctx, `SELECT pg_notify($1, $2)`, Channel, wakeup)
	return err
}
