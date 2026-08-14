// user_auth.go is the user-flow companion to auth.go's web3 (SIWE)
// JWT verifier. Where auth.go handles the wallet-addressed `type=web3`
// tokens issued by /web3-auth/verify, user_auth.go handles the
// `type=user` access + refresh tokens issued by /auth/login |
// /auth/register | /auth/refresh, signed under separate secrets and
// distinguishing access vs refresh by jwt.type.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

// UserClaims carries the user-flow JWT body. NestJS uses
// {sub, username, email, roles}. We add `type` so the verifier can
// tell access (`user`) from refresh (`refresh`) tokens.
type UserClaims struct {
	Sub      string   `json:"sub"`
	Username string   `json:"username"`
	Email    string   `json:"email"`
	Roles    []string `json:"roles"`
	Type     string   `json:"type"`
	jwt.RegisteredClaims
}

// UserVerifier validates user access / refresh JWTs. Two distinct
// secrets — same pattern as NestJS's separate JWT_SECRET / JWT_REFRESH_SECRET.
type UserVerifier struct {
	accessSecret  []byte
	refreshSecret []byte
	accessTTL     time.Duration
	refreshTTL    time.Duration
}

// NewUserVerifier builds the verifier. ttls default to 15m / 7d.
func NewUserVerifier(accessSecret, refreshSecret string, accessTTL, refreshTTL time.Duration) *UserVerifier {
	if accessTTL <= 0 {
		accessTTL = 15 * time.Minute
	}
	if refreshTTL <= 0 {
		refreshTTL = 7 * 24 * time.Hour
	}
	return &UserVerifier{
		accessSecret:  []byte(accessSecret),
		refreshSecret: []byte(refreshSecret),
		accessTTL:     accessTTL,
		refreshTTL:    refreshTTL,
	}
}

// Sentinel errors for the verifier.
var (
	ErrUserTokenInvalid   = errors.New("auth: user token signature invalid or expired")
	ErrUserTokenWrongType = errors.New("auth: user token has wrong type")
)

// IssueTokens signs an access ("user") and a refresh ("refresh") token.
func (v *UserVerifier) IssueTokens(userID, username, email string, roles []string) (access, refresh string, err error) {
	now := time.Now()
	access, err = v.sign(userID, username, email, roles, "user", v.accessSecret, now.Add(v.accessTTL))
	if err != nil {
		return "", "", err
	}
	refresh, err = v.sign(userID, username, email, roles, "refresh", v.refreshSecret, now.Add(v.refreshTTL))
	if err != nil {
		return "", "", err
	}
	return access, refresh, nil
}

func (v *UserVerifier) sign(userID, username, email string, roles []string, tokenType string, secret []byte, expires time.Time) (string, error) {
	if len(secret) == 0 {
		return "", fmt.Errorf("auth: signing secret not configured for %q", tokenType)
	}
	claims := UserClaims{
		Sub:      userID,
		Username: username,
		Email:    email,
		Roles:    roles,
		Type:     tokenType,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(expires),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(secret)
}

// VerifyAccess parses an access ("user") JWT. ErrUserTokenInvalid on
// bad signature / expiry; ErrUserTokenWrongType when type != "user".
func (v *UserVerifier) VerifyAccess(token string) (UserClaims, error) {
	return v.verify(token, v.accessSecret, "user")
}

// VerifyRefresh — same but for refresh tokens.
func (v *UserVerifier) VerifyRefresh(token string) (UserClaims, error) {
	return v.verify(token, v.refreshSecret, "refresh")
}

func (v *UserVerifier) verify(token string, secret []byte, expectedType string) (UserClaims, error) {
	if v == nil || len(secret) == 0 {
		return UserClaims{}, ErrUserTokenInvalid
	}
	parsed, err := jwt.ParseWithClaims(token, &UserClaims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, ErrUserTokenInvalid
		}
		return secret, nil
	})
	if err != nil || !parsed.Valid {
		return UserClaims{}, ErrUserTokenInvalid
	}
	claims, ok := parsed.Claims.(*UserClaims)
	if !ok {
		return UserClaims{}, ErrUserTokenInvalid
	}
	if claims.Type != expectedType {
		return UserClaims{}, ErrUserTokenWrongType
	}
	return *claims, nil
}

// bcryptInput mirrors node bcrypt's silent 72-byte truncation. Go's
// x/crypto/bcrypt REFUSES inputs >72 bytes instead, which made every
// classic login/register/refresh 500 on Go: issueAndPersist bcrypts the
// ~200-byte refresh JWT (caught 2026-07-02 by the classic-auth-flow smoke;
// "bcrypt: password length exceeds 72 bytes"). The deployed NestJS
// (`bcrypt.hash(refreshToken, 10)` in auth.service.ts) stores
// bcrypt(token[:72]), so truncating here (a) unbreaks the Go flow and
// (b) keeps hashes written by either stack verifiable on the other during
// the cutover window. Security note: for refresh tokens the bcrypt pin is
// defense-in-depth AFTER JWT signature verification (userauth.Refresh
// verifies the signature first), so prefix truncation does not weaken the
// auth decision.
func bcryptInput(s string) []byte {
	b := []byte(s)
	if len(b) > 72 {
		b = b[:72]
	}
	return b
}

// HashPassword wraps bcrypt with cost 10 — matches `bcrypt.hash(p, 10)`
// in auth.service.ts, including node bcrypt's 72-byte truncation.
func HashPassword(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword(bcryptInput(password), 10)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// ComparePassword returns nil iff the plaintext matches the hash (with the
// same node-parity 72-byte truncation as HashPassword).
func ComparePassword(hash, password string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), bcryptInput(password))
}

// --- CSRF token (HMAC-SHA256) ---
//
// Token format: base64(userId|expires|base64(hmac_sha256(secret, userId|expires)))
// Matches legacy NestJS common/services/csrf.service.ts.

// CsrfSigner generates / validates the XSRF-TOKEN cookies.
type CsrfSigner struct {
	secret []byte
	ttl    time.Duration
}

// NewCsrfSigner builds a signer. A zero ttl defaults to 24h.
func NewCsrfSigner(secret string, ttl time.Duration) *CsrfSigner {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return &CsrfSigner{secret: []byte(secret), ttl: ttl}
}

// GenerateToken — returns the empty string when no secret is configured
// (matches the dev/test path where CSRF is optional).
func (c *CsrfSigner) GenerateToken(userID string) string {
	if c == nil || len(c.secret) == 0 {
		return ""
	}
	expires := time.Now().Add(c.ttl).UnixMilli()
	data := userID + "|" + strconv.FormatInt(expires, 10)
	sig := hmacSHA256(c.secret, data)
	return base64.StdEncoding.EncodeToString([]byte(data + "|" + sig))
}

// ValidateToken — returns true iff the token is well-formed, fresh,
// and signed for userID with the same secret.
func (c *CsrfSigner) ValidateToken(token, userID string) bool {
	if c == nil || len(c.secret) == 0 {
		return false
	}
	raw, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		return false
	}
	parts := strings.Split(string(raw), "|")
	if len(parts) != 3 {
		return false
	}
	if parts[0] != userID {
		return false
	}
	expires, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return false
	}
	if time.Now().UnixMilli() > expires {
		return false
	}
	data := parts[0] + "|" + parts[1]
	expected := hmacSHA256(c.secret, data)
	return subtle.ConstantTimeCompare([]byte(parts[2]), []byte(expected)) == 1
}

func hmacSHA256(secret []byte, data string) string {
	h := hmac.New(sha256.New, secret)
	h.Write([]byte(data))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// --- request-context helpers for the user JWT ---

const (
	userIDKey ctxKey = 100 + iota
	userRolesKey
	usernameKey
	userEmailKey
)

// WithUser stores the user claims on the context (used by the user JWT
// middleware before delegating to downstream handlers).
func WithUser(ctx context.Context, claims UserClaims) context.Context {
	ctx = context.WithValue(ctx, userIDKey, claims.Sub)
	ctx = context.WithValue(ctx, userRolesKey, append([]string{}, claims.Roles...))
	ctx = context.WithValue(ctx, usernameKey, claims.Username)
	ctx = context.WithValue(ctx, userEmailKey, claims.Email)
	return ctx
}

// UserIDFromContext returns the user id placed by the middleware, or "".
func UserIDFromContext(ctx context.Context) string {
	v, _ := ctx.Value(userIDKey).(string)
	return v
}

// UserRolesFromContext returns the role list, or nil.
func UserRolesFromContext(ctx context.Context) []string {
	v, _ := ctx.Value(userRolesKey).([]string)
	return v
}

// UsernameFromContext returns the cached username.
func UsernameFromContext(ctx context.Context) string {
	v, _ := ctx.Value(usernameKey).(string)
	return v
}

// UserEmailFromContext returns the cached email.
func UserEmailFromContext(ctx context.Context) string {
	v, _ := ctx.Value(userEmailKey).(string)
	return v
}

// _ ensures json/encoding is wired — useful if future helpers move
// claim serialization here.
var _ = json.Marshal
