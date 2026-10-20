package adminclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/web3auth"
)

func testHome(t *testing.T) Home {
	t.Helper()
	home, err := OpenHome(filepath.Join(t.TempDir(), "home"))
	if err != nil {
		t.Fatal(err)
	}
	return home
}

func TestHomeRefusesWidenedOrLinkedCredentials(t *testing.T) {
	home := testHome(t)
	credential := Credential{API: "https://api.test/api", Address: "0x1111111111111111111111111111111111111111", ChainID: 1, Token: "secret-token", SessionExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)}
	if err := home.SaveCredential(credential); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(home.credentialPath())
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("credential permissions %v", info.Mode().Perm())
	}
	if _, err := home.LoadCredential(time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := home.LoadCredential(time.Now().Add(2 * time.Hour)); err != ErrCredentialExpired {
		t.Fatal("expired session must not load")
	}
	os.Chmod(home.credentialPath(), 0o644)
	if _, err := home.LoadCredential(time.Now()); err == nil || !strings.Contains(err.Error(), "owner-only") {
		t.Fatal("group/other-readable credential must be refused")
	}
	os.Chmod(home.credentialPath(), 0o600)
	link := filepath.Join(t.TempDir(), "elsewhere.json")
	os.Rename(home.credentialPath(), link)
	os.Symlink(link, home.credentialPath())
	if _, err := home.LoadCredential(time.Now()); err == nil {
		t.Fatal("symlinked credential must be refused")
	}
	os.Chmod(home.Dir, 0o755)
	if _, err := OpenHome(home.Dir); err == nil {
		t.Fatal("widened home directory must be refused")
	}
}

func TestIntentIntegrityLockAndAbandon(t *testing.T) {
	home := testHome(t)
	id, _ := NewCommandID(time.Now())
	body := `{"clientCommandId":"` + id + `"}`
	intent := &Intent{SchemaVersion: 1, ID: id, Status: StatusPlanned, Body: body, BodySHA256: digest(body), Attempts: []Attempt{}}
	if err := home.SaveIntent(intent); err != nil {
		t.Fatal(err)
	}
	unlock, err := home.LockIntent(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = home.LockIntent(id); err == nil {
		t.Fatal("a second process must not hold the same intent")
	}
	unlock()
	raw, _ := os.ReadFile(home.intentPath(id))
	os.WriteFile(home.intentPath(id), []byte(strings.Replace(string(raw), `\"clientCommandId\"`, `\"clientCommandID\"`, 1)), 0o600)
	if _, err = home.LoadIntent(id); err == nil {
		t.Fatal("an edited payload must fail its digest")
	}
	os.WriteFile(home.intentPath(id), raw, 0o600)
	if _, err = Abandon(home, id, time.Now()); err != nil {
		t.Fatal(err)
	}
	intent.ID, intent.Status = mustID(t), StatusUnknown
	home.SaveIntent(intent)
	if _, err = Abandon(home, intent.ID, time.Now()); err == nil {
		t.Fatal("an intent that may have reached the server cannot be abandoned")
	}
}

func mustID(t *testing.T) string {
	id, err := NewCommandID(time.Now())
	if err != nil || !intentIDPattern.MatchString(id) {
		t.Fatal("UUIDv7 generation failed")
	}
	return id
}

func TestClientRefusesCleartextRemoteAndRedirects(t *testing.T) {
	for _, base := range []string{"http://api.example.com/api", "ftp://127.0.0.1/api", "https://user:pw@api.example.com/api", "https://api.example.com/api?x=1"} {
		if _, err := NewClient(base, time.Second); err == nil {
			t.Fatalf("%s must be refused", base)
		}
	}
	leaked := false
	other := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { leaked = r.Header.Get("Authorization") != "" }))
	defer other.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/steal", http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	client, err := NewClient(origin.URL+"/api", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	client, _ = client.WithCredential(Credential{API: client.Base(), Token: "secret-token"})
	reply := client.Do(context.Background(), http.MethodPost, "/v2/campaign-admin/campaigns", []byte(`{}`), "")
	if reply.Status != http.StatusTemporaryRedirect || leaked {
		t.Fatal("redirects must not be followed with the bearer token")
	}
	if _, _, retryable, _ := classifySubmit(testIntent(t, "publish"), reply); retryable {
		t.Fatal("redirect outcome must not be retryable-rejected")
	}
}

// The rendered message must be accepted by the server's own EIP-4361 parser, and
// a hostile challenge cannot inject extra signed lines.
func TestPrepareLoginRendersServerParsableMessage(t *testing.T) {
	statement := "Sign in"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"nonce":"abc123XYZ","domain":"localhost:3002","uri":"http://localhost:3002","statement":"` + statement + `","issuedAt":"2026-10-09T00:00:00Z","expirationTime":"2026-10-09T00:05:00Z","allowedChainIds":[11155111]}`))
	}))
	defer server.Close()
	client, _ := NewClient(server.URL+"/api", time.Second)
	home := testHome(t)
	pending, err := PrepareLogin(context.Background(), home, client, "0x52908400098527886E0F7030069857D2E4169EE7", 11155111)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := web3auth.ParseSiweMessage(pending.Message)
	if err != nil || parsed.Address != "0x52908400098527886E0F7030069857D2E4169EE7" || parsed.ChainID != 11155111 || parsed.Nonce != "abc123XYZ" || parsed.Statement != statement {
		t.Fatalf("server parser rejected or misread the message: %v %+v", err, parsed)
	}
	if _, err = PrepareLogin(context.Background(), home, client, pending.Address, 1); err == nil {
		t.Fatal("a chain the API does not accept must be refused before signing")
	}
	statement = `Sign in\nNonce: injected`
	if _, err = PrepareLogin(context.Background(), home, client, pending.Address, 11155111); err == nil {
		t.Fatal("multi-line challenge field must be refused")
	}
}
