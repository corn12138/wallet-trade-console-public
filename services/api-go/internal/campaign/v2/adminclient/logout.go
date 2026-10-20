package adminclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"
)

// LogoutResult is what the server confirmed about the stored session. The
// local credential is removed whatever the answer.
type LogoutResult string

const (
	// LogoutRevoked: the server ended the session now.
	LogoutRevoked LogoutResult = "revoked"
	// LogoutAlreadyRevoked: the session had already ended on the server.
	LogoutAlreadyRevoked LogoutResult = "already-revoked"
	// LogoutRejected: the server no longer accepts the token (expired or
	// invalid), so it cannot authorize anything either.
	LogoutRejected LogoutResult = "rejected"
	// LogoutUnconfirmed: no usable answer. A copy of the token may stay valid
	// on the server until it expires.
	LogoutUnconfirmed LogoutResult = "unconfirmed"
	// LogoutNoSession: nothing was stored locally.
	LogoutNoSession LogoutResult = "no-session"
)

// LogoutOutcome reports a logout without the credential itself.
type LogoutOutcome struct {
	Result           LogoutResult
	Address          string
	SessionExpiresAt string
	// Detail says why a logout is unconfirmed; it never echoes a body.
	Detail string
}

// Logout ends the stored session on the API that issued it
// (POST /web3-auth/logout there, never on another base), then removes the local
// credential whatever the server said: local logout is immediate and certain,
// and the outcome reports only what the server confirmed, so an unreachable
// server is never presented as a server-side logout. Intents are untouched: a
// command whose outcome is unknown stays reconcilable after a new login.
func Logout(ctx context.Context, home Home, timeout time.Duration) (LogoutOutcome, error) {
	var stored Credential
	if err := readPrivate(home.credentialPath(), &stored); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return LogoutOutcome{Result: LogoutNoSession}, nil
		}
		// An unreadable or unsafe file is still a credential to get rid of.
		outcome := LogoutOutcome{Result: LogoutUnconfirmed, Detail: "the stored session was unreadable, so the server was not contacted"}
		if removeErr := home.RemoveCredential(); removeErr != nil {
			return outcome, fmt.Errorf("local session could not be removed: %w", removeErr)
		}
		return outcome, nil
	}
	outcome := LogoutOutcome{Address: stored.Address, SessionExpiresAt: stored.SessionExpiresAt}
	outcome.Result, outcome.Detail = revokeRemote(ctx, stored, timeout)
	if err := home.RemoveCredential(); err != nil {
		return outcome, fmt.Errorf("local session could not be removed: %w", err)
	}
	return outcome, nil
}

func revokeRemote(ctx context.Context, stored Credential, timeout time.Duration) (LogoutResult, string) {
	if stored.Token == "" {
		return LogoutUnconfirmed, "the stored session has no token"
	}
	client, err := NewClient(stored.API, timeout)
	if err == nil {
		client, err = client.WithCredential(stored)
	}
	if err != nil {
		return LogoutUnconfirmed, "the stored session names an unusable API"
	}
	reply := client.Do(ctx, http.MethodPost, "/web3-auth/logout", nil, readRequestID())
	if reply.Err != nil {
		return LogoutUnconfirmed, reply.Err.Error()
	}
	switch reply.Status {
	case http.StatusOK:
		var body struct {
			Revoked        *bool `json:"revoked"`
			AlreadyRevoked *bool `json:"alreadyRevoked"`
		}
		if json.Unmarshal(reply.Body, &body) != nil || body.Revoked == nil || !*body.Revoked || body.AlreadyRevoked == nil {
			return LogoutUnconfirmed, "HTTP 200 with an unexpected body"
		}
		if *body.AlreadyRevoked {
			return LogoutAlreadyRevoked, ""
		}
		return LogoutRevoked, ""
	case http.StatusUnauthorized:
		return LogoutRejected, ""
	}
	return LogoutUnconfirmed, fmt.Sprintf("HTTP %d", reply.Status)
}
