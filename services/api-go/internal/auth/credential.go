package auth

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims is the web3 (SIWE) bearer token payload. `sub` is the lowercase
// wallet; `sid`, `sver` and `epoch` bind the token to one persistent session,
// its version and the auth epoch it was issued under. KeyID is taken from the
// verified token header and is never serialized into the payload.
type Claims struct {
	Sub            string `json:"sub"`
	ChainID        int    `json:"chainId"`
	Type           string `json:"type"`
	SessionID      string `json:"sid"`
	SessionVersion int    `json:"sver"`
	AuthEpoch      int64  `json:"epoch"`
	KeyID          string `json:"-"`
	jwt.RegisteredClaims
}

// Session is the persistent session these verified claims are bound to.
func (c Claims) Session() SessionRef {
	return SessionRef{ID: c.SessionID, Wallet: c.Sub, ChainID: c.ChainID, Version: c.SessionVersion, Epoch: c.AuthEpoch, KeyID: c.KeyID}
}

// Errors returned by verification. ErrSessionUnavailable means the session
// store could not answer: private requests must fail closed with 503, never
// fall back to anonymous or to a cached positive answer.
var (
	ErrMissingToken       = errors.New("auth: missing or malformed bearer token")
	ErrInvalidToken       = errors.New("auth: token signature, claims or key invalid or expired")
	ErrWrongType          = errors.New("auth: token type is not web3")
	ErrInvalidAddr        = errors.New("auth: token sub is not a valid address")
	ErrSessionInvalid     = errors.New("auth: session revoked, expired, superseded or unknown")
	ErrSessionUnavailable = errors.New("auth: session state cannot be confirmed")
)

var (
	addressRE   = regexp.MustCompile(`^0x[a-fA-F0-9]{40}$`)
	sessionIDRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
)

const zeroAddress = "0x0000000000000000000000000000000000000000"

// sessionCheckTimeout bounds one session lookup so a slow store fails closed
// quickly instead of holding private requests open.
const sessionCheckTimeout = 2 * time.Second

// Verifier issues and verifies web3 credentials under one Policy.
type Verifier struct{ policy Policy }

// NewVerifier validates the policy. There is deliberately no secret-only
// constructor: every verifier enforces issuer, audience, key and epoch.
func NewVerifier(policy Policy) (*Verifier, error) {
	if err := policy.validate(); err != nil {
		return nil, err
	}
	return &Verifier{policy: policy}, nil
}

// SessionKey reports the key id and epoch a new session must be created with
// so that Issue can sign a token for it.
func (v *Verifier) SessionKey() (keyID string, epoch int64) {
	return v.policy.Current.ID, v.policy.Epoch
}

// Issue signs a token for an already persisted session. The session must have
// been created with the current key and epoch; issuing for anything else would
// produce a token the verifier itself rejects.
func (v *Verifier) Issue(ref SessionRef, issuedAt, expiresAt time.Time) (string, error) {
	if v == nil {
		return "", errors.New("auth: verifier not configured")
	}
	p := v.policy
	if ref.KeyID != p.Current.ID || ref.Epoch != p.Epoch || !sessionIDRE.MatchString(ref.ID) || ref.Version < 1 || ref.ChainID <= 0 || !addressRE.MatchString(ref.Wallet) || !expiresAt.After(issuedAt) {
		return "", errors.New("auth: session does not match the current issuing policy")
	}
	claims := Claims{Sub: strings.ToLower(ref.Wallet), ChainID: ref.ChainID, Type: "web3", SessionID: ref.ID, SessionVersion: ref.Version, AuthEpoch: ref.Epoch,
		RegisteredClaims: jwt.RegisteredClaims{Issuer: p.Issuer, Audience: jwt.ClaimStrings{p.Audience}, IssuedAt: jwt.NewNumericDate(issuedAt), ExpiresAt: jwt.NewNumericDate(expiresAt)}}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	token.Header["kid"] = p.Current.ID
	return token.SignedString(p.Current.Secret)
}

// VerifyStrict checks the token itself: HS256 only, a known unretired key,
// exact issuer and single audience, required exp and iat (within leeway),
// web3 type, a non-zero wallet, a positive chain, a session id/version and the
// current auth epoch. It does not consult the session store; private access
// must use Authenticate.
func (v *Verifier) VerifyStrict(token string) (Claims, error) {
	if v == nil {
		return Claims{}, ErrInvalidToken
	}
	p := v.policy
	parser := jwt.NewParser(jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired(), jwt.WithIssuedAt(),
		jwt.WithIssuer(p.Issuer), jwt.WithAudience(p.Audience), jwt.WithLeeway(p.Leeway), jwt.WithTimeFunc(p.now))
	var kid string
	parsed, err := parser.ParseWithClaims(token, &Claims{}, func(t *jwt.Token) (any, error) {
		kid, _ = t.Header["kid"].(string)
		secret, ok := p.verificationKey(kid)
		if !ok {
			return nil, ErrInvalidToken
		}
		return secret, nil
	})
	if err != nil || parsed == nil || !parsed.Valid {
		return Claims{}, ErrInvalidToken
	}
	claims, ok := parsed.Claims.(*Claims)
	if !ok || claims.IssuedAt == nil || len(claims.Audience) != 1 || claims.ChainID <= 0 {
		return Claims{}, ErrInvalidToken
	}
	if claims.Type != "web3" {
		return Claims{}, ErrWrongType
	}
	if !addressRE.MatchString(claims.Sub) || strings.ToLower(claims.Sub) == zeroAddress {
		return Claims{}, ErrInvalidAddr
	}
	if !sessionIDRE.MatchString(claims.SessionID) || claims.SessionVersion < 1 || claims.AuthEpoch != p.Epoch {
		return Claims{}, ErrInvalidToken
	}
	claims.Sub = strings.ToLower(claims.Sub)
	claims.KeyID = kid
	return *claims, nil
}

// Authenticate verifies the token and confirms its persistent session. A
// store (or verifier) that cannot answer yields ErrSessionUnavailable; a
// missing, revoked, expired or mismatched session yields ErrSessionInvalid.
func (v *Verifier) Authenticate(ctx context.Context, token string) (Claims, error) {
	if v == nil {
		// No configured verifier is an outage, not a bad credential.
		return Claims{}, ErrSessionUnavailable
	}
	claims, err := v.VerifyStrict(token)
	if err != nil {
		return Claims{}, err
	}
	if v.policy.Sessions == nil {
		return Claims{}, ErrSessionUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, sessionCheckTimeout)
	defer cancel()
	valid, err := v.policy.Sessions.CheckSession(ctx, claims.Session())
	if err != nil {
		return Claims{}, ErrSessionUnavailable
	}
	if !valid {
		return Claims{}, ErrSessionInvalid
	}
	return claims, nil
}
