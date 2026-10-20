package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"strings"

	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/adminclient"
)

const legacyUsage = `usage: campaign-admin legacy <command>   (controlled legacy import, protocol §5)

  plan <runId> [--cursor C] [--json]              one page of the run's plan (50 items)
  review <runId> <campaignId> --decision import-public|import-unpublished|exclude
         [--accept visibility,participantCount,walletState] [--acknowledge KIND,...] --reason R
                                                 freeze a review (not sent)
  activate <runId> <campaignId> --reason R        freeze an activation with the item's digests (not sent)
  submit <intentId> [--confirm CAMPAIGN_ID]        confirm and send (or settle and resend the same key)
  status <intentId>                               read the server's record for an intent
  abandon <intentId>                              retire a never-sent intent
  list | show <intentId>                          inspect local legacy intents (no credentials)
`

func (a *app) legacy(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(a.stderr, legacyUsage)
		return exitUsage
	}
	switch args[0] {
	case "plan":
		return a.legacyPlan(args[1:])
	case "review", "activate":
		return a.legacyFreeze(args[0], args[1:])
	case "submit":
		return a.legacySubmit(args[1:])
	case "status":
		return a.legacyStatus(args[1:])
	case "abandon", "show":
		return a.legacyLocal(args[0], args[1:])
	case "list":
		return a.legacyList()
	}
	fmt.Fprint(a.stderr, legacyUsage)
	return exitUsage
}

func (a *app) legacyPlan(args []string) int {
	fs := flag.NewFlagSet("legacy plan", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	cursor := fs.String("cursor", "", "the previous page's next cursor")
	asJSON := fs.Bool("json", false, "print the contract response")
	positional, err := parse(fs, args)
	if err != nil || len(positional) != 1 {
		fmt.Fprint(a.stderr, legacyUsage)
		return exitUsage
	}
	client, _, err := a.client(true)
	if err != nil {
		return a.fail(err)
	}
	ctx, cancel := a.context()
	defer cancel()
	page, err := adminclient.LegacyPlanPage(ctx, client, positional[0], *cursor)
	if err != nil {
		return a.fail(err)
	}
	if *asJSON {
		a.printJSON(page)
	} else {
		fmt.Fprint(a.stdout, adminclient.RenderLegacyPlan(page))
	}
	return exitOK
}

func splitList(value string) []string {
	if value == "" {
		return nil
	}
	return strings.Split(value, ",")
}

func (a *app) legacyFreeze(command string, args []string) int {
	fs := flag.NewFlagSet("legacy "+command, flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	reason := fs.String("reason", "", "audited reason")
	decision := fs.String("decision", "", "review decision")
	accept := fs.String("accept", "", "accepted difference classes, comma-separated")
	acknowledge := fs.String("acknowledge", "", "acknowledged review finding kinds, comma-separated")
	positional, err := parse(fs, args)
	if err != nil || len(positional) != 2 || (command == "activate" && (*decision != "" || *accept != "" || *acknowledge != "")) {
		fmt.Fprint(a.stderr, legacyUsage)
		return exitUsage
	}
	client, credential, err := a.client(true)
	if err != nil {
		return a.fail(err)
	}
	ctx, cancel := a.context()
	defer cancel()
	var intent *adminclient.LegacyIntent
	if command == "review" {
		intent, err = adminclient.PlanLegacyReview(ctx, a.home, client, credential, adminclient.LegacyReviewInput{RunID: positional[0], CampaignID: positional[1],
			Decision: *decision, Reason: *reason, Accept: splitList(*accept), Acknowledge: splitList(*acknowledge)}, a.now())
	} else {
		intent, err = adminclient.PlanLegacyActivation(ctx, a.home, client, credential, positional[0], positional[1], *reason, a.now())
	}
	if err != nil {
		return a.fail(err)
	}
	fmt.Fprint(a.stdout, adminclient.RenderLegacyIntent(intent, nil))
	fmt.Fprintf(a.stdout, "frozen legacy intent %s (not sent). Next: campaign-admin legacy submit %s\n", intent.ID, intent.ID)
	return exitOK
}

func (a *app) legacySubmit(args []string) int {
	fs := flag.NewFlagSet("legacy submit", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	confirmFlag := fs.String("confirm", "", "non-interactive confirmation: the campaign ID")
	positional, err := parse(fs, args)
	if err != nil || len(positional) != 1 {
		fmt.Fprint(a.stderr, legacyUsage)
		return exitUsage
	}
	client, credential, err := a.client(true)
	if err != nil {
		return a.fail(err)
	}
	ctx, cancel := a.context()
	defer cancel()
	confirm := func(intent *adminclient.LegacyIntent, fresh v2.LegacyPlanItemView) error {
		fmt.Fprint(a.stdout, adminclient.RenderLegacyIntent(intent, &fresh))
		answer := *confirmFlag
		if answer == "" {
			if !a.tty {
				return errors.New("confirmation required: pass --confirm " + intent.CampaignID + " or run interactively")
			}
			fmt.Fprintf(a.stdout, "type %q to send: ", intent.CampaignID)
			line, _ := bufio.NewReader(a.stdin).ReadString('\n')
			answer = strings.TrimSpace(line)
		}
		if answer != intent.CampaignID {
			return errors.New("confirmation did not match; nothing was sent")
		}
		return nil
	}
	intent, err := adminclient.SubmitLegacy(ctx, a.home, client, credential, positional[0], a.now, confirm)
	if err != nil {
		return a.failLegacy(intent, err)
	}
	return a.legacyOutcome(intent)
}

func (a *app) legacyStatus(args []string) int {
	fs := flag.NewFlagSet("legacy status", flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	positional, err := parse(fs, args)
	if err != nil || len(positional) != 1 {
		fmt.Fprint(a.stderr, legacyUsage)
		return exitUsage
	}
	client, credential, err := a.client(true)
	if err != nil {
		return a.fail(err)
	}
	ctx, cancel := a.context()
	defer cancel()
	intent, err := adminclient.ReconcileLegacy(ctx, a.home, client, credential, positional[0], a.now)
	if err != nil {
		return a.failLegacy(intent, err)
	}
	return a.legacyOutcome(intent)
}

func (a *app) legacyOutcome(intent *adminclient.LegacyIntent) int {
	fmt.Fprint(a.stdout, adminclient.RenderLegacyOutcome(intent))
	switch intent.Status {
	case adminclient.StatusCommitted:
		return exitOK
	case adminclient.StatusRejected:
		return exitRejected
	}
	return exitUnknown
}

// failLegacy reports an error with what is known about the intent; one that
// may already have committed exits with exitUnknown.
func (a *app) failLegacy(intent *adminclient.LegacyIntent, err error) int {
	fmt.Fprintln(a.stderr, "error:", err)
	if intent == nil || intent.Status != adminclient.StatusUnknown {
		return exitError
	}
	fmt.Fprint(a.stdout, adminclient.RenderLegacyOutcome(intent))
	return exitUnknown
}

func (a *app) legacyLocal(command string, args []string) int {
	if len(args) != 1 {
		fmt.Fprint(a.stderr, legacyUsage)
		return exitUsage
	}
	if command == "abandon" {
		intent, err := adminclient.AbandonLegacy(a.home, args[0], a.now())
		if err != nil {
			return a.fail(err)
		}
		fmt.Fprintf(a.stdout, "legacy intent %s abandoned; it was never sent\n", intent.ID)
		return exitOK
	}
	intent, err := a.home.LoadLegacyIntent(args[0])
	if err != nil {
		return a.fail(err)
	}
	a.printJSON(intent)
	return exitOK
}

func (a *app) legacyList() int {
	intents, err := a.home.ListLegacyIntents()
	if err != nil {
		return a.fail(err)
	}
	for _, intent := range intents {
		fmt.Fprintf(a.stdout, "%s  %-9s  %-10s  %s  %s  attempts=%d\n", intent.ID, intent.Status, intent.Operation, intent.RunID, intent.CampaignID, len(intent.Attempts))
	}
	return exitOK
}
