package auth

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Key is one HS256 secret. The current key signs every new token; a previous
// key may still verify tokens until Retire and never signs. A token whose key
// is unknown or retired is rejected, so a rotation has an explicit cutoff.
type Key struct {
	ID     string
	Secret []byte
	Retire time.Time
}

// SessionRef is the persistent session a token claims to belong to.
type SessionRef struct {
	ID      string
	Wallet  string
	ChainID int
	Version int
	Epoch   int64
	KeyID   string
}

// SessionChecker confirms that a session exists and still matches the token:
// same wallet, chain, version, epoch and key, unrevoked and unexpired. It must
// return an error (never false) when the store cannot answer, so callers fail
// closed with "unavailable" instead of treating the session as invalid.
type SessionChecker interface {
	CheckSession(ctx context.Context, ref SessionRef) (bool, error)
}

// Policy is the trusted startup configuration for web3 bearer credentials. The
// same Policy both issues (Issue) and verifies tokens, so issuer and verifier
// can never disagree about algorithm, issuer, audience, keys or epoch.
//
// Epoch is the current auth epoch. Its trusted value comes from deployment
// configuration, outside the business database: after a database restore the
// operators raise it on every instance and all earlier tokens stop working,
// even if the restored session rows look valid.
type Policy struct {
	Issuer   string
	Audience string
	Current  Key
	Previous []Key
	Epoch    int64
	// Leeway tolerates clock skew for iat/nbf/exp between instances.
	Leeway time.Duration
	// Sessions is required for Authenticate; when nil every private request
	// fails closed with ErrSessionUnavailable.
	Sessions SessionChecker
	Now      func() time.Time
}

var keyIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// MinSecretBytes is the smallest accepted HS256 secret.
const MinSecretBytes = 32

func (p Policy) validate() error {
	if strings.TrimSpace(p.Issuer) == "" || strings.TrimSpace(p.Audience) == "" {
		return errors.New("auth policy: issuer and audience are required")
	}
	if p.Epoch < 1 {
		return errors.New("auth policy: auth epoch must be a positive integer")
	}
	if p.Leeway < 0 || p.Leeway > 5*time.Minute {
		return errors.New("auth policy: leeway must be between 0 and 5 minutes")
	}
	seen := map[string]bool{}
	for i, key := range append([]Key{p.Current}, p.Previous...) {
		if !keyIDPattern.MatchString(key.ID) || seen[key.ID] {
			return fmt.Errorf("auth policy: key id %q is malformed or duplicated", key.ID)
		}
		if len(key.Secret) < MinSecretBytes {
			return fmt.Errorf("auth policy: key %q is shorter than %d bytes", key.ID, MinSecretBytes)
		}
		if i == 0 && !key.Retire.IsZero() {
			return errors.New("auth policy: the current key cannot have a retirement time")
		}
		if i > 0 && key.Retire.IsZero() {
			return fmt.Errorf("auth policy: previous key %q needs an explicit retirement time", key.ID)
		}
		seen[key.ID] = true
	}
	return nil
}

func (p Policy) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// verificationKey returns the secret for kid if that key may still verify.
func (p Policy) verificationKey(kid string) ([]byte, bool) {
	if kid == p.Current.ID {
		return p.Current.Secret, true
	}
	for _, key := range p.Previous {
		if key.ID == kid && p.now().Before(key.Retire) {
			return key.Secret, true
		}
	}
	return nil, false
}
