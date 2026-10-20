package auth

import (
	"context"
	"errors"
	"net/http"
)

type strictCredentialKey struct{}
type strictCredential struct{ token string }

// withCredential keeps the raw credential privately in the context so a
// handler that waited (for example on a database lock) can re-authenticate,
// including the session check, instead of trusting the earlier result.
func withCredential(ctx context.Context, token string, claims Claims) context.Context {
	ctx = context.WithValue(ctx, strictCredentialKey{}, strictCredential{token: token})
	ctx = context.WithValue(ctx, addressKey, claims.Sub)
	return context.WithValue(ctx, chainIDKey, claims.ChainID)
}

// StrictMiddleware guards every private web3 route. Exactly one Bearer
// credential is verified and its persistent session confirmed: 401 when the
// credential or session is invalid, 503 when the session store (or the
// verifier itself) is unavailable, so a private request never proceeds on
// unknown state.
func StrictMiddleware(v *Verifier) func(http.Handler) http.Handler {
	return StrictMiddlewareWithFailure(v, func(w http.ResponseWriter, _ *http.Request, err error) {
		if errors.Is(err, ErrSessionUnavailable) {
			writeUnavailable(w)
			return
		}
		writeUnauthorized(w)
	})
}

// StrictMiddlewareWithFailure lets versioned transports keep their own error
// envelope. failure receives ErrSessionUnavailable when the session store
// could not answer (map it to 503) and another error otherwise (401).
func StrictMiddlewareWithFailure(v *Verifier, failure func(http.ResponseWriter, *http.Request, error)) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "private, no-store")
			token, ok := bearer(r.Header.Values("Authorization"))
			if !ok {
				failure(w, r, ErrMissingToken)
				return
			}
			claims, err := v.Authenticate(r.Context(), token)
			if err != nil {
				failure(w, r, err)
				return
			}
			next.ServeHTTP(w, r.WithContext(withCredential(r.Context(), token, claims)))
		})
	}
}

// RevalidateStrict re-authenticates the credential stored by the middleware,
// including the session check, so revocation during a lock wait is seen. It
// never accepts WithAddress as proof; nil verifiers cannot reuse old results.
func RevalidateStrict(ctx context.Context, v *Verifier) (Claims, error) {
	if ctx == nil {
		return Claims{}, ErrMissingToken
	}
	credential, ok := ctx.Value(strictCredentialKey{}).(strictCredential)
	if !ok || credential.token == "" {
		return Claims{}, ErrMissingToken
	}
	return v.Authenticate(ctx, credential.token)
}

// Bearer extracts the single Bearer credential from a request, for handlers
// (such as optional enrichment and logout) that are not behind a middleware.
func Bearer(r *http.Request) (string, bool) {
	return bearer(r.Header.Values("Authorization"))
}
