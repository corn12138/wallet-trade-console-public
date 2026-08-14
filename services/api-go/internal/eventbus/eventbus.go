// Package eventbus is the production transport between the indexer's DB sink
// (which projects chain events) and the API's Socket.IO tier (which fans them
// out to /token-events subscribers). The two run as separate processes
// (cmd/indexer, cmd/api), so an in-memory call cannot work in deploy — the
// bus rides Postgres LISTEN/NOTIFY, which is already a hard dependency of
// both binaries (no new infra).
//
//	sink (after committed projection) ──pg_notify──▶ wtc_token_events
//	api LISTEN loop ──DecodeTokenEvent──▶ Dispatch ──▶ realtime gateways
//
// Payloads are small JSON envelopes (NOTIFY caps payloads at ~8000 bytes;
// trade/price/graduation events are a few hundred).
package eventbus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TokenEventsChannel is the Postgres NOTIFY channel name.
const TokenEventsChannel = "wtc_token_events"

// Event kinds — mirror the /token-events Socket.IO event names.
const (
	KindTrade       = "trade"
	KindPriceUpdate = "price-update"
	KindGraduation  = "graduation"
)

// TokenEvent is the wire envelope.
type TokenEvent struct {
	Kind         string          `json:"kind"`
	TokenAddress string          `json:"tokenAddress"`
	Payload      json.RawMessage `json:"payload"`
}

// TradePayload is the Kind=trade payload — one projected bonding-curve trade.
type TradePayload struct {
	TokenAddress string `json:"tokenAddress"`
	TokenID      string `json:"tokenId"`
	Type         string `json:"type"` // BUY | SELL
	UserAddress  string `json:"userAddress"`
	TokenAmount  string `json:"tokenAmount"`
	EthAmount    string `json:"ethAmount"`
	Price        string `json:"price"` // normalized NATIVE/token decimal string
	TxHash       string `json:"txHash"`
	BlockNumber  int64  `json:"blockNumber"`
	Timestamp    string `json:"timestamp"`
}

// PricePayload is the Kind=price-update payload.
type PricePayload struct {
	TokenAddress string `json:"tokenAddress"`
	Price        string `json:"price"`     // normalized NATIVE/token decimal string
	MarketCap    string `json:"marketCap"` // normalized NATIVE decimal string
	Timestamp    string `json:"timestamp"`
}

// GraduationPayload is the Kind=graduation payload.
type GraduationPayload struct {
	TokenAddress string `json:"tokenAddress"`
	MarketCap    string `json:"marketCap"`
	LPToken      string `json:"lpToken,omitempty"`
	LPAmount     string `json:"lpAmount,omitempty"`
	Timestamp    string `json:"timestamp"`
}

// NewTokenEvent wraps a typed payload into the wire envelope.
func NewTokenEvent(kind, tokenAddress string, payload any) (TokenEvent, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return TokenEvent{}, fmt.Errorf("eventbus: marshal %s payload: %w", kind, err)
	}
	return TokenEvent{
		Kind:         kind,
		TokenAddress: strings.ToLower(strings.TrimSpace(tokenAddress)),
		Payload:      raw,
	}, nil
}

// EncodeTokenEvent renders the envelope for pg_notify.
func EncodeTokenEvent(ev TokenEvent) (string, error) {
	b, err := json.Marshal(ev)
	if err != nil {
		return "", fmt.Errorf("eventbus: encode: %w", err)
	}
	return string(b), nil
}

// DecodeTokenEvent parses a NOTIFY payload back into the envelope.
func DecodeTokenEvent(raw string) (TokenEvent, error) {
	var ev TokenEvent
	if err := json.Unmarshal([]byte(raw), &ev); err != nil {
		return TokenEvent{}, fmt.Errorf("eventbus: decode: %w", err)
	}
	if ev.Kind == "" || ev.TokenAddress == "" {
		return TokenEvent{}, errors.New("eventbus: decode: missing kind/tokenAddress")
	}
	return ev, nil
}

// TokenEventHandler is the broadcast surface Dispatch drives.
// *realtime.Server satisfies it.
type TokenEventHandler interface {
	BroadcastTrade(tokenAddr string, trade any)
	BroadcastPriceUpdate(tokenAddr string, update any)
	BroadcastGraduation(tokenAddr string, payload any)
}

// Dispatch routes one decoded event to the matching broadcast. The payload is
// re-decoded to a generic map so the Socket.IO emitter serializes it as plain
// JSON. Unknown kinds are logged and dropped (forward compatibility).
func Dispatch(h TokenEventHandler, ev TokenEvent) {
	if h == nil {
		return
	}
	var payload any
	if len(ev.Payload) > 0 {
		if err := json.Unmarshal(ev.Payload, &payload); err != nil {
			slog.Warn("eventbus: undecodable payload", "kind", ev.Kind, "err", err)
			return
		}
	}
	switch ev.Kind {
	case KindTrade:
		h.BroadcastTrade(ev.TokenAddress, payload)
	case KindPriceUpdate:
		h.BroadcastPriceUpdate(ev.TokenAddress, payload)
	case KindGraduation:
		h.BroadcastGraduation(ev.TokenAddress, payload)
	default:
		slog.Warn("eventbus: unknown event kind", "kind", ev.Kind)
	}
}

// DispatchEncoded decodes + dispatches one raw NOTIFY payload. This is the
// exact function the production LISTEN loop runs per notification; the
// realtime e2e harness drives it directly so the producer path under test is
// the deployed code, not a test-only shortcut.
func DispatchEncoded(h TokenEventHandler, raw string) error {
	ev, err := DecodeTokenEvent(raw)
	if err != nil {
		return err
	}
	Dispatch(h, ev)
	return nil
}

// Execer is the narrow pg surface the publisher needs (pgxpool.Pool or a fake).
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// PgPublisher publishes token events over pg_notify. Safe for concurrent use.
type PgPublisher struct {
	db Execer
}

// NewPgPublisher wraps a pool (nil yields a publisher whose Publish errors).
func NewPgPublisher(db Execer) *PgPublisher {
	return &PgPublisher{db: db}
}

// PublishTokenEvent sends one envelope. Callers treat failures as
// non-fatal (the projection already committed; realtime is best-effort).
func (p *PgPublisher) PublishTokenEvent(ctx context.Context, ev TokenEvent) error {
	if p == nil || p.db == nil {
		return errors.New("eventbus: publisher has no database")
	}
	encoded, err := EncodeTokenEvent(ev)
	if err != nil {
		return err
	}
	if _, err := p.db.Exec(ctx, `SELECT pg_notify($1, $2)`, TokenEventsChannel, encoded); err != nil {
		return fmt.Errorf("eventbus: pg_notify: %w", err)
	}
	return nil
}

// ListenTokenEvents runs the API-side LISTEN loop until ctx is cancelled:
// acquire a dedicated connection, LISTEN, and dispatch each notification to
// the handler. Connection loss re-acquires with backoff — the loop never
// gives up while the process lives (a dead listener silently killing the
// "live" feed is precisely the dead-namespace failure this replaces).
func ListenTokenEvents(ctx context.Context, pool *pgxpool.Pool, h TokenEventHandler) {
	if pool == nil || h == nil {
		return
	}
	backoff := time.Second
	for ctx.Err() == nil {
		err := listenOnce(ctx, pool, h)
		if ctx.Err() != nil {
			return
		}
		slog.Warn("eventbus: token-events listener dropped; reconnecting", "err", err, "retry_in", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func listenOnce(ctx context.Context, pool *pgxpool.Pool, h TokenEventHandler) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "LISTEN "+TokenEventsChannel); err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	slog.Info("eventbus: token-events listener attached", "channel", TokenEventsChannel)

	for {
		notification, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			return fmt.Errorf("wait: %w", err)
		}
		if err := DispatchEncoded(h, notification.Payload); err != nil {
			slog.Warn("eventbus: dropped malformed token event", "err", err)
		}
	}
}
