package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/web3auth"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/web3session"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// deploymentProfile is the validated runtime environment the web3 security
// defaults derive from (FQ-01). An empty, misspelled or unknown NODE_ENV
// refuses to start: guessing would silently pick either the production or the
// development behaviour.
type deploymentProfile struct {
	env      string
	loopback bool
}

var deploymentEnvironments = map[string]bool{"production": true, "staging": true, "test": true, "development": true}

func resolveDeploymentProfile(getenv func(string) string, listen string) (deploymentProfile, error) {
	env := getenv("NODE_ENV")
	if !deploymentEnvironments[env] {
		if len(env) > 32 {
			env = env[:32] + "…"
		}
		return deploymentProfile{}, fmt.Errorf("NODE_ENV must be exactly one of production, staging, test or development (got %q)", env)
	}
	return deploymentProfile{env: env, loopback: loopbackListen(listen)}, nil
}

// localTest is the only profile allowed process-local shortcuts (an in-memory
// SIWE nonce map): a development or test process reachable only on loopback.
func (p deploymentProfile) localTest() bool {
	return (p.env == "development" || p.env == "test") && p.loopback
}

func loopbackListen(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

const (
	defaultJWTLeeway = 30 * time.Second
	// maxPreviousKeyWindow bounds how far ahead a rotated-out key may stay
	// valid, so a forgotten setting cannot keep an old key alive indefinitely.
	maxPreviousKeyWindow = 30 * 24 * time.Hour
)

// web3CredentialPolicy reads the trusted web3 credential configuration. Every
// value is required — there is no default issuer, audience, key or epoch — and
// the signing secret is never shared with user-auth. Error messages name the
// variable, never its value.
func web3CredentialPolicy(getenv func(string) string, now time.Time) (auth.Policy, error) {
	var missing []string
	required := func(key string) string {
		value := strings.TrimSpace(getenv(key))
		if value == "" {
			missing = append(missing, key)
		}
		return value
	}
	issuer := required("WEB3_JWT_ISSUER")
	audience := required("WEB3_JWT_AUDIENCE")
	keyID := required("WEB3_JWT_KEY_ID")
	secret := required("WEB3_JWT_SECRET")
	epochText := required("WEB3_AUTH_EPOCH")
	if len(missing) > 0 {
		return auth.Policy{}, fmt.Errorf("%s required", strings.Join(missing, ", "))
	}
	epoch, err := strconv.ParseInt(epochText, 10, 64)
	if err != nil || epoch < 1 {
		return auth.Policy{}, errors.New("WEB3_AUTH_EPOCH must be a positive integer")
	}
	for _, other := range []string{"JWT_SECRET", "JWT_REFRESH_SECRET"} {
		if value := strings.TrimSpace(getenv(other)); value != "" && value == secret {
			return auth.Policy{}, fmt.Errorf("WEB3_JWT_SECRET must differ from %s", other)
		}
	}
	leeway := defaultJWTLeeway
	if raw := strings.TrimSpace(getenv("WEB3_JWT_LEEWAY_SECONDS")); raw != "" {
		seconds, err := strconv.Atoi(raw)
		if err != nil || seconds < 0 || seconds > 300 {
			return auth.Policy{}, errors.New("WEB3_JWT_LEEWAY_SECONDS must be an integer from 0 to 300")
		}
		leeway = time.Duration(seconds) * time.Second
	}
	policy := auth.Policy{Issuer: issuer, Audience: audience, Current: auth.Key{ID: keyID, Secret: []byte(secret)}, Epoch: epoch, Leeway: leeway}

	previousID := strings.TrimSpace(getenv("WEB3_JWT_PREVIOUS_KEY_ID"))
	previousSecret := strings.TrimSpace(getenv("WEB3_JWT_PREVIOUS_SECRET"))
	retireText := strings.TrimSpace(getenv("WEB3_JWT_PREVIOUS_RETIRE_AT"))
	switch {
	case previousID == "" && previousSecret == "" && retireText == "":
	case previousID == "" || previousSecret == "" || retireText == "":
		return auth.Policy{}, errors.New("WEB3_JWT_PREVIOUS_KEY_ID, WEB3_JWT_PREVIOUS_SECRET and WEB3_JWT_PREVIOUS_RETIRE_AT must be set together")
	default:
		retire, err := time.Parse(time.RFC3339, retireText)
		if err != nil {
			return auth.Policy{}, errors.New("WEB3_JWT_PREVIOUS_RETIRE_AT must be an RFC 3339 time")
		}
		if retire.After(now.Add(maxPreviousKeyWindow)) {
			return auth.Policy{}, errors.New("WEB3_JWT_PREVIOUS_RETIRE_AT may be at most 30 days ahead")
		}
		if previousSecret == secret {
			return auth.Policy{}, errors.New("WEB3_JWT_PREVIOUS_SECRET must differ from WEB3_JWT_SECRET")
		}
		policy.Previous = []auth.Key{{ID: previousID, Secret: []byte(previousSecret), Retire: retire}}
	}
	return policy, nil
}

// web3Credentials is the one verifier/issuer pair every guard, /me, logout
// and /verify share, plus the session store it is bound to.
type web3Credentials struct {
	verifier *auth.Verifier
	// sessions is a true nil without a database, so the SIWE service treats
	// it as unavailable instead of calling a nil pool.
	sessions web3auth.SessionStore
	// adminsGranted reports whether the admin allowlists were (re)granted for
	// the current auth epoch; otherwise every admin route is deny-all.
	adminsGranted bool
}

// buildWeb3Credentials resolves the credential policy and session store and
// refuses to start on an epoch that went backwards relative to the sessions
// already stored. Without a database every private web3 route and sign-in
// answers 503 rather than trusting tokens it cannot check.
func buildWeb3Credentials(ctx context.Context, getenv func(string) string, pool *pgxpool.Pool) (web3Credentials, error) {
	policy, err := web3CredentialPolicy(getenv, time.Now())
	if err != nil {
		return web3Credentials{}, fmt.Errorf("web3 credentials: %w", err)
	}
	// Configuration errors are reported before the database is consulted, so
	// they surface as themselves whatever state the database is in.
	if _, err = auth.NewVerifier(policy); err != nil {
		return web3Credentials{}, fmt.Errorf("web3 credentials: %w", err)
	}
	granted, err := adminGrantCurrent(getenv, policy.Epoch)
	if err != nil {
		return web3Credentials{}, err
	}
	var sessions web3auth.SessionStore
	storeKind := "unavailable"
	if pool != nil {
		store := web3session.New(pool)
		stored, err := store.MaxEpoch(ctx)
		if err != nil {
			return web3Credentials{}, fmt.Errorf("web3 credentials: cannot read the stored auth epoch (%s); apply the web3_auth_sessions migration first", describeDBError(err))
		}
		if stored > policy.Epoch {
			return web3Credentials{}, fmt.Errorf("web3 credentials: WEB3_AUTH_EPOCH=%d is below epoch %d already used by stored sessions; the epoch never goes back", policy.Epoch, stored)
		}
		policy.Sessions = store
		sessions = store
		storeKind = "postgres"
	} else {
		slog.Warn("web3 sessions unavailable without a database: sign-in and every private web3 route answer 503")
	}
	verifier, err := auth.NewVerifier(policy)
	if err != nil {
		return web3Credentials{}, fmt.Errorf("web3 credentials: %w", err)
	}
	previous := ""
	if len(policy.Previous) == 1 {
		previous = policy.Previous[0].ID
	}
	slog.Info("web3 credentials ready", "issuer", policy.Issuer, "audience", policy.Audience,
		"key_id", policy.Current.ID, "previous_key_id", previous, "auth_epoch", policy.Epoch,
		"sessions", storeKind, "admin_allowlists_active", granted)
	return web3Credentials{verifier: verifier, sessions: sessions, adminsGranted: granted}, nil
}

// adminGrantCurrent implements "high privilege is re-granted independently"
// after an auth-epoch cutoff (FQ-06): the admin allowlists apply only while
// WEB3_ADMIN_GRANT_EPOCH names the current epoch. Advancing WEB3_AUTH_EPOCH
// therefore closes every admin route until an operator re-grants it.
func adminGrantCurrent(getenv func(string) string, epoch int64) (bool, error) {
	raw := strings.TrimSpace(getenv("WEB3_ADMIN_GRANT_EPOCH"))
	if raw == "" {
		slog.Warn("admin allowlists inactive: WEB3_ADMIN_GRANT_EPOCH is not set", "auth_epoch", epoch)
		return false, nil
	}
	grant, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || grant < 1 {
		return false, errors.New("web3 credentials: WEB3_ADMIN_GRANT_EPOCH must be a positive integer")
	}
	if grant != epoch {
		slog.Warn("admin allowlists inactive: they were granted for another auth epoch; review CAMPAIGN_ADMIN_WALLETS and I18N_ADMIN_WALLETS, then re-grant",
			"grant_epoch", grant, "auth_epoch", epoch)
		return false, nil
	}
	return true, nil
}

// describeDBError keeps startup errors free of connection details.
func describeDBError(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return "SQLSTATE " + pgErr.Code
	}
	return "database unavailable"
}
