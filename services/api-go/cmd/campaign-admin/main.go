// Command campaign-admin is the controlled operator tool for versioned campaign
// administration (create, revision, preview, publish, pause, resume, archive,
// rollback and command reconciliation) and for the controlled legacy import
// (plan, review, activation). Every write is planned into a private durable
// intent first, shown for typed confirmation, and sent with a fixed UUIDv7 key
// so an uncertain outcome is retried or reconciled, never re-keyed.
//
// The tool signs nothing: login prints a SIWE message for the operator's own
// wallet or signer, and the resulting session token stays in the private home.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/adminclient"
)

// Exit codes let scripts distinguish a definite refusal from an outcome that
// must be reconciled before anyone acts on the campaign again.
const (
	exitOK       = 0
	exitError    = 1
	exitUsage    = 2
	exitRejected = 3
	exitUnknown  = 4
)

type app struct {
	home    adminclient.Home
	api     string
	timeout time.Duration
	stdin   io.Reader
	stdout  io.Writer
	stderr  io.Writer
	now     func() time.Time
	tty     bool
}

func main() {
	stat, _ := os.Stdin.Stat()
	a := &app{stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr, now: time.Now, tty: stat != nil && stat.Mode()&os.ModeCharDevice != 0}
	os.Exit(a.run(os.Args[1:]))
}

func defaultHome() string {
	if value := os.Getenv("CAMPAIGN_ADMIN_HOME"); value != "" {
		return value
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "wallet-trade", "campaign-admin")
}

const usage = `usage: campaign-admin [--home DIR] [--api URL] [--timeout D] <command>

  login prepare --address 0x.. --chain-id N      print a SIWE message to sign externally
  login complete --signature-file PATH|-         exchange the signature for a private session
  logout                                         end the session on the server, then delete the local copy
  session                                        show the local session identity and expiry
  state <campaignId>                             administrator view (versions, lock, revisions)
  preview <campaignId> <revisionId>              private candidate preview
  plan <create|revision|publish|rollback|pause|resume|archive> --reason R
       [--campaign ID] [--revision-file F] [--candidate REVISION] [--slug S] [--expect-version N]
  submit <intentId> [--confirm TARGET]           confirm and send (or resend the same key)
  reconcile <intentId>                           read the authoritative command ledger
  abandon <intentId>                             retire a never-sent intent
  list | show <intentId>                         inspect local intents (no credentials)
  legacy plan|review|activate|submit|status|...  controlled legacy import (campaign-admin legacy for help)

--api defaults to $CAMPAIGN_ADMIN_API and includes the /api prefix.
`

func (a *app) run(args []string) int {
	global := flag.NewFlagSet("campaign-admin", flag.ContinueOnError)
	global.SetOutput(a.stderr)
	home := global.String("home", defaultHome(), "private state directory")
	global.StringVar(&a.api, "api", os.Getenv("CAMPAIGN_ADMIN_API"), "API base including /api")
	global.DurationVar(&a.timeout, "timeout", 15*time.Second, "per-request timeout")
	global.Usage = func() { fmt.Fprint(a.stderr, usage) }
	if err := global.Parse(args); err != nil || global.NArg() == 0 {
		fmt.Fprint(a.stderr, usage)
		return exitUsage
	}
	var err error
	if a.home, err = adminclient.OpenHome(*home); err != nil {
		return a.fail(err)
	}
	command, rest := global.Arg(0), global.Args()[1:]
	switch command {
	case "login":
		return a.login(rest)
	case "logout":
		return a.logout()
	case "session":
		credential, err := a.home.LoadCredential(a.now())
		if err != nil {
			return a.fail(err)
		}
		fmt.Fprintf(a.stdout, "administrator %s chain %d at %s, session expires %s\n", credential.Address, credential.ChainID, credential.API, credential.SessionExpiresAt)
		return exitOK
	case "state", "preview":
		return a.read(command, rest)
	case "plan":
		return a.plan(rest)
	case "submit":
		return a.submit(rest)
	case "reconcile":
		return a.reconcile(rest)
	case "abandon", "show":
		return a.local(command, rest)
	case "list":
		return a.list()
	case "legacy":
		return a.legacy(rest)
	}
	fmt.Fprint(a.stderr, usage)
	return exitUsage
}

func (a *app) fail(err error) int {
	fmt.Fprintln(a.stderr, "error:", err)
	return exitError
}

// parse accepts flags before or after positional arguments, which the standard
// flag package alone does not.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return positional, nil
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func (a *app) client(authenticated bool) (*adminclient.Client, adminclient.Credential, error) {
	var credential adminclient.Credential
	if a.api == "" {
		return nil, credential, errors.New("--api or CAMPAIGN_ADMIN_API is required")
	}
	client, err := adminclient.NewClient(a.api, a.timeout)
	if err != nil || !authenticated {
		return client, credential, err
	}
	if credential, err = a.home.LoadCredential(a.now()); err != nil {
		return nil, credential, err
	}
	client, err = client.WithCredential(credential)
	return client, credential, err
}

func (a *app) context() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 4*a.timeout)
}

func (a *app) printJSON(value any) {
	raw, _ := json.MarshalIndent(value, "", "  ")
	fmt.Fprintln(a.stdout, string(raw))
}
