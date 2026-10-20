package campaignhttp

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
)

// Admins is a trusted configuration reader, never a request parameter. Calling
// it on each check also covers authorization after waiting on a campaign lock.
type Authorizer struct {
	Verifier *auth.Verifier
	Admins   func() []string
}

var walletPattern = regexp.MustCompile(`^0x[0-9a-f]{40}$`)

// Require re-authenticates the request's own credential, including its
// persistent session, on every check: before reads, and inside the command
// transaction after the campaign lock, so a logout or revocation during the
// wait is refused and the transaction rolls back without side effects.
func (a Authorizer) Require(ctx context.Context, actor string, admin bool) error {
	claims, err := auth.RevalidateStrict(ctx, a.Verifier)
	if err != nil {
		return authFailure(err)
	}
	if actor != claims.Sub {
		return &v2.DomainError{Code: "UNAUTHORIZED", Status: 401}
	}
	if !admin {
		return nil
	}
	if a.Admins != nil {
		for _, entry := range a.Admins() {
			entry = strings.ToLower(strings.TrimSpace(entry))
			if walletPattern.MatchString(entry) && entry == actor {
				return nil
			}
		}
	}
	return &v2.DomainError{Code: "CAMPAIGN_ADMIN_REQUIRED", Status: 403}
}

// authFailure maps a credential failure to the campaign envelope. A session
// that cannot be confirmed is 503: the client keeps its credential and a
// command stays reconcilable under its original key. Anything else is 401.
func authFailure(err error) error {
	if errors.Is(err, auth.ErrSessionUnavailable) {
		return unavailable("WEB3_SESSION_UNAVAILABLE")
	}
	return &v2.DomainError{Code: "UNAUTHORIZED", Status: http.StatusUnauthorized}
}
