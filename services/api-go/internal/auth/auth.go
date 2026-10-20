// Package auth verifies web3 (SIWE) bearer credentials for guarded routes and
// exposes the authenticated wallet through the request context.
//
// Every credential is checked under one trusted Policy (HS256, issuer,
// audience, signing key, auth epoch) and against its persistent session, so
// logout and operator revocation take effect immediately and a database
// restore is cut off by the epoch. There is no development identity in any
// environment: the former zero-address and X-Wallet-Address fallbacks are
// gone, and tests authenticate with real tokens from package authtest.
package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

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

// bearer accepts exactly one "Bearer <token>" header value.
func bearer(headers []string) (string, bool) {
	if len(headers) != 1 {
		return "", false
	}
	parts := strings.Fields(headers[0])
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return "", false
	}
	return parts[1], true
}

// writeUnauthorized emits the web3 guard 401 as the JSON envelope the
// frontend reads (`.message`), shared by every Go guard.
func writeUnauthorized(w http.ResponseWriter) {
	writeAuthError(w, http.StatusUnauthorized, "Missing or invalid web3 authorization")
}

// writeUnavailable tells the client the session could not be confirmed. It is
// not a logout signal: clients keep the credential and retry later.
func writeUnavailable(w http.ResponseWriter) {
	writeAuthError(w, http.StatusServiceUnavailable, "Web3 session verification unavailable")
}

func writeAuthError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"statusCode": status, "message": message})
}
