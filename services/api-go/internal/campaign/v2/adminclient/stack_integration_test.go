//go:build integration

package adminclient_test

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth/authtest"
	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/adminclient"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/application"
	campaignhttp "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/http"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/postgres"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/web3auth"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/web3session"
	secp256k1 "github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/sha3"
)

// fault decides how the proxy in front of the real API mangles one request.
// It models the network, not the server: the API and database are untouched.
type fault int

const (
	faultNone         fault = iota
	faultDropRequest        // the request never reaches the API
	faultDropResponse       // the API commits; the client sees a gateway page
	faultResetAfter         // the API commits; the connection is cut mid-response
)

type stack struct {
	t        *testing.T
	pool     *pgxpool.Pool
	sessions *web3session.Store
	server   *httptest.Server
	mu       sync.Mutex
	admins   []string
	faults   map[string]fault // "METHOD path-prefix" -> next fault (consumed once)
	// requests records every proxied "METHOD path" so tests can assert what the
	// client read and sent, and in which order.
	requests []string
}

func keccak(data []byte) []byte {
	h := sha3.NewLegacyKeccak256()
	h.Write(data)
	return h.Sum(nil)
}

// signer stands in for the operator's own wallet. The CLI only ever sees the
// signature, never this key.
type signer struct {
	key     *secp256k1.PrivateKey
	address string
}

func newSigner(t *testing.T) signer {
	key, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	public := key.PubKey().SerializeUncompressed()
	return signer{key: key, address: "0x" + hex.EncodeToString(keccak(public[1:])[12:])}
}

func (s signer) sign(message string) string {
	digest := keccak([]byte("\x19Ethereum Signed Message:\n" + strconv.Itoa(len(message)) + message))
	compact := ecdsa.SignCompact(s.key, digest, false)
	return "0x" + hex.EncodeToString(append(append(append([]byte{}, compact[1:33]...), compact[33:65]...), compact[0]))
}

func newStack(t *testing.T) *stack {
	t.Helper()
	url := os.Getenv("CAMPAIGN_TEST_DATABASE_URL")
	if url == "" {
		t.Fatal("explicit disposable campaign database required")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var name string
	if err = pool.QueryRow(context.Background(), `SELECT current_database()`).Scan(&name); err != nil || (!strings.HasPrefix(name, "campaign_test") && !strings.HasPrefix(name, "migration_probe")) {
		t.Fatal("disposable campaign database required")
	}
	s := &stack{t: t, pool: pool, faults: map[string]fault{}, sessions: web3session.New(pool)}
	// One credential policy for issuing (SIWE) and every guard, with sessions
	// persisted in the disposable database exactly as cmd/api does.
	verifier := authtest.New(t, func(p *auth.Policy) { p.Sessions = s.sessions }).Verifier
	authorizer := campaignhttp.Authorizer{Verifier: verifier, Admins: func() []string {
		s.mu.Lock()
		defer s.mu.Unlock()
		return append([]string(nil), s.admins...)
	}}
	capabilities := func() v2.Capabilities { return v2.Capabilities{Templates: map[string]bool{"campaignCardV1": true}} }
	store := postgres.New(pool)
	siwe := web3auth.NewServiceWithConfig(web3auth.Config{SessionTTL: 15 * time.Minute, NonceTTL: 5 * time.Minute, AllowedChainIDs: []int{11155111},
		Credentials: verifier, Sessions: s.sessions})
	authRoutes := chi.NewRouter()
	web3auth.RegisterAuthAliases(authRoutes, siwe)
	router := chi.NewRouter()
	router.Use(middleware.RequestID)
	router.Mount("/api/auth", authRoutes)
	router.Mount("/api/web3-auth", web3auth.Router(siwe))
	router.Mount("/api/v2", campaignhttp.Router(campaignhttp.Config{Enabled: true, Verifier: verifier, Commands: application.NewService(store, authorizer, capabilities), Queries: application.NewQueryService(store, authorizer, capabilities)}))
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.proxy(w, r, router) }))
	t.Cleanup(s.server.Close)
	return s
}

func (s *stack) proxy(w http.ResponseWriter, r *http.Request, next http.Handler) {
	s.mu.Lock()
	mode := faultNone
	for key, value := range s.faults {
		method, prefix, _ := strings.Cut(key, " ")
		if r.Method == method && strings.HasPrefix(r.URL.Path, prefix) {
			mode = value
			delete(s.faults, key)
		}
	}
	s.requests = append(s.requests, r.Method+" "+r.URL.Path)
	s.mu.Unlock()
	switch mode {
	case faultDropRequest:
		http.Error(w, "<html>bad gateway</html>", http.StatusBadGateway)
	case faultDropResponse:
		next.ServeHTTP(httptest.NewRecorder(), r)
		http.Error(w, "<html>bad gateway</html>", http.StatusBadGateway)
	case faultResetAfter:
		next.ServeHTTP(httptest.NewRecorder(), r)
		conn, buffered, err := w.(http.Hijacker).Hijack()
		if err == nil {
			buffered.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 500\r\n\r\n{\"schemaVersion\"")
			buffered.Flush()
			conn.Close()
		}
	default:
		next.ServeHTTP(w, r)
	}
}

// inject arms a one-shot fault for the next POST under prefix; injectMethod
// does the same for another method (for example a failing ledger GET).
func (s *stack) inject(prefix string, mode fault) { s.injectMethod(http.MethodPost, prefix, mode) }

func (s *stack) injectMethod(method, prefix string, mode fault) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.faults[method+" "+prefix] = mode
}

// requestsSince returns the proxied requests after mark whose path contains part.
func (s *stack) requestsSince(mark int, part string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, request := range s.requests[mark:] {
		if strings.Contains(request, part) {
			out = append(out, request)
		}
	}
	return out
}

func (s *stack) mark() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

// committedFacts counts the ledger rows and audit rows for one command key, so
// recovery tests prove that settling never executed a command twice.
func (s *stack) committedFacts(t *testing.T, key string) (commands, audits int) {
	t.Helper()
	commands = s.count(t, `SELECT count(*) FROM campaign_commands WHERE client_command_id=$1`, key)
	audits = s.count(t, `SELECT count(*) FROM campaign_audit_events a JOIN campaign_commands c ON c.id=a.command_id WHERE c.client_command_id=$1`, key)
	return commands, audits
}

// revokeSessions is operator revocation: every live session of wallet ends.
func (s *stack) revokeSessions(t *testing.T, wallet string) {
	t.Helper()
	if _, err := s.sessions.RevokeWallet(context.Background(), strings.ToLower(wallet), "integration-operator"); err != nil {
		t.Fatal(err)
	}
}

func (s *stack) setAdmins(addresses ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.admins = addresses
}

// operator is one administrator workstation: its own private home and session.
type operator struct {
	s      *stack
	signer signer
	home   adminclient.Home
	client *adminclient.Client
	cred   adminclient.Credential
}

func (s *stack) operator(t *testing.T) *operator {
	home, err := adminclient.OpenHome(filepath.Join(t.TempDir(), "admin"))
	if err != nil {
		t.Fatal(err)
	}
	client, err := adminclient.NewClient(s.server.URL+"/api", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	o := &operator{s: s, signer: newSigner(t), home: home, client: client}
	o.login(t)
	return o
}

// login runs the real SIWE path: server nonce, external personal_sign, verify.
func (o *operator) login(t *testing.T) {
	t.Helper()
	pending, err := adminclient.PrepareLogin(context.Background(), o.home, o.client, o.signer.address, 11155111)
	if err != nil {
		t.Fatal(err)
	}
	if o.cred, err = adminclient.CompleteLogin(context.Background(), o.home, o.client, o.signer.sign(pending.Message)); err != nil {
		t.Fatal(err)
	}
	if o.client, err = adminclient.NewClient(o.s.server.URL+"/api", 5*time.Second); err == nil {
		o.client, err = o.client.WithCredential(o.cred)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func revisionJSON(t *testing.T, start, end time.Time, rules string) []byte {
	raw, err := os.ReadFile("../../../../../../packages/shared/contracts/fixtures/campaign-v2-revision.json")
	if err != nil {
		t.Fatal(err)
	}
	var value v2.RevisionInput
	if err = json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	value.MaterialTerms.StartAt, value.MaterialTerms.EndAt = start.UTC().Format("2006-01-02T15:04:05Z"), end.UTC().Format("2006-01-02T15:04:05Z")
	if rules != "" {
		value.MaterialTerms.Copy.En.Rules = rules
	}
	out, _ := json.Marshal(value)
	return out
}

func (o *operator) plan(t *testing.T, in adminclient.PlanInput) *adminclient.Intent {
	t.Helper()
	if in.Reason == "" {
		in.Reason = "integration " + in.Operation
	}
	intent, err := adminclient.Plan(context.Background(), o.home, o.client, o.cred, in, time.Now())
	if err != nil {
		t.Fatalf("plan %s: %v", in.Operation, err)
	}
	return intent
}

func accept(*adminclient.Intent, *adminclient.CurrentState) error { return nil }

func (o *operator) submit(t *testing.T, id string, confirm adminclient.Confirm) *adminclient.Intent {
	t.Helper()
	intent, err := adminclient.Submit(context.Background(), o.home, o.client, o.cred, id, time.Now, confirm)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	return intent
}

func (o *operator) commit(t *testing.T, in adminclient.PlanInput) *adminclient.Intent {
	t.Helper()
	intent := o.submit(t, o.plan(t, in).ID, accept)
	if intent.Status != adminclient.StatusCommitted {
		t.Fatalf("%s not committed: %s", in.Operation, adminclient.RenderOutcome(intent))
	}
	return intent
}

func (s *stack) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := s.pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// interact sends a participant command with an ordinary (non-admin) SIWE
// session, so lock and serialization tests use real accepted history.
func (o *operator) interact(t *testing.T, method, path string, body map[string]string) adminclient.Reply {
	t.Helper()
	id, err := adminclient.NewCommandID(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	body["clientCommandId"] = id
	raw, _ := json.Marshal(body)
	return o.client.Do(context.Background(), method, "/v2/campaigns/"+path, raw, "")
}

// reminder records an accepted reminder and then removes it: the acceptance
// must keep binding the terms after cancellation.
func (s *stack) reminder(t *testing.T, campaignID string) bool {
	participant := s.operator(t)
	set := participant.interact(t, http.MethodPost, campaignID+"/reminder", map[string]string{"expectedStateVersion": "0", "expectedPublicationVersion": "1"})
	removed := participant.interact(t, http.MethodDelete, campaignID+"/reminder", map[string]string{"expectedStateVersion": "1"})
	return set.Status == http.StatusOK && removed.Status == http.StatusOK
}
