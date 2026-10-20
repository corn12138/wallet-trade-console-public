package outboxrepair

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"time"
)

type positionCursor struct {
	Schema  int    `json:"schema"`
	Scope   string `json:"scope"`
	At      string `json:"at"`
	EventID string `json:"eventId"`
}

func encodePosition(position Position, scope string) string {
	raw, _ := json.Marshal(positionCursor{1, scope, position.At.UTC().Format(time.RFC3339Nano), position.EventID})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodePosition(raw, scope string) (*Position, error) {
	if raw == "" {
		return nil, nil
	}
	if len(raw) > 1024 {
		return nil, ErrInvalidInput
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(raw)
	if err != nil {
		return nil, ErrInvalidInput
	}
	var cursor positionCursor
	if json.Unmarshal(decoded, &cursor) != nil || cursor.Schema != 1 || cursor.Scope != scope || !uuid.MatchString(cursor.EventID) {
		return nil, ErrInvalidInput
	}
	at, err := time.Parse(time.RFC3339Nano, cursor.At)
	p := Position{At: at, EventID: cursor.EventID}
	// Exact encoding also rejects duplicate keys, case aliases, extra fields,
	// whitespace variants and alternate encodings of the same timestamp.
	if err != nil || !validTime(at) || encodePosition(p, scope) != raw {
		return nil, ErrInvalidInput
	}
	return &p, nil
}

func (s *Service) Inspect(ctx context.Context, cursor string, limit int) (Listing, error) {
	var empty Listing
	if limit < 1 || limit > 50 {
		return empty, ErrInvalidInput
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	a, err := s.authorize(ctx, InspectOperation)
	if err != nil {
		return empty, err
	}
	scope := digest(struct {
		Operation, Source, Status string
		Authority                 Authority
	}{InspectOperation, "campaign", "deadLetter", a})
	position, err := decodePosition(cursor, scope)
	if err != nil {
		return empty, err
	}
	if s.store == nil {
		return empty, ErrUnavailable
	}
	page, err := s.store.ListDeadLetters(ctx, position, limit)
	if err != nil {
		return empty, safeStoreError(err)
	}
	if err = s.guard(InspectOperation, a)(ctx); err != nil {
		return empty, err
	}
	result := Listing{ObservedAt: page.ObservedAt, Entries: page.Entries}
	if page.Next != nil {
		result.NextCursor = encodePosition(*page.Next, scope)
	}
	return result, nil
}
