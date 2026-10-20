package main

import (
	"bytes"
	"flag"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/adminclient"
)

func testApp(t *testing.T, stdin string, tty bool) (*app, *bytes.Buffer, *bytes.Buffer) {
	var out, errOut bytes.Buffer
	return &app{stdin: strings.NewReader(stdin), stdout: &out, stderr: &errOut, now: time.Now, tty: tty}, &out, &errOut
}

// Exit codes are part of the operator contract: scripts must be able to tell a
// usage mistake from a refusal and from an outcome that needs reconciliation.
func TestCommandLineContract(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	cases := []struct {
		args []string
		exit int
		text string
	}{
		{[]string{}, exitUsage, "usage"},
		{[]string{"--home", home, "unknown"}, exitUsage, "usage"},
		{[]string{"--home", home, "login"}, exitUsage, "usage"},
		{[]string{"--home", home, "plan"}, exitUsage, "usage"},
		{[]string{"--home", home, "--api", "http://127.0.0.1:1/api", "state", "campaign-1"}, exitError, "no administrator session"},
		{[]string{"--home", home, "--api", "http://api.example.com/api", "login", "prepare", "--address", "0x1111111111111111111111111111111111111111", "--chain-id", "1"}, exitError, "loopback"},
		{[]string{"--home", home, "--api", "http://127.0.0.1:1/api", "state", "../escape"}, exitError, "malformed campaign ID"},
		{[]string{"--home", home, "--api", "http://127.0.0.1:1/api", "login", "complete"}, exitError, "not accepted in argv"},
		{[]string{"--home", home, "show", "not-an-id"}, exitError, "UUIDv7"},
		{[]string{"--home", home, "list"}, exitOK, ""},
		{[]string{"--home", home, "legacy"}, exitUsage, "campaign-admin legacy"},
		{[]string{"--home", home, "legacy", "activate", "019aa123-4567-7000-8000-0000000000aa", "campaign-1", "--decision", "exclude", "--reason", "r"}, exitUsage, "usage"},
		{[]string{"--home", home, "--api", "http://127.0.0.1:1/api", "legacy", "plan", "019aa123-4567-7000-8000-0000000000aa"}, exitError, "no administrator session"},
		{[]string{"--home", home, "legacy", "show", "not-an-id"}, exitError, "UUIDv7"},
		{[]string{"--home", home, "legacy", "list"}, exitOK, ""},
	}
	for _, c := range cases {
		a, out, errOut := testApp(t, "", false)
		if got := a.run(c.args); got != c.exit || !strings.Contains(out.String()+errOut.String(), c.text) {
			t.Fatalf("%v: exit %d (want %d) output %q", c.args, got, c.exit, out.String()+errOut.String())
		}
	}
}

func TestParseAcceptsFlagsAroundPositionals(t *testing.T) {
	fs := newTestFlagSet()
	reason := fs.String("reason", "", "")
	positional, err := parse(fs, []string{"publish", "--reason", "why", "extra"})
	if err != nil || *reason != "why" || len(positional) != 2 || positional[0] != "publish" || positional[1] != "extra" {
		t.Fatalf("parse: %v %v %q", err, positional, *reason)
	}
}

func newTestFlagSet() *flag.FlagSet { return flag.NewFlagSet("test", flag.ContinueOnError) }

// Logout always removes the local session, and claims a server-side logout
// only when the server confirmed it: an unreachable server exits with
// exitUnknown and never prints the token.
func TestLogoutContract(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "home")
	a, out, errOut := testApp(t, "", false)
	if got := a.run([]string{"--home", dir, "logout"}); got != exitOK || !strings.Contains(out.String(), "no local session") {
		t.Fatalf("no session: exit %d output %q", got, out.String()+errOut.String())
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	api := "http://" + listener.Addr().String() + "/api"
	listener.Close()
	home, err := adminclient.OpenHome(dir)
	if err != nil {
		t.Fatal(err)
	}
	const token = "cli-logout-test-token"
	expires := time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	if err := home.SaveCredential(adminclient.Credential{API: api, Address: "0x1111111111111111111111111111111111111111", ChainID: 1, Token: token, SessionExpiresAt: expires}); err != nil {
		t.Fatal(err)
	}
	a, out, errOut = testApp(t, "", false)
	got := a.run([]string{"--home", dir, "--timeout", "500ms", "logout"})
	text := out.String() + errOut.String()
	if got != exitUnknown || !strings.Contains(text, "did not confirm") || !strings.Contains(text, expires) || strings.Contains(text, token) {
		t.Fatalf("unconfirmed: exit %d output %q", got, text)
	}
	if _, err := home.LoadCredential(time.Now()); err != adminclient.ErrCredentialMissing {
		t.Fatalf("the local session must be removed: %v", err)
	}
}
