// Package legacyactivation records review decisions and activates fenced,
// verified legacy campaigns into the versioned model
// (docs/campaign-legacy-activation-protocol-2026-10-09.md). An activation is
// one READ COMMITTED transaction under the campaign's exclusive fence lock:
// verify the legacy rows, read the legacy answer, write the versioned rows,
// read the answer back through the versioned model at the same instant,
// compare, then commit or roll back.
package legacyactivation

import (
	"errors"
	"fmt"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/legacyimport"
	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
	"github.com/jackc/pgx/v5/pgconn"
)

// Error is a refusal an administration surface can show: a stable code, an
// HTTP status and, when they explain it, the drift or the comparison.
type Error struct {
	Code       string
	Status     int
	Detail     string
	Drift      []legacyimport.Drift
	Comparison *Comparison
}

func (e *Error) Error() string { return e.Code + ": " + e.Detail }

func invalid(detail string) error {
	return &Error{Code: "CAMPAIGN_LEGACY_INVALID_INPUT", Status: 400, Detail: detail}
}

func refused(code, detail string) error { return &Error{Code: code, Status: 409, Detail: detail} }

func notFound(detail string) error {
	return &Error{Code: "CAMPAIGN_LEGACY_NOT_FOUND", Status: 404, Detail: detail}
}

// archiveRefusal maps a failure to read a run's archive for one campaign.
func archiveRefusal(err error) error {
	switch {
	case errors.Is(err, legacyimport.ErrRunNotFound), errors.Is(err, legacyimport.ErrNoPlanItem):
		return notFound(err.Error())
	case errors.Is(err, legacyimport.ErrRunNotCaptured):
		return refused("CAMPAIGN_LEGACY_RUN_NOT_CAPTURED", err.Error())
	}
	return refused("CAMPAIGN_LEGACY_ARCHIVE_INVALID", err.Error())
}

// unavailable keeps a database guard's message and SQLSTATE and drops anything
// that could carry connection details or stored values.
func unavailable(err error) error {
	var known *Error
	if errors.As(err, &known) {
		return err
	}
	var domain *v2.DomainError
	if errors.As(err, &domain) {
		return &Error{Code: domain.Code, Status: domain.Status, Detail: domain.Detail}
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return &Error{Code: "CAMPAIGN_DATA_UNAVAILABLE", Status: 503, Detail: fmt.Sprintf("%s (SQLSTATE %s)", pg.Message, pg.Code)}
	}
	return &Error{Code: "CAMPAIGN_DATA_UNAVAILABLE", Status: 503, Detail: err.Error()}
}
