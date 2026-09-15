package httpx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/ai"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/article"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/contractconfig"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/deployments"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/txreview"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/web3auth"
	"github.com/golang-jwt/jwt/v5"
)

const testWeb3Secret = "test-web3-secret"

// emptyLoader satisfies markets.DeploymentsLoader so markets/discover mount.
type emptyLoader struct{}

func (emptyLoader) Load() (map[int]deployments.ChainConfig, error) {
	return map[int]deployments.ChainConfig{}, nil
}

// TestRouter_GuardParity locks in the auth boundary documented in
// docs/migration/backend-go/guard-parity.md: the modules that are entirely
// non-public in the deployed NestJS Web3AppModule must 401 without a token in
// Go, while public modules stay open. Production posture disables the web3
// guard's non-prod X-Wallet-Address fallback so both guard types are strict.
func TestRouter_GuardParity(t *testing.T) {
	t.Setenv("NODE_ENV", "production")

	txReviewSvc := &txreview.Service{}
	userVerifier := auth.NewUserVerifier("test-access-secret", "test-refresh-secret", time.Hour, time.Hour)
	r := NewRouter(Deps{
		MarketsLoader:  emptyLoader{},
		AuthVerifier:   auth.NewVerifier(testWeb3Secret),
		UserVerifier:   userVerifier,
		CsrfSigner:     &auth.CsrfSigner{},
		ContractConfig: &contractconfig.Service{},
		TxReview:       txReviewSvc,
		AIExplain:      ai.NewService(ai.Config{}, nil, txReviewSvc),
		Web3Auth:       &web3auth.Service{},
		ArticleRepo:    &article.Repository{},
	})

	code := func(method, path string) int {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		return rec.Code
	}
	codeWithBearer := func(method, path, token string) int {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec.Code
	}
	codeWithBearerBody := func(method, path, token, body string) int {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec.Code
	}

	// Access-token guarded (NestJS global JwtAuthGuard — no @Public) → 401 sans token.
	for _, tc := range []struct{ method, path string }{
		// staking pool CATALOG reads (/pools, /pools/stats, /pools/{id}) are now
		// Go-canonical PUBLIC (asserted in the public block below). The admin
		// mutations, user-specific read, and user writes STAY access-guarded:
		{"POST", "/api/staking/pools"},
		{"PUT", "/api/staking/pools/abc"},
		{"GET", "/api/staking/user/0x000000000000000000000000000000000000dEaD"},
		{"POST", "/api/staking/stake"},
		{"POST", "/api/staking/unstake/abc"},
		// (swap is now Go-canonical PUBLIC — asserted in the public-routes block
		// below; it was access-guarded before the 2026-06-17 cut.)
		// earn build-tx POSTs stay access-guarded after GET /products was made
		// Go-canonical PUBLIC (2026-06-15 cut). The public read is asserted in
		// the public-routes block below.
		{"POST", "/api/earn/build-deposit"},
		{"POST", "/api/earn/build-withdraw"},
		// (bridge is now Go-canonical PUBLIC — asserted in the public-routes
		// block below; it was access-guarded before the 2026-06-17 cut.)
		// (paper-orders is DISABLED by default since the papertrade dev-only
		// boundary — asserted in its own block below, not here.)
		// (token GET reads are now Go-canonical PUBLIC — asserted in the
		// public-routes block below; POST /api/token web3-guard asserted next.)
		// trading account-scoped routes require the product's SIWE wallet session.
		{"GET", "/api/trading/positions/0xabc"},
		{"GET", "/api/trading/orders/0xabc"},
		{"GET", "/api/trading/history/0xabc"},
	} {
		if got := code(tc.method, tc.path); got != http.StatusUnauthorized {
			t.Errorf("%s %s = %d, want 401 (SIWE wallet-guarded)", tc.method, tc.path, got)
		}
	}

	// token: POST /api/token (create) is web3-guarded (NestJS
	// @UseGuards(Web3AuthGuard)); the GET reads are Go-canonical PUBLIC (asserted
	// in the public-routes block below — a departure from the NestJS global
	// JwtAuthGuard that 401s them, since the FE loads them with plain fetch).
	if got := code("POST", "/api/token"); got != http.StatusUnauthorized {
		t.Errorf("POST /api/token (no token) = %d, want 401 (web3-guarded create)", got)
	}

	// paper-orders: the fake-order seeder is dev-only. Default deployments
	// (PaperTradeEnabled unset) answer an explicit 503 for BOTH the read and
	// the write — a fake book must never be live by default.
	for _, method := range []string{"GET", "POST"} {
		if got := code(method, "/api/paper-orders"); got != http.StatusServiceUnavailable {
			t.Errorf("%s /api/paper-orders (disabled) = %d, want 503", method, got)
		}
	}

	// media: uploads are web3-guarded mutations; /status stays public.
	if got := code("POST", "/api/media/upload"); got != http.StatusUnauthorized {
		t.Errorf("POST /api/media/upload (no token) = %d, want 401 (web3-guarded)", got)
	}
	if got := code("GET", "/api/media/status"); got != http.StatusOK {
		t.Errorf("GET /api/media/status = %d, want 200 (public)", got)
	}

	// Web3-token guarded (NestJS @Public + Web3AuthGuard) → 401 sans token.
	for _, p := range []string{
		"/api/portfolio/summary", "/api/activity",
	} {
		if got := code("GET", p); got != http.StatusUnauthorized {
			t.Errorf("GET %s = %d, want 401 (web3-guarded)", p, got)
		}
	}

	// Security: the whole NestJS SecurityController is class-level @Public +
	// @UseGuards(Web3AuthGuard), so ALL EIGHT FE-used routes 401 without a web3
	// token — the three data reads, the connected-sites mutations, the revoke
	// tx-builder, tx-review, AND its explanation route (folded into /security via
	// registrars when their deps are wired, as they are here). The live golden run
	// (golden-run-2026-06-08.md) caught Go serving the reads publicly (NestJS 401
	// vs Go 200), an over-exposure since closed; the reads are now also owner-pinned
	// (403 on a foreign ?address=, asserted below with a valid token).
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/security/approvals?address=0x0000000000000000000000000000000000000001&chainId=1"},
		{"GET", "/api/security/alerts?address=0x0000000000000000000000000000000000000001&chainId=1"},
		{"GET", "/api/security/connected-sites?address=0x0000000000000000000000000000000000000001"},
		{"POST", "/api/security/connected-sites"},
		{"DELETE", "/api/security/connected-sites/abc"},
		{"POST", "/api/security/revoke/build-tx"},
		{"POST", "/api/security/tx-review"},
		{"POST", "/api/security/tx-review/explain"},
	} {
		if got := code(tc.method, tc.path); got != http.StatusUnauthorized {
			t.Errorf("%s %s = %d, want 401 (security web3-guarded)", tc.method, tc.path, got)
		}
	}

	// Settings: the whole NestJS SettingsController is class-level @Public +
	// @UseGuards(Web3AuthGuard), so EVERY route (reads AND writes) 401s without a
	// web3 token. Go must match — the reads previously mounted public (an
	// over-exposure of owner-private settings, now closed via web3 guard +
	// owner-pin). The guard rejects before the handler, so no repo is needed here.
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/settings/preferences"},
		{"GET", "/api/settings/notifications"},
		{"GET", "/api/settings/hidden-assets?chainId=11155111"},
		{"PUT", "/api/settings/preferences"},
		{"PUT", "/api/settings/notifications"},
		{"POST", "/api/settings/hidden-assets"},
		{"DELETE", "/api/settings/hidden-assets/abc"},
	} {
		if got := code(tc.method, tc.path); got != http.StatusUnauthorized {
			t.Errorf("%s %s = %d, want 401 (settings web3-guarded)", tc.method, tc.path, got)
		}
	}

	// Wallets: like settings, the whole NestJS WalletsController is class-level
	// @Public + @UseGuards(Web3AuthGuard), so all five FE-used routes (reads AND
	// writes) 401 without a web3 token. Go must match — the two GET reads (/ and
	// /manager) previously mounted public (an over-exposure of owner-private
	// wallet data, now closed via web3 guard + owner-pin). The guard rejects
	// before the handler, so no repo is needed here.
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/wallets"},
		{"GET", "/api/wallets/manager"},
		{"POST", "/api/wallets/watch-only"},
		{"POST", "/api/wallets/groups"},
		{"PUT", "/api/wallets/abc/profile"},
	} {
		if got := code(tc.method, tc.path); got != http.StatusUnauthorized {
			t.Errorf("%s %s = %d, want 401 (wallets web3-guarded)", tc.method, tc.path, got)
		}
	}

	web3Token := signWeb3Token(t)
	userToken, _, err := userVerifier.IssueTokens("user-1", "alice", "alice@example.com", []string{"user"})
	if err != nil {
		t.Fatalf("issue user token: %v", err)
	}
	adminToken, _, err := userVerifier.IssueTokens("admin-1", "admin", "admin@example.com", []string{"admin"})
	if err != nil {
		t.Fatalf("issue admin token: %v", err)
	}
	if got := codeWithBearer("POST", "/api/staking/pools", userToken); got != http.StatusForbidden {
		t.Errorf("POST staking pool as ordinary user = %d, want 403", got)
	}
	if got := codeWithBearer("POST", "/api/staking/pools", adminToken); got != http.StatusBadRequest {
		t.Errorf("POST staking pool as admin = %d, want handler validation 400", got)
	}
	if got := codeWithBearer("GET", "/api/staking/user/0x000000000000000000000000000000000000dEaD", web3Token); got != http.StatusOK {
		t.Errorf("GET own staking state with SIWE = %d, want 200", got)
	}
	if got := codeWithBearer("GET", "/api/staking/user/0x000000000000000000000000000000000000bEEF", web3Token); got != http.StatusForbidden {
		t.Errorf("GET another wallet's staking state = %d, want 403", got)
	}
	for _, p := range []string{
		"/api/trading/positions/0x000000000000000000000000000000000000dEaD",
		"/api/trading/orders/0x000000000000000000000000000000000000dEaD?symbol=ETH-USD",
		"/api/trading/history/0x000000000000000000000000000000000000dEaD?symbol=ETH-USD",
	} {
		if got := codeWithBearer("GET", p, web3Token); got == http.StatusUnauthorized {
			t.Errorf("GET %s with web3 token = 401, want SIWE guard to pass", p)
		}
	}
	if got := codeWithBearer("GET", "/api/trading/orders/0x000000000000000000000000000000000000bEEF", web3Token); got != http.StatusForbidden {
		t.Errorf("GET trading orders for another wallet = %d, want 403 (owner mismatch)", got)
	}
	if got := codeWithBearer("GET", "/api/portfolio/summary", web3Token); got == http.StatusUnauthorized {
		t.Errorf("GET /api/portfolio/summary with web3 token = 401, want web3 guard to pass")
	}
	// IDOR guard (golden-run-2026-06-08): a web3-guarded portfolio read pins to
	// the token's wallet — a mismatched ?address= is 403, never someone else's
	// data. The token's sub is 0x…dEaD, so 0x…bEEF must be rejected.
	if got := codeWithBearer("GET", "/api/portfolio/summary?address=0x000000000000000000000000000000000000bEEF", web3Token); got != http.StatusForbidden {
		t.Errorf("GET /api/portfolio/summary?address=<other> = %d, want 403 (owner mismatch)", got)
	}
	if got := codeWithBearer("GET", "/api/portfolio/assets?address=0x000000000000000000000000000000000000bEEF", web3Token); got != http.StatusForbidden {
		t.Errorf("GET /api/portfolio/assets?address=<other> = %d, want 403 (owner mismatch)", got)
	}
	if got := codeWithBearer("GET", "/api/portfolio/assets/0x000000000000000000000000000000000000dead", web3Token); got != http.StatusUnauthorized {
		t.Errorf("GET /api/portfolio/assets/:address with web3 token = %d, want 401 (access-guarded)", got)
	}

	// Wallets reads owner-pin to the token wallet too: a valid web3 token passes
	// the guard, and a mismatched ?address=/?ownerAddress= is 403 (never another
	// wallet's data) — the IDOR guard this cut added to the formerly-public reads.
	if got := codeWithBearer("GET", "/api/wallets", web3Token); got == http.StatusUnauthorized {
		t.Errorf("GET /api/wallets with web3 token = 401, want web3 guard to pass")
	}
	if got := codeWithBearer("GET", "/api/wallets?address=0x000000000000000000000000000000000000bEEF", web3Token); got != http.StatusForbidden {
		t.Errorf("GET /api/wallets?address=<other> = %d, want 403 (owner mismatch)", got)
	}
	if got := codeWithBearer("GET", "/api/wallets/manager?ownerAddress=0x000000000000000000000000000000000000bEEF", web3Token); got != http.StatusForbidden {
		t.Errorf("GET /api/wallets/manager?ownerAddress=<other> = %d, want 403 (owner mismatch)", got)
	}

	// Security reads owner-pin to the token wallet too: a valid web3 token passes
	// the guard, and a mismatched ?address= (or ?ownerAddress= on the delete) is
	// 403 — the IDOR guard added to the formerly guarded-but-unpinned reads.
	if got := codeWithBearer("GET", "/api/security/approvals", web3Token); got == http.StatusUnauthorized {
		t.Errorf("GET /api/security/approvals with web3 token = 401, want web3 guard to pass")
	}
	for _, p := range []string{
		"/api/security/approvals?address=0x000000000000000000000000000000000000bEEF",
		"/api/security/alerts?address=0x000000000000000000000000000000000000bEEF",
		"/api/security/connected-sites?address=0x000000000000000000000000000000000000bEEF",
	} {
		if got := codeWithBearer("GET", p, web3Token); got != http.StatusForbidden {
			t.Errorf("GET %s = %d, want 403 (owner mismatch)", p, got)
		}
	}
	// DELETE connected-sites owner-pins via ?ownerAddress= (NestJS @Query).
	if got := codeWithBearer("DELETE", "/api/security/connected-sites/abc?ownerAddress=0x000000000000000000000000000000000000bEEF", web3Token); got != http.StatusForbidden {
		t.Errorf("DELETE /api/security/connected-sites/:id?ownerAddress=<other> = %d, want 403 (owner mismatch)", got)
	}
	// The explain route must pass the real SIWE middleware into its handler: a
	// valid token reaches JSON parsing (400 on an empty body), while a body naming
	// another wallet is owner-pinned to the token and rejected before any review
	// or paid provider call can run.
	if got := codeWithBearer("POST", "/api/security/tx-review/explain", web3Token); got != http.StatusBadRequest {
		t.Errorf("POST /api/security/tx-review/explain with web3 token = %d, want 400 from handler", got)
	}
	foreignOwner := `{"fromAddress":"0x000000000000000000000000000000000000bEEF"}`
	if got := codeWithBearerBody("POST", "/api/security/tx-review/explain", web3Token, foreignOwner); got != http.StatusForbidden {
		t.Errorf("POST /api/security/tx-review/explain for another wallet = %d, want 403", got)
	}

	// token create: a valid web3 token passes the guard (the handler then 400s on
	// the empty body — NOT 401), proving create is web3-guarded, not access-guarded.
	if got := codeWithBearer("POST", "/api/token", web3Token); got == http.StatusUnauthorized {
		t.Errorf("POST /api/token with web3 token = 401, want web3 guard to pass")
	}

	// Public routes must stay reachable without a token (never 401) — including
	// the market-wide trading reads (NestJS @Public), the Go-canonical public
	// earn product catalog (2026-06-15), the Go-canonical public token reads
	// (2026-06-16: token list/trending/by-address), and the Go-canonical public
	// bridge simulator (2026-06-17: GET /status — an intentional departure from
	// the NestJS global JwtAuthGuard that 401'd them; the FE loads them with plain
	// fetch and no auth). Degraded nil-repo → 200 (list/trending) or 404
	// (by-address) — never 401.
	for _, p := range []string{
		"/api/markets/symbols?chainId=1", "/healthz", "/api/tags",
		"/api/trading/markets?chainId=1", "/api/earn/products?chainId=1",
		"/api/token", "/api/token/trending", "/api/token/address/0xabc",
		"/api/bridge/status/probe",
		// staking pool catalog reads — Go-canonical PUBLIC (2026-06-17: the FE
		// advanced-earn page loads /pools + /pools/stats with plain fetch + no
		// auth). /pools/{id} is the same public catalog kind. Degraded nil-repo →
		// 200 (list/stats) or 404/503 (by-id) — never 401.
		"/api/staking/pools?chainId=1", "/api/staking/pools/stats",
	} {
		if got := code("GET", p); got == http.StatusUnauthorized {
			t.Errorf("GET %s = 401, want public (unguarded)", p)
		}
	}

	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/livekit/token"},
		{"GET", "/api/livekit/url"},
		{"GET", "/api/mobile/docs"},
		{"GET", "/api/mobile/v1/docs"},
		{"GET", "/api/web/v1/docs"},
		{"POST", "/api/indexer/start"},
		{"POST", "/api/indexer/stop"},
		{"POST", "/api/indexer/backfill"},
		{"POST", "/api/indexer/resync-tx"},
	} {
		if got := code(tc.method, tc.path); got != http.StatusNotFound {
			t.Errorf("%s %s = %d, want retired route 404", tc.method, tc.path, got)
		}
	}
	if got := code("GET", "/api/indexer/status"); got != http.StatusOK {
		t.Errorf("GET /api/indexer/status = %d, want read-only monitoring 200", got)
	}

	// Bridge POST /routes is public too (pure simulator, non-user-private): an
	// empty body reaches the handler and 400s on the JSON decode — crucially NOT
	// 401, proving the access guard was dropped (2026-06-17 Go-canonical cut).
	if got := code("POST", "/api/bridge/routes"); got == http.StatusUnauthorized {
		t.Errorf("POST /api/bridge/routes (no token) = 401, want public (unguarded)")
	}

	// Swap is Go-canonical PUBLIC too (stateless quote + ABI tx-builders, no DB,
	// no signing): all three POSTs reach the handler without a token. An empty
	// body 400s on the JSON decode — crucially NOT 401, proving the access guard
	// was dropped (2026-06-17 Go-canonical cut).
	for _, p := range []string{
		"/api/swap/quote", "/api/swap/build-approve", "/api/swap/build-swap",
	} {
		if got := code("POST", p); got == http.StatusUnauthorized {
			t.Errorf("POST %s (no token) = 401, want public (unguarded)", p)
		}
	}
}

func signWeb3Token(t *testing.T) string {
	t.Helper()
	claims := auth.Claims{
		Sub:     "0x000000000000000000000000000000000000dEaD",
		ChainID: 11155111,
		Type:    "web3",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(testWeb3Secret))
	if err != nil {
		t.Fatalf("sign web3 token: %v", err)
	}
	return signed
}
