package eventpush

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"regexp"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/outboxwire"
	"github.com/jackc/pgx/v5/pgxpool"
)

var wakeupIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// decodeWakeup accepts exactly the publisher's {schemaVersion:1,eventId} body.
func decodeWakeup(raw string) (string, bool) {
	var body struct {
		SchemaVersion int    `json:"schemaVersion"`
		EventID       string `json:"eventId"`
	}
	if len(raw) > 128 {
		return "", false
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(raw)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&body) != nil || decoder.Decode(new(any)) != io.EOF || body.SchemaVersion != 1 || !wakeupIDPattern.MatchString(body.EventID) {
		return "", false
	}
	return body.EventID, true
}

// Listen keeps one dedicated connection listening on the Campaign wakeup
// channel until Close. Every (re)registration is followed by a full sweep, so
// notifications missed while disconnected are recovered from durable storage.
func (h *Hub) Listen(pool *pgxpool.Pool) {
	if pool == nil {
		return
	}
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		backoff := time.Second
		for h.ctx.Err() == nil {
			attached, err := h.listenOnce(pool)
			if h.ctx.Err() != nil {
				return
			}
			if attached {
				backoff = time.Second
			}
			slog.Warn("campaign push: wakeup listener dropped; durable sweeps continue", "retryIn", backoff.String(), "reason", listenerFailure(err))
			timer := time.NewTimer(backoff)
			select {
			case <-h.ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
		}
	}()
}

// listenerFailure reports a closed category; raw errors can carry connection
// details and are not logged.
func listenerFailure(err error) string {
	if err == nil {
		return "closed"
	}
	return "connection"
}

func (h *Hub) listenOnce(pool *pgxpool.Pool) (bool, error) {
	acquired, err := pool.Acquire(h.ctx)
	if err != nil {
		return false, err
	}
	// A listening session must never return to the pool, where another caller
	// would inherit its registration and buffered notifications.
	conn := acquired.Hijack()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = conn.Close(ctx)
		cancel()
	}()
	if _, err = conn.Exec(h.ctx, "LISTEN "+outboxwire.Channel); err != nil {
		return false, err
	}
	h.RequestSweep()
	slog.Info("campaign push: wakeup listener attached", "channel", outboxwire.Channel)
	for {
		notification, err := conn.WaitForNotification(h.ctx)
		if err != nil {
			return true, err
		}
		if notification.Channel != outboxwire.Channel {
			continue
		}
		if id, ok := decodeWakeup(notification.Payload); ok {
			h.Wake(id)
		}
	}
}
