// Command routes prints the Go API's mounted route table, one
// "METHOD /path" per line, sorted. It is the source of truth for the
// committed route contract snapshot testdata/parity/go-routes.txt, which
// scripts/check-go-schemas.sh diffs on every CI run (the "Go API contract"
// gate) so an accidental route add/remove/rename fails before it ships.
//
// It builds the real chi router from internal/httpx with stub
// dependencies chosen so every module mounts — including the ones gated
// on a non-nil verifier/service (auth, web3-auth, contracts, tx-review, AI
// explanation).
// The stubs are never invoked: chi.Walk only inspects the route tree, it
// does not call handlers, so zero-value pointers are safe here.
//
// Usage:
//
//	go run ./cmd/routes            # print the table
//	go run ./cmd/routes | wc -l    # count routes
package main

import (
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/ai"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/article"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/contractconfig"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/deployments"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/httpx"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/txreview"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/web3auth"
	"github.com/go-chi/chi/v5"
)

// emptyLoader satisfies markets.DeploymentsLoader so the markets service
// (and the discover service that depends on it) mount.
type emptyLoader struct{}

func (emptyLoader) Load() (map[int]deployments.ChainConfig, error) {
	return map[int]deployments.ChainConfig{}, nil
}

func main() {
	// Non-nil stubs for every dep that GATES a mount. Most concrete repos can
	// be left nil — their module mounts unconditionally and the routes still
	// register (handlers just degrade at request time). The exceptions are
	// deps that a Router consults to DECIDE whether to register a route:
	//   - the verifier/service gates in server.go (markets, auth, contracts, …)
	//   - ArticleRepo: article.Router only registers its 5 write mutations when
	//     the Writer (ArticleRepo) is non-nil, so a nil repo would hide real,
	//     implemented routes from the inventory.
	txReviewSvc := &txreview.Service{}
	deps := httpx.Deps{
		MarketsLoader:  emptyLoader{},
		AuthVerifier:   &auth.Verifier{},
		UserVerifier:   &auth.UserVerifier{},
		CsrfSigner:     &auth.CsrfSigner{},
		ContractConfig: &contractconfig.Service{},
		TxReview:       txReviewSvc,
		AIExplain:      ai.NewService(ai.Config{}, nil, txReviewSvc),
		Web3Auth:       &web3auth.Service{},
		ArticleRepo:    &article.Repository{},
	}

	r := httpx.NewRouter(deps)

	var lines []string
	walk := func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		// Business routes are GET/POST/PUT/PATCH/DELETE. chi's Handle()
		// (e.g. the /metrics promhttp handler) registers every method;
		// CONNECT/HEAD/OPTIONS/TRACE are transport noise, not parity surface.
		if !businessMethods[method] {
			return nil
		}
		n := normalize(route)
		// The Prometheus scrape endpoints are registered with chi.Handle
		// (every method) but are GET in practice — collapse the all-method
		// fan-out so the artifact isn't littered with DELETE/PATCH /metrics.
		if (n == "/metrics" || n == "/api/metrics") && method != http.MethodGet {
			return nil
		}
		lines = append(lines, method+" "+n)
		return nil
	}
	if err := chi.Walk(r, walk); err != nil {
		fmt.Fprintln(os.Stderr, "route walk failed:", err)
		os.Exit(1)
	}

	sort.Strings(lines)
	for _, l := range dedupe(lines) {
		fmt.Println(l)
	}
}

var businessMethods = map[string]bool{
	http.MethodGet:    true,
	http.MethodPost:   true,
	http.MethodPut:    true,
	http.MethodPatch:  true,
	http.MethodDelete: true,
}

// normalize makes a chi route comparable to a NestJS route:
//   - {param}        → :param          (chi wildcard → Nest param syntax)
//   - /api/*/health  → /api/health     (drop the "*" mount-point segment that
//     chi inserts when a sub-router is mounted at "/")
//   - trailing /*    → ""              (bare catch-all of a mounted sub-router)
//   - trailing /     → ""              (so "/api/activity/" == "/api/activity")
func normalize(route string) string {
	out := make([]byte, 0, len(route))
	for i := 0; i < len(route); i++ {
		switch route[i] {
		case '{':
			out = append(out, ':')
		case '}':
			// drop
		default:
			out = append(out, route[i])
		}
	}
	s := string(out)
	s = strings.ReplaceAll(s, "/*/", "/") // mount segment in the middle
	s = strings.TrimSuffix(s, "/*")       // mount catch-all at the end
	if s != "/" {
		s = strings.TrimSuffix(s, "/") // normalize trailing slash
	}
	if s == "" {
		s = "/"
	}
	return s
}

func dedupe(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := in[:0]
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
