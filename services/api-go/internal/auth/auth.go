// Package auth is the Go port of legacy NestJS web3-auth's
// Web3AuthGuard. It verifies the Bearer JWT issued by NestJS's
// /web3-auth/verify (HS256 signed with the shared JWT_SECRET) and
// exposes the recovered wallet address via request context.
//
// Non-production parity with the NestJS guard: when no Bearer is
// present and NODE_ENV != "production", the middleware falls back
// to the X-Wallet-Address header so dev/staging tools keep working
// against either backend. NODE_ENV=development additionally allows
// no header at all (zero-address fallback) — same as NestJS.
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

// Claims mirrors the Web3JwtPayload interface in web3-auth.service.ts.
// `sub` carries the lowercase wallet address; `chainId` is the chain
// the SIWE sign-in happened on; `type` is the literal "web3" marker
// distinguishing these from refresh / user-auth JWTs.
type Claims struct {
	Sub     string `json:"sub"`
	ChainID int    `json:"chainId"`
	Type    string `json:"type"`
	jwt.RegisteredClaims
}

// Verifier holds the HMAC secret. Built once at startup from JWT_SECRET.
type Verifier struct {
	secret []byte
}

// NewVerifier builds a Verifier. An empty secret is allowed for tests
// (Verify will always fail) but the cmd/api wiring refuses to start
// in production with a missing secret.
func NewVerifier(secret string) *Verifier {
	return &Verifier{secret: []byte(secret)}
}

// Errors returned by Verify. Handlers map these to 401.
var (
	ErrMissingToken = errors.New("auth: missing or malformed bearer token")
	ErrInvalidToken = errors.New("auth: token signature invalid or expired")
	ErrWrongType    = errors.New("auth: token type is not web3")
	ErrInvalidAddr  = errors.New("auth: token sub is not a valid address")
)

// addressRE matches the 0x + 40-hex shape NestJS isValidAddress checks.
var addressRE = regexp.MustCompile(`^0x[a-fA-F0-9]{40}$`)

// Verify parses and validates the Bearer token. Returns the claims
// (with a normalized lowercase address) or one of the sentinel errors.
func (v *Verifier) Verify(token string) (Claims, error) {
	if v == nil || len(v.secret) == 0 {
		return Claims{}, ErrInvalidToken
	}
	parsed, err := jwt.ParseWithClaims(token, &Claims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, ErrInvalidToken
		}
		return v.secret, nil
	})
	if err != nil || !parsed.Valid {
		return Claims{}, ErrInvalidToken
	}
	claims, ok := parsed.Claims.(*Claims)
	if !ok {
		return Claims{}, ErrInvalidToken
	}
	if claims.Type != "web3" {
		return Claims{}, ErrWrongType
	}
	if !addressRE.MatchString(claims.Sub) {
		return Claims{}, ErrInvalidAddr
	}
	claims.Sub = strings.ToLower(claims.Sub)
	return *claims, nil
}

// ctxKey is a private type to avoid collisions with other packages
// storing values on the same context.
type ctxKey int

const (
	addressKey ctxKey = iota
	chainIDKey
)

// AddressFromContext returns the wallet address the middleware placed
// on ctx, or "" if none.
func AddressFromContext(ctx context.Context) string {
	v, _ := ctx.Value(addressKey).(string)
	return v
}

// ChainIDFromContext returns the chain id the middleware placed on
// ctx, or 0 if none.
func ChainIDFromContext(ctx context.Context) int {
	v, _ := ctx.Value(chainIDKey).(int)
	return v
}

// WithAddress returns a context with the given address attached.
// Exposed mainly for tests that want to bypass the middleware.
func WithAddress(ctx context.Context, address string) context.Context {
	return context.WithValue(ctx, addressKey, strings.ToLower(address))
}

// Resolver-side sentinels for ResolveOwner. Mirror the NestJS
// resolveAuthenticatedOwnerAddress thrown exceptions:
//
//	BadRequest    → ErrInvalidRequestedAddress
//	Forbidden     → ErrOwnerMismatch
//	Unauthorized  → ErrMissingAuthenticated
var (
	ErrMissingAuthenticated    = errors.New("auth: missing authenticated wallet address")
	ErrInvalidRequestedAddress = errors.New("auth: requested address is not a valid wallet address")
	ErrOwnerMismatch           = errors.New("auth: requested address does not match the authenticated wallet")
)

// ResolveOwner mirrors common/utils/web3-request-owner.ts
// resolveAuthenticatedOwnerAddress. Use this in any guarded mutation
// that accepts an explicit `ownerAddress` / `address` body field — it
// lets callers pass their own address as a body field for parity with
// the older FE, while preventing an authenticated user from acting on
// behalf of someone else.
//
// Returns the lowercase, normalized address to use, or one of the
// sentinels above for the handler to map to 401/400/403.
func ResolveOwner(authenticated, requested string) (string, error) {
	auth := strings.TrimSpace(strings.ToLower(authenticated))
	if !addressRE.MatchString(auth) {
		return "", ErrMissingAuthenticated
	}
	if strings.TrimSpace(requested) == "" {
		return auth, nil
	}
	if !addressRE.MatchString(requested) {
		return "", ErrInvalidRequestedAddress
	}
	if strings.ToLower(requested) != auth {
		return "", ErrOwnerMismatch
	}
	return auth, nil
}

// Middleware returns a chi-compatible middleware that authenticates
// requests for guarded routes. It mirrors Web3AuthGuard.canActivate:
//
//  1. Bearer token in Authorization header → verified via JWT
//  2. If no Bearer and NODE_ENV != "production" → X-Wallet-Address header
//  3. If still none and NODE_ENV == "development" → zero address
//  4. Otherwise → 401
//
// The recovered (lowercase, normalized) address is placed on
// r.Context() under the addressKey, retrievable via AddressFromContext.
func Middleware(v *Verifier) func(http.Handler) http.Handler {
	prod := os.Getenv("NODE_ENV") == "production"
	dev := os.Getenv("NODE_ENV") == "development"
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			addr, chainID, ok := authenticate(r, v, prod, dev)
			if !ok {
				writeUnauthorized(w)
				return
			}
			ctx := context.WithValue(r.Context(), addressKey, addr)
			if chainID != 0 {
				ctx = context.WithValue(ctx, chainIDKey, chainID)
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// writeUnauthorized emits the Web3AuthGuard 401 as JSON. The deployed NestJS
// guard throws an UnauthorizedException whose body carries this message; the
// frontend reads `.message`. We mirror the access guard's {statusCode, message}
// shape (internal/userauth.writeError) rather than the older plaintext body so
// every Go guard denies access with a consistent, FE-consumable JSON envelope
// (the remaining NestJS envelope fields are an FE-layer concern — see
// docs/migration/backend-go/adr/0005-response-contract-raw-payloads.md and
// golden-run-2026-06-08.md blocker A).
func writeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"statusCode": http.StatusUnauthorized,
		"message":    "Missing or invalid web3 authorization",
	})
}

// authenticate runs the Web3AuthGuard precedence: Bearer → fallback
// header (non-prod) → zero address (dev only). Returns (address,
// chainID, ok). chainID is 0 when no JWT was presented.
func authenticate(r *http.Request, v *Verifier, prod, dev bool) (string, int, bool) {
	if token := extractBearer(r.Header.Get("Authorization")); token != "" {
		claims, err := v.Verify(token)
		if err != nil {
			return "", 0, false
		}
		return claims.Sub, claims.ChainID, true
	}
	if !prod {
		if hdr := r.Header.Get("X-Wallet-Address"); hdr != "" && addressRE.MatchString(hdr) {
			return strings.ToLower(hdr), 0, true
		}
		if dev {
			return "0x0000000000000000000000000000000000000000", 0, true
		}
	}
	return "", 0, false
}

func extractBearer(header string) string {
	if !strings.HasPrefix(header, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
}
