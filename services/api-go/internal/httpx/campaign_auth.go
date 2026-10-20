package httpx

import (
	"net/http"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
)

// sessionWalletResolver returns the wallet of a request whose single Bearer
// token authenticates against its live persistent session, or "" on any
// failure — including an unavailable session store — so an optional
// enrichment falls back to the anonymous view, which carries no private data.
// Legacy campaign reads run the strict guard first, so a bad credential there
// is already 401/503; follow-state and the mobile settings aggregate answer
// their public/guest shape.
func sessionWalletResolver(verifier *auth.Verifier) func(*http.Request) string {
	return func(r *http.Request) string {
		token, ok := auth.Bearer(r)
		if !ok {
			return ""
		}
		claims, err := verifier.Authenticate(r.Context(), token)
		if err != nil {
			return ""
		}
		return claims.Sub
	}
}
