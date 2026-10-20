// Package web3session persists SIWE sessions (FQ-01) in web3_auth_sessions.
// A token is valid only while its row exists and matches it exactly; logout
// and operator revocation are one-way column updates enforced by a trigger, so
// they take effect on the next request on every instance and survive restarts.
package web3session

import (
	"context"
	"errors"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Revocation reasons stored with a revoked session.
const (
	ReasonLogout   = "logout"
	ReasonOperator = "operator"
)

// ErrNotOwned is returned when a revocation names a session that does not
// exist for this wallet. It implements NotOwned() so callers can recognise it
// without importing this package.
var ErrNotOwned error = notOwnedError{}

type notOwnedError struct{}

func (notOwnedError) Error() string  { return "web3session: session does not belong to this wallet" }
func (notOwnedError) NotOwned() bool { return true }

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

var _ auth.SessionChecker = (*Store)(nil)

// Create persists a session before any token for it is issued; if this fails
// no token may be returned to the client.
func (s *Store) Create(ctx context.Context, ref auth.SessionRef, issuedAt, expiresAt time.Time) error {
	if s == nil || s.pool == nil {
		return errors.New("web3session: store unavailable")
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO web3_auth_sessions(id,wallet_address,chain_id,session_version,auth_epoch,key_id,issued_at,expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, ref.ID, ref.Wallet, ref.ChainID, ref.Version, ref.Epoch, ref.KeyID, issuedAt, expiresAt)
	return err
}

// CheckSession implements auth.SessionChecker. Expiry is judged by the
// database clock, the same clock every instance shares.
func (s *Store) CheckSession(ctx context.Context, ref auth.SessionRef) (bool, error) {
	if s == nil || s.pool == nil {
		return false, errors.New("web3session: store unavailable")
	}
	var valid bool
	err := s.pool.QueryRow(ctx, `SELECT revoked_at IS NULL AND expires_at > clock_timestamp()
		AND wallet_address=$2 AND chain_id=$3 AND session_version=$4 AND auth_epoch=$5 AND key_id=$6
		FROM web3_auth_sessions WHERE id=$1`, ref.ID, ref.Wallet, ref.ChainID, ref.Version, ref.Epoch, ref.KeyID).Scan(&valid)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return valid, err
}

// Revoke ends exactly the session ref names: same id, wallet, chain, version,
// epoch and key. Repeating it is a no-op that reports alreadyRevoked; a
// reference that matches no stored session (another wallet's id, or a token
// whose claims differ from its session) is ErrNotOwned and changes nothing.
func (s *Store) Revoke(ctx context.Context, ref auth.SessionRef, reason, by string) (alreadyRevoked bool, err error) {
	if s == nil || s.pool == nil {
		return false, errors.New("web3session: store unavailable")
	}
	const match = `id=$1 AND wallet_address=$2 AND chain_id=$3 AND session_version=$4 AND auth_epoch=$5 AND key_id=$6`
	tag, err := s.pool.Exec(ctx, `UPDATE web3_auth_sessions SET revoked_at=clock_timestamp(), revoke_reason=$7, revoked_by=$8
		WHERE `+match+` AND revoked_at IS NULL`, ref.ID, ref.Wallet, ref.ChainID, ref.Version, ref.Epoch, ref.KeyID, reason, by)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 1 {
		return false, nil
	}
	var revoked bool
	err = s.pool.QueryRow(ctx, `SELECT revoked_at IS NOT NULL FROM web3_auth_sessions WHERE `+match,
		ref.ID, ref.Wallet, ref.ChainID, ref.Version, ref.Epoch, ref.KeyID).Scan(&revoked)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrNotOwned
	}
	if err != nil {
		return false, err
	}
	// The exact session exists and is unrevoked only if it changed between the
	// two statements, which cannot happen for one session revoked once.
	return revoked, nil
}

// RevokeWallet ends every live session of wallet (operator revocation) and
// returns how many were revoked.
func (s *Store) RevokeWallet(ctx context.Context, wallet, by string) (int64, error) {
	if s == nil || s.pool == nil {
		return 0, errors.New("web3session: store unavailable")
	}
	tag, err := s.pool.Exec(ctx, `UPDATE web3_auth_sessions SET revoked_at=clock_timestamp(), revoke_reason=$2, revoked_by=$3
		WHERE wallet_address=$1 AND revoked_at IS NULL AND expires_at > clock_timestamp()`, wallet, ReasonOperator, by)
	return tag.RowsAffected(), err
}

// MaxEpoch reports the highest auth epoch any stored session was issued under.
// cmd/api refuses to start when its configured epoch is lower, which would mean
// the trusted epoch was rolled back while the database kept newer sessions.
func (s *Store) MaxEpoch(ctx context.Context) (int64, error) {
	if s == nil || s.pool == nil {
		return 0, errors.New("web3session: store unavailable")
	}
	var epoch int64
	err := s.pool.QueryRow(ctx, `SELECT coalesce(max(auth_epoch),0) FROM web3_auth_sessions`).Scan(&epoch)
	return epoch, err
}

// Summary is the operator view of the store: no wallet identities.
type Summary struct {
	MaxEpoch     int64 `json:"maxEpoch"`
	LiveSessions int64 `json:"liveSessions"`
}

// Summary reports the highest stored auth epoch and how many sessions are
// live by the database clock.
func (s *Store) Summary(ctx context.Context) (Summary, error) {
	if s == nil || s.pool == nil {
		return Summary{}, errors.New("web3session: store unavailable")
	}
	var out Summary
	err := s.pool.QueryRow(ctx, `SELECT coalesce(max(auth_epoch),0), count(*) FILTER (WHERE revoked_at IS NULL AND expires_at > clock_timestamp())
		FROM web3_auth_sessions`).Scan(&out.MaxEpoch, &out.LiveSessions)
	return out, err
}
