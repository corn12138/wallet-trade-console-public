package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/adminclient"
)

func (a *app) login(args []string) int {
	if len(args) == 0 || (args[0] != "prepare" && args[0] != "complete") {
		fmt.Fprint(a.stderr, usage)
		return exitUsage
	}
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	address := fs.String("address", "", "administrator wallet")
	chainID := fs.Int("chain-id", 0, "chain ID accepted by the API")
	signatureFile := fs.String("signature-file", "", "file holding the 0x signature, or - for stdin")
	if extra, err := parse(fs, args[1:]); err != nil || len(extra) != 0 {
		return exitUsage
	}
	client, _, err := a.client(false)
	if err != nil {
		return a.fail(err)
	}
	ctx, cancel := a.context()
	defer cancel()
	if args[0] == "prepare" {
		pending, err := adminclient.PrepareLogin(ctx, a.home, client, *address, *chainID)
		if err != nil {
			return a.fail(err)
		}
		fmt.Fprintf(a.stderr, "sign this exact message with %s (personal_sign); challenge expires %s\n", pending.Address, pending.ExpiresAt)
		fmt.Fprint(a.stdout, pending.Message)
		return exitOK
	}
	var raw []byte
	switch *signatureFile {
	case "":
		return a.fail(errors.New("--signature-file is required (signatures are not accepted in argv)"))
	case "-":
		raw, err = io.ReadAll(io.LimitReader(a.stdin, 128*1024))
	default:
		raw, err = os.ReadFile(*signatureFile)
	}
	if err != nil {
		return a.fail(err)
	}
	credential, err := adminclient.CompleteLogin(ctx, a.home, client, string(raw))
	if err != nil {
		return a.fail(err)
	}
	fmt.Fprintf(a.stdout, "session stored for %s on chain %d until %s\n", credential.Address, credential.ChainID, credential.SessionExpiresAt)
	return exitOK
}

func (a *app) read(command string, args []string) int {
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	positional, err := parse(fs, args)
	if err != nil || (command == "state" && len(positional) != 1) || (command == "preview" && len(positional) != 2) {
		fmt.Fprint(a.stderr, usage)
		return exitUsage
	}
	if err = adminclient.ValidatePathIDs(positional...); err != nil {
		return a.fail(err)
	}
	client, _, err := a.client(true)
	if err != nil {
		return a.fail(err)
	}
	ctx, cancel := a.context()
	defer cancel()
	path, contract := "/v2/campaign-admin/campaigns/"+positional[0], "AdminCampaignResponse"
	if command == "preview" {
		path, contract = path+"/revisions/"+positional[1]+"/preview", "QueryPreviewResponse"
	}
	var value any
	if err = client.GetContract(ctx, path, contract, &value); err != nil {
		return a.fail(err)
	}
	a.printJSON(value)
	return exitOK
}

func (a *app) plan(args []string) int {
	fs := flag.NewFlagSet("plan", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	in := adminclient.PlanInput{}
	fs.StringVar(&in.CampaignID, "campaign", "", "campaign ID")
	fs.StringVar(&in.CandidateRevisionID, "candidate", "", "candidate revision ID")
	fs.StringVar(&in.Slug, "slug", "", "permanent slug (create only)")
	fs.StringVar(&in.Reason, "reason", "", "audited reason")
	fs.StringVar(&in.ExpectVersion, "expect-version", "", "required current publication version")
	revisionFile := fs.String("revision-file", "", "RevisionInput JSON (create/revision)")
	positional, err := parse(fs, args)
	if err != nil || len(positional) != 1 {
		fmt.Fprint(a.stderr, usage)
		return exitUsage
	}
	in.Operation = positional[0]
	if *revisionFile != "" {
		if in.RevisionJSON, err = os.ReadFile(*revisionFile); err != nil {
			return a.fail(err)
		}
	}
	client, credential, err := a.client(true)
	if err != nil {
		return a.fail(err)
	}
	ctx, cancel := a.context()
	defer cancel()
	intent, err := adminclient.Plan(ctx, a.home, client, credential, in, a.now())
	if err != nil {
		return a.fail(err)
	}
	fmt.Fprint(a.stdout, adminclient.RenderReview(intent, nil))
	fmt.Fprintf(a.stdout, "planned intent %s (not sent). Next: campaign-admin submit %s\n", intent.ID, intent.ID)
	return exitOK
}

func (a *app) submit(args []string) int {
	fs := flag.NewFlagSet("submit", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	confirmFlag := fs.String("confirm", "", "non-interactive confirmation: the campaign ID, or \"create\"")
	positional, err := parse(fs, args)
	if err != nil || len(positional) != 1 {
		fmt.Fprint(a.stderr, usage)
		return exitUsage
	}
	client, credential, err := a.client(true)
	if err != nil {
		return a.fail(err)
	}
	ctx, cancel := a.context()
	defer cancel()
	confirm := func(intent *adminclient.Intent, fresh *adminclient.CurrentState) error {
		fmt.Fprint(a.stdout, adminclient.RenderReview(intent, fresh))
		target := adminclient.ConfirmationTarget(intent)
		answer := *confirmFlag
		if answer == "" {
			if !a.tty {
				return errors.New("confirmation required: pass --confirm " + target + " or run interactively")
			}
			fmt.Fprintf(a.stdout, "type %q to send: ", target)
			line, _ := bufio.NewReader(a.stdin).ReadString('\n')
			answer = strings.TrimSpace(line)
		}
		if answer != target {
			return errors.New("confirmation did not match; nothing was sent")
		}
		return nil
	}
	intent, err := adminclient.Submit(ctx, a.home, client, credential, positional[0], a.now, confirm)
	if err != nil {
		return a.failIntent(intent, err)
	}
	return a.outcome(intent)
}

func (a *app) reconcile(args []string) int {
	fs := flag.NewFlagSet("reconcile", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	positional, err := parse(fs, args)
	if err != nil || len(positional) != 1 {
		fmt.Fprint(a.stderr, usage)
		return exitUsage
	}
	client, credential, err := a.client(true)
	if err != nil {
		return a.fail(err)
	}
	ctx, cancel := a.context()
	defer cancel()
	intent, err := adminclient.Reconcile(ctx, a.home, client, credential, positional[0], a.now)
	if err != nil {
		return a.failIntent(intent, err)
	}
	return a.outcome(intent)
}

func (a *app) outcome(intent *adminclient.Intent) int {
	fmt.Fprint(a.stdout, adminclient.RenderOutcome(intent))
	switch intent.Status {
	case adminclient.StatusCommitted:
		return exitOK
	case adminclient.StatusRejected:
		return exitRejected
	}
	return exitUnknown
}

func (a *app) local(command string, args []string) int {
	if len(args) != 1 {
		fmt.Fprint(a.stderr, usage)
		return exitUsage
	}
	if command == "abandon" {
		intent, err := adminclient.Abandon(a.home, args[0], a.now())
		if err != nil {
			return a.fail(err)
		}
		fmt.Fprintf(a.stdout, "intent %s abandoned; it was never sent\n", intent.ID)
		return exitOK
	}
	intent, err := a.home.LoadIntent(args[0])
	if err != nil {
		return a.fail(err)
	}
	a.printJSON(intent)
	return exitOK
}

func (a *app) list() int {
	intents, err := a.home.ListIntents()
	if err != nil {
		return a.fail(err)
	}
	for _, intent := range intents {
		target := intent.CampaignID
		if target == "" {
			target = "(new campaign)"
		}
		fmt.Fprintf(a.stdout, "%s  %-9s  %-8s  %s  attempts=%d\n", intent.ID, intent.Status, intent.Operation, target, len(intent.Attempts))
	}
	return exitOK
}

// failIntent reports an error together with what is known about the command.
// An intent that may already have committed exits with exitUnknown even when
// this invocation failed, so scripts never mistake it for a plain error.
func (a *app) failIntent(intent *adminclient.Intent, err error) int {
	fmt.Fprintln(a.stderr, "error:", err)
	if intent == nil || intent.Status != adminclient.StatusUnknown {
		return exitError
	}
	fmt.Fprint(a.stdout, adminclient.RenderOutcome(intent))
	return exitUnknown
}

// logout always removes the local session. It reports a server-side logout
// only when the server confirmed it; otherwise it exits with exitUnknown and
// says how long a copied token could still work.
func (a *app) logout() int {
	ctx, cancel := a.context()
	defer cancel()
	outcome, err := adminclient.Logout(ctx, a.home, a.timeout)
	if err != nil {
		return a.fail(err)
	}
	switch outcome.Result {
	case adminclient.LogoutNoSession:
		fmt.Fprintln(a.stdout, "no local session; nothing to log out")
	case adminclient.LogoutRevoked:
		fmt.Fprintf(a.stdout, "logged out %s: the server ended the session and the local copy was removed\n", outcome.Address)
	case adminclient.LogoutAlreadyRevoked:
		fmt.Fprintf(a.stdout, "logged out %s: the server session had already ended; the local copy was removed\n", outcome.Address)
	case adminclient.LogoutRejected:
		fmt.Fprintf(a.stdout, "logged out %s: the server no longer accepts this session; the local copy was removed\n", outcome.Address)
	default:
		expiry := outcome.SessionExpiresAt
		if expiry == "" {
			expiry = "its expiry"
		}
		fmt.Fprintf(a.stderr, "local session removed, but the server did not confirm the logout (%s): a copy of the token stays valid until %s unless an operator revokes the session\n", outcome.Detail, expiry)
		return exitUnknown
	}
	return exitOK
}
