// Package outboxrepair provides an internal maintenance seam. No request field
// or Campaign administrator membership is an outbox-maintenance credential.
package outboxrepair

import (
	"context"
	"errors"
	"time"
)

var (
	ErrInvalidInput         = errors.New("outbox repair: invalid input")
	ErrUnauthorized         = errors.New("outbox repair: unauthorized")
	ErrAuthorityUnavailable = errors.New("outbox repair: authority unavailable")
	ErrAuthorityChanged     = errors.New("outbox repair: authority changed")
	ErrUnavailable          = errors.New("outbox repair: store unavailable")
	ErrOutcomeUnknown       = errors.New("outbox repair: reconcile original command")
	ErrConflict             = errors.New("outbox repair: command payload conflict")
	ErrNotFound             = errors.New("outbox repair: not found")
	ErrNotReplayable        = errors.New("outbox repair: no matching unleased dead letter")
)

const (
	InspectOperation = "campaign.outbox.inspect"
	ReplayOperation  = "campaign.outbox.replay"
)

type Authority struct {
	Principal string
	Role      string
	Version   int64
}

// Authorizer supplies trusted attribution and checks the current grant on each
// call. Implementations must honor cancellation and must not trust request text.
type Authorizer interface {
	Authorize(context.Context, string) (Authority, error)
}

type Expected struct {
	Generation   int64
	DeadLetterAt time.Time
	Attempts     int
	ErrorCode    string
}

type Request struct {
	CommandID string
	EventID   string
	Expected  Expected
	Reason    string
	RequestID string
}

// RequestID is transport attribution; it is excluded from the stable command
// fingerprint, so a lost response can be reconciled by a new request.
type Prepared struct {
	Request
	Authority   Authority
	Fingerprint string
}

type Intent struct {
	Prepared
	AcceptedAt time.Time
}

type Snapshot struct {
	Generation   int64      `json:"generation"`
	Attempts     int        `json:"attempts"`
	DeadLetterAt *time.Time `json:"deadLetteredAt"`
	ErrorCode    *string    `json:"errorCode"`
	PublishedAt  *time.Time `json:"publishedAt"`
	AvailableAt  time.Time  `json:"availableAt"`
	LeaseActive  bool       `json:"leaseActive"`
}

type Outcome struct {
	Status      string
	Code        string
	Before      Snapshot
	After       Snapshot
	CompletedAt time.Time
}

type State struct {
	Intent  Intent
	Outcome *Outcome
}

type Entry struct {
	EventID       string
	Topic         string
	StreamVersion int64
	StreamDigest  string
	OccurredAt    time.Time
	Snapshot
}

type Position struct {
	At      time.Time
	EventID string
}

type Page struct {
	ObservedAt time.Time
	Entries    []Entry
	Next       *Position
}

type Listing struct {
	ObservedAt time.Time
	Entries    []Entry
	NextCursor string
}

// Store persists admission before execution. Both methods recheck authority
// after serialization/event locks. Complete commits reset and outcome together;
// its commit error is unknown until Query proves the original command outcome.
type Store interface {
	Admit(context.Context, Prepared, func(context.Context) error) (State, error)
	Complete(context.Context, Prepared, func(context.Context) error) (State, error)
	Query(context.Context, string, string) (State, error)
	ListDeadLetters(context.Context, *Position, int) (Page, error)
}
