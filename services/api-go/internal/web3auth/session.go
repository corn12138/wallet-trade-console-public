package web3auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
)

// SessionStore persists the session every issued token is bound to and ends
// it on logout. web3session.Store implements it against PostgreSQL.
type SessionStore interface {
	Create(ctx context.Context, ref auth.SessionRef, issuedAt, expiresAt time.Time) error
	Revoke(ctx context.Context, ref auth.SessionRef, reason, by string) (alreadyRevoked bool, err error)
}

// ErrSessionStoreUnavailable means no session could be persisted, so no token
// was issued: a token without a stored session could never be revoked.
var ErrSessionStoreUnavailable = errors.New("sign-in session could not be recorded")

// sessionWriteTimeout bounds persisting or revoking a session, so a stuck
// store fails sign-in or logout promptly (503) instead of holding requests.
const sessionWriteTimeout = 5 * time.Second

// issueSession persists a session for owner and only then signs its token.
// If signing fails after the row exists, the orphaned session is harmless: no
// token for it was ever returned.
func (s *Service) issueSession(ctx context.Context, owner string, chainID int, now time.Time) (VerifyResponse, error) {
	if s.credentials == nil || s.sessions == nil {
		return VerifyResponse{}, ErrSessionStoreUnavailable
	}
	id, err := newSessionID()
	if err != nil {
		return VerifyResponse{}, err
	}
	keyID, epoch := s.credentials.SessionKey()
	ref := auth.SessionRef{ID: id, Wallet: owner, ChainID: chainID, Version: 1, Epoch: epoch, KeyID: keyID}
	expires := now.Add(s.sessionTTL)
	writeCtx, cancel := context.WithTimeout(ctx, sessionWriteTimeout)
	defer cancel()
	if err = s.sessions.Create(writeCtx, ref, now, expires); err != nil {
		return VerifyResponse{}, errors.Join(ErrSessionStoreUnavailable, err)
	}
	token, err := s.credentials.Issue(ref, now, expires)
	if err != nil {
		return VerifyResponse{}, err
	}
	return VerifyResponse{Token: token, Address: owner, ChainID: chainID, SessionExpiresAt: expires.UTC().Format(time.RFC3339)}, nil
}

// LogoutResponse reports the server-side result. alreadyRevoked is true when
// the session had ended before this call; the token is invalid either way.
type LogoutResponse struct {
	Revoked        bool `json:"revoked"`
	AlreadyRevoked bool `json:"alreadyRevoked"`
}

// handleLogout revokes the caller's own session — exactly the one the token is
// bound to (id, wallet, chain, version, epoch, key). The token's signature and
// claims are verified, not whether its session is still live, so repeating
// logout is idempotent; a token that is invalid or expired answers 401 and is
// already unusable. A store failure answers 503 so the client reports
// "revocation unknown" instead of claiming the server ended the session.
func (s *Service) handleLogout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	token, ok := auth.Bearer(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "Missing or invalid authorization header")
		return
	}
	if s.credentials == nil || s.sessions == nil {
		writeError(w, http.StatusServiceUnavailable, "Web3 session store unavailable")
		return
	}
	claims, err := s.credentials.VerifyStrict(token)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "Missing or invalid web3 authorization")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), sessionWriteTimeout)
	defer cancel()
	already, err := s.sessions.Revoke(ctx, claims.Session(), "logout", "self")
	if err != nil {
		// Revocation is bound to the exact session the token names. If no such
		// session exists (removed, or the token's claims differ from it), the
		// token can never authenticate: nothing remains to revoke.
		if isNotOwned(err) {
			writeJSON(w, http.StatusOK, LogoutResponse{Revoked: true, AlreadyRevoked: true})
			return
		}
		writeError(w, http.StatusServiceUnavailable, "Web3 session store unavailable")
		return
	}
	writeJSON(w, http.StatusOK, LogoutResponse{Revoked: true, AlreadyRevoked: already})
}

// notOwned is implemented by store errors that mean "no such session for this
// wallet", keeping this package independent of the PostgreSQL store.
type notOwned interface{ NotOwned() bool }

func isNotOwned(err error) bool {
	var marker notOwned
	return errors.As(err, &marker) && marker.NotOwned()
}

func newSessionID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	v := hex.EncodeToString(raw[:])
	return v[:8] + "-" + v[8:12] + "-" + v[12:16] + "-" + v[16:20] + "-" + v[20:], nil
}
